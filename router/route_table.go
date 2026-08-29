/*
   Copyright 2024 Josh Deprez

   Licensed under the Apache License, Version 2.0 (the "License");
   you may not use this file except in compliance with the License.
   You may obtain a copy of the License at

       http://www.apache.org/licenses/LICENSE-2.0

   Unless required by applicable law or agreed to in writing, software
   distributed under the License is distributed on an "AS IS" BASIS,
   WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
   See the License for the specific language governing permissions and
   limitations under the License.
*/

package router

import (
	"cmp"
	"context"
	"fmt"
	"iter"
	"maps"
	"slices"
	"sync"
	"time"

	"drjosh.dev/jrouter/status"
	"github.com/sfiera/multitalk/pkg/ddp"
)

const routingTableTemplate = `
<table>
	<thead><tr>
		<th>Network range</th>
		<th>Extended?</th>
		<th>Zone names</th>
		<th>Distance</th>
		<th>Learned via</th>
		<th>Last seen</th>
		<th>Valid?</th>
		<th>Target</th>
	</tr></thead>
	<tbody>
{{range $route := . }}
	<tr>
		<td>{{$route.NetStart}}{{if not (eq $route.NetStart $route.NetEnd)}} - {{$route.NetEnd}}{{end}}</td>
		<td>{{if $route.Extended}}extended{{else}}non-extended{{end}}</td>
		<td><ul>{{range $route.ZoneNames}}<li>{{.}}</li>{{end}}</ul></td>
		<td>{{$route.Distance}}</td>
		<td>{{$route.LearnedVia}}</td>
		<td>{{$route.LastSeen | ago}}</td>
		<td class="{{if $route.Valid}}green{{else}}red{{end}}">{{if $route.Valid}}valid{{else}}stale{{end}}</td>
		<td>{{$route.Target}}</td>
	</tr>
{{end}}
	</tbody>
</table>
`

// RouteTable is an in-memory database of routes.
type RouteTable struct {
	// byNetwork is used for packet forwarding, so it uses very fine-
	// grained locking and structures per network number. (There are only 2^16
	// of them, on a modern system that's tiny.)
	byNetwork [1 << 16]network

	// byClassMu divides routes broadly by target type.
	byClassMu [TargetClassCount]sync.RWMutex
	byClass   [TargetClassCount]map[RouteKey]Route

	// networksByZone maps zone names to network numbers.
	networksByZoneMu sync.RWMutex
	networksByZone   map[string][]ddp.Network

	// observers can observe changes to routing information
	observersMu sync.RWMutex
	observers   map[RouteTableObserver]struct{}
}

// NewRouteTable initialises a new empty route table.
func NewRouteTable(ctx context.Context) *RouteTable {
	rt := &RouteTable{
		observers:      make(map[RouteTableObserver]struct{}),
		networksByZone: make(map[string][]ddp.Network),
	}
	for i := range TargetClassCount {
		rt.byClass[i] = make(map[RouteKey]Route)
	}
	status.AddItem(ctx, "Routing table", routingTableTemplate, func(context.Context) (any, error) {
		rs := rt.Dump()
		slices.SortFunc(rs, func(ra, rb Route) int {
			return cmp.Compare(ra.NetStart, rb.NetStart)
		})
		return rs, nil
	})
	return rt
}

// AddObserver adds a route table observer.
func (rt *RouteTable) AddObserver(obs RouteTableObserver) {
	rt.observersMu.Lock()
	defer rt.observersMu.Unlock()
	rt.observers[obs] = struct{}{}
}

// RemoveObserver removes a route table observer.
func (rt *RouteTable) RemoveObserver(obs RouteTableObserver) {
	rt.observersMu.Lock()
	defer rt.observersMu.Unlock()
	delete(rt.observers, obs)
}

// Dump returns all routes in the table.
func (rt *RouteTable) Dump() (allRoutes []Route) {
	for i := range rt.byClass {
		func() {
			rt.byClassMu[i].RLock()
			defer rt.byClassMu[i].RUnlock()
			allRoutes = append(allRoutes, slices.Collect(maps.Values(rt.byClass[i]))...)
		}()
	}
	return allRoutes
}

// Lookup returns the best valid route for the network number. If there is no
// valid route, the zero Route is returned (it will have nil Target).
func (rt *RouteTable) Lookup(network ddp.Network) Route {
	rt.byNetwork[network].RLock()
	defer rt.byNetwork[network].RUnlock()

	// Routes are sorted by distance, so we can return the first valid route.
	for _, r := range rt.byNetwork[network].Routes {
		if r.Valid() {
			return r
		}
	}
	return Route{}
}

func (rt *RouteTable) lookupIgnoringAge(network ddp.Network) Route {
	rt.byNetwork[network].RLock()
	defer rt.byNetwork[network].RUnlock()

	for _, r := range rt.byNetwork[network].Routes {
		if r.validIgnoringAge() {
			return r
		}
	}
	return Route{}
}

// PruneExpiredRoutes removes AppleTalk-peer routes whose RTMP refresh age has
// exceeded maxRouteAge. Observer transitions are based on the route that was
// best immediately before expiry, so AURP peers receive the corresponding
// ND/NRC/NDC lifecycle update instead of silently retaining a route that
// Lookup has stopped using.
func (rt *RouteTable) PruneExpiredRoutes(now time.Time) int {
	class := TargetClassAppleTalkPeer

	var expired []Route
	func() {
		rt.byClassMu[class].Lock()
		defer rt.byClassMu[class].Unlock()

		for key, r := range rt.byClass[class] {
			if !r.expiredAt(now) {
				continue
			}
			expired = append(expired, r)
			delete(rt.byClass[class], key)
		}
	}()
	if len(expired) == 0 {
		return 0
	}

	oldBest := make(map[ddp.Network]Route, len(expired))
	affectedNetworks := make(Set[ddp.Network])
	expiredAt := make(map[RouteKey]time.Time, len(expired))
	for _, r := range expired {
		expiredAt[r.RouteKey] = r.LastSeen
		if _, seen := oldBest[r.NetStart]; !seen {
			oldBest[r.NetStart] = rt.lookupIgnoringAge(r.NetStart)
		}
		for n := r.NetStart; n <= r.NetEnd; n++ {
			affectedNetworks.Insert(n)
		}
	}

	// A route may have been refreshed after the class-index snapshot but
	// before this forwarding-index pass. Preserve any copy with a newer
	// LastSeen so maintenance never deletes a fresh RTMP update.
	for n := range affectedNetworks {
		func() {
			rt.byNetwork[n].Lock()
			defer rt.byNetwork[n].Unlock()

			rt.byNetwork[n].Routes = slices.DeleteFunc(
				rt.byNetwork[n].Routes,
				func(candidate Route) bool {
					lastSeen, stale := expiredAt[candidate.RouteKey]
					return stale && !candidate.LastSeen.After(lastSeen)
				},
			)
		}()
		rt.clearZonesForNetworkIfNoRoutes(n)
	}

	starts := slices.Sorted(maps.Keys(oldBest))
	for _, n := range starts {
		rt.informObservers(oldBest[n], rt.Lookup(n))
	}
	return len(expired)
}

// RunMaintenance periodically retires stale RTMP-learned routes. AURP routes
// are connection-owned and are removed synchronously when their receiver-side
// connection is lost, so only AppleTalk-peer ageing is handled here.
func (rt *RouteTable) RunMaintenance(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			rt.PruneExpiredRoutes(now)
		}
	}
}

// DeleteTarget deletes the route target and all its routes.
func (rt *RouteTable) DeleteTarget(target RouteTarget) {
	class := target.Class()
	targetKey := target.RouteTargetKey()

	// Remove the target's stored route records first, but retain copies so we
	// can remove the same RouteKeys from the per-network forwarding indexes.
	var deleted []Route
	func() {
		rt.byClassMu[class].Lock()
		defer rt.byClassMu[class].Unlock()

		for key, r := range rt.byClass[class] {
			if r.TargetKey != targetKey {
				continue
			}
			deleted = append(deleted, r)
			delete(rt.byClass[class], key)
		}
	}()
	if len(deleted) == 0 {
		return
	}

	// Capture the best route at each deleted range start before mutating the
	// forwarding indexes. One observer transition per stored route range is
	// enough; the Route itself carries the extended range end.
	oldBest := make(map[ddp.Network]Route, len(deleted))
	affectedNetworks := make(Set[ddp.Network])
	for _, r := range deleted {
		oldBest[r.NetStart] = rt.Lookup(r.NetStart)
		for n := r.NetStart; n <= r.NetEnd; n++ {
			affectedNetworks.Insert(n)
		}
	}

	for n := range affectedNetworks {
		func() {
			rt.byNetwork[n].Lock()
			defer rt.byNetwork[n].Unlock()

			rt.byNetwork[n].Routes = slices.DeleteFunc(
				rt.byNetwork[n].Routes,
				func(r Route) bool {
					return r.TargetKey == targetKey
				},
			)
		}()
		rt.clearZonesForNetworkIfNoRoutes(n)
	}

	starts := slices.Sorted(maps.Keys(oldBest))
	for _, n := range starts {
		rt.informObservers(oldBest[n], rt.Lookup(n))
	}
}

// DeleteRoute deletes the route specified by the (target, netStart) tuple.
func (rt *RouteTable) DeleteRoute(target RouteTarget, netStart ddp.Network) error {
	class := target.Class()
	routeKey := RouteKey{
		TargetKey: target.RouteTargetKey(),
		NetStart:  netStart,
	}

	// Capture the old best route for observer comparisons. It may be zero
	// even though the specific stored route exists (for example, an AURP
	// route learned before its zone information arrives).
	oldBest := rt.Lookup(netStart)

	// Find and delete the route from byClass. Existence of the requested route,
	// not current routability, determines whether DeleteRoute may proceed.
	route, exists := func() (Route, bool) {
		rt.byClassMu[class].Lock()
		defer rt.byClassMu[class].Unlock()

		route, found := rt.byClass[class][routeKey]
		delete(rt.byClass[class], routeKey)
		return route, found
	}()
	if !exists {
		return fmt.Errorf("route %v not found", routeKey)
	}

	// Delete the route from byNetwork.
	for n := route.NetStart; n <= route.NetEnd; n++ {
		func() {
			rt.byNetwork[n].Lock()
			defer rt.byNetwork[n].Unlock()

			rt.byNetwork[n].Routes = slices.DeleteFunc(rt.byNetwork[n].Routes, func(r Route) bool {
				return r.RouteKey == routeKey
			})
		}()
		rt.clearZonesForNetworkIfNoRoutes(n)
	}

	newBest := rt.Lookup(route.NetStart)
	rt.informObservers(oldBest, newBest)
	return nil
}

// find looks up a route by target and network range start.
func (rt *RouteTable) find(target RouteTarget, netStart ddp.Network) Route {
	class := target.Class()
	routeKey := RouteKey{
		TargetKey: target.RouteTargetKey(),
		NetStart:  netStart,
	}

	rt.byClassMu[class].RLock()
	defer rt.byClassMu[class].RUnlock()
	return rt.byClass[class][routeKey]
}

// UpdateDistance updates the distance for an existing route.
func (rt *RouteTable) UpdateDistance(target RouteTarget, netStart ddp.Network, distance uint8) error {
	if distance > maxRouteDistance {
		return fmt.Errorf("route distance too high (%d > %d)", distance, maxRouteDistance)
	}
	class := target.Class()

	// The route can legitimately exist before it is valid for Lookup (notably
	// while AURP zone information is still pending), so find the stored route
	// independently from the old best route.
	oldBest := rt.Lookup(netStart)

	oldRoute := rt.find(target, netStart)
	if oldRoute.Zero() {
		return fmt.Errorf("route (%v,%d) not found", target, netStart)
	}

	newRoute := oldRoute // shallow clone

	newRoute.LastSeen = time.Now()
	if distance != oldRoute.Distance {
		newRoute.Distance = distance
	}

	func() {
		rt.byClassMu[class].Lock()
		defer rt.byClassMu[class].Unlock()
		rt.byClass[class][oldRoute.RouteKey] = newRoute
	}()

	for n := oldRoute.NetStart; n <= oldRoute.NetEnd; n++ {
		func() {
			rt.byNetwork[n].Lock()
			defer rt.byNetwork[n].Unlock()
			for i, r := range rt.byNetwork[n].Routes {
				if r.RouteKey == oldRoute.RouteKey {
					rt.byNetwork[n].Routes[i] = newRoute
				}
			}
			slices.SortFunc(rt.byNetwork[n].Routes, func(a, b Route) int {
				return cmp.Compare(a.Distance, b.Distance)
			})
		}()
	}

	newBest := rt.Lookup(oldRoute.NetStart)
	rt.informObservers(oldBest, newBest)

	return nil
}

// UpsertRoute validates and inserts a new route or updates an existing route.
// It always returns a new Route (new route structs fully replace old ones).
func (rt *RouteTable) UpsertRoute(target RouteTarget, extended bool, netStart, netEnd ddp.Network, distance uint8) (Route, error) {
	if netStart > netEnd {
		return Route{}, fmt.Errorf("invalid network range [%d, %d]", netStart, netEnd)
	}
	if netStart != netEnd && !extended {
		return Route{}, fmt.Errorf("invalid network range [%d, %d] for nonextended network", netStart, netEnd)
	}
	if distance > maxRouteDistance {
		return Route{}, fmt.Errorf("route distance too high (%d > %d)", distance, maxRouteDistance)
	}

	oldBest := rt.Lookup(netStart) // may not exist yet

	class := target.Class()
	key := RouteKey{
		TargetKey: target.RouteTargetKey(),
		NetStart:  netStart,
	}

	newRoute := Route{
		RouteKey: key,
		Extended: extended,
		NetEnd:   netEnd,
		Target:   target,
		Origin:   routeOriginForTarget(target),
		Distance: distance,
		LastSeen: time.Now(),

		network: &rt.byNetwork[netStart],
	}

	oldRoute := rt.find(target, netStart)
	update := !oldRoute.Zero()
	func() {
		rt.byClassMu[class].Lock()
		defer rt.byClassMu[class].Unlock()
		rt.byClass[class][key] = newRoute
	}()

	if update {
		for n := oldRoute.NetStart; n <= oldRoute.NetEnd; n++ {
			func() {
				rt.byNetwork[n].Lock()
				defer rt.byNetwork[n].Unlock()
				rt.byNetwork[n].Routes = slices.DeleteFunc(
					rt.byNetwork[n].Routes,
					func(r Route) bool { return r.RouteKey == key },
				)
			}()
		}
	}

	for n := netStart; n <= netEnd; n++ {
		func() {
			rt.byNetwork[n].Lock()
			defer rt.byNetwork[n].Unlock()

			rt.byNetwork[n].Routes = append(rt.byNetwork[n].Routes, newRoute)
			slices.SortFunc(rt.byNetwork[n].Routes, func(a, b Route) int {
				return cmp.Compare(a.Distance, b.Distance)
			})
		}()
	}

	if update {
		for n := oldRoute.NetStart; n <= oldRoute.NetEnd; n++ {
			if n < netStart || n > netEnd {
				rt.clearZonesForNetworkIfNoRoutes(n)
			}
		}
	}

	newBest := rt.Lookup(netStart)
	rt.informObservers(oldBest, newBest)

	return newRoute, nil
}

func (rt *RouteTable) informObservers(oldBest, newBest Route) {
	rt.observersMu.RLock()
	defer rt.observersMu.RUnlock()

	switch {
	case oldBest.Zero() && newBest.Zero():
		// neither old nor new route is valid (yet), no notifying.

	case oldBest.Zero(): // newBest.Target != nil
		for o := range rt.observers {
			o.NetworkAdded(newBest)
		}

	case newBest.Zero(): // oldBest != nil
		for o := range rt.observers {
			o.NetworkDeleted(oldBest)
		}

	case oldBest.TargetKey != newBest.TargetKey ||
		oldBest.Distance != newBest.Distance ||
		oldBest.Extended != newBest.Extended ||
		oldBest.NetEnd != newBest.NetEnd:
		for o := range rt.observers {
			o.BestNetworkChanged(oldBest, newBest)
		}
	}
}

// ValidRoutes yields all valid routes.
func (rt *RouteTable) ValidRoutes(yield func(Route) bool) {
	for c := range TargetClassCount {
		for r := range rt.ValidRoutesForClass(c) {
			if !yield(r) {
				return
			}
		}
	}
}

// ValidRoutesForClass returns an iterator that yields all valid routes for a
// given target class.
func (rt *RouteTable) ValidRoutesForClass(class TargetClass) iter.Seq[Route] {
	return func(yield func(Route) bool) {
		rt.byClassMu[class].RLock()
		defer rt.byClassMu[class].RUnlock()

		for _, r := range rt.byClass[class] {
			if !r.Valid() {
				continue
			}
			if !yield(r) {
				return
			}
		}
	}
}

// RouteKey is a comparable struct for identifying a specific route.
// A route can be specified by the target and the start of the network range.
type RouteKey struct {
	TargetKey string
	NetStart  ddp.Network
}

// RouteTableObserver implementations can receive notifications of route table
// changes. (TODO, not yet implemented)
type RouteTableObserver interface {
	// NetworkAdded is called when a network becomes routable.
	NetworkAdded(best Route)

	// NetworkDeleted is called when a network becomes _un_routable.
	NetworkDeleted(oldBest Route)

	// BestNetworkChanged is called when the best routing distance
	// or route for a network has changed (from e.g. a direct EtherTalk
	// connection to an AURP peer).
	BestNetworkChanged(from, to Route)
}

// network is a data structure representing an AppleTalk network.
type network struct {
	sync.RWMutex
	Routes    []Route
	ZoneNames Set[string]
}
