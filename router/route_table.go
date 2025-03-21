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

// DeleteTarget deletes the route target and all its routes.
func (rt *RouteTable) DeleteTarget(target RouteTarget) {
	class := target.Class()
	targetKey := target.RouteTargetKey()

	type routeChange struct{ from, to Route }
	var routeChanges []*routeChange
	networks := make(map[ddp.Network]*routeChange)

	// Scan routesByTargetClass to find and delete routes for the target.
	func() {
		rt.byClassMu[class].Lock()
		defer rt.byClassMu[class].Unlock()
		for _, r := range rt.byClass[class] {
			if r.TargetKey != targetKey {
				continue
			}
			rc := new(routeChange)
			routeChanges = append(routeChanges, rc)
			for n := r.NetStart; n <= r.NetEnd; n++ {
				networks[n] = rc
			}
			delete(rt.byClass[class], r.RouteKey)
		}
	}()

	// Delete target routes from each network number.
	for n, rc := range networks {
		func() {
			rt.byNetwork[n].Lock()
			defer rt.byNetwork[n].Unlock()

			oldRoutes := rt.byNetwork[n].Routes
			newRoutes := make([]Route, 0, len(oldRoutes))
			for _, r := range oldRoutes {
				if rc.from.Zero() && r.Valid() {
					rc.from = r
				}
				if r.Target.RouteTargetKey() == targetKey {
					continue
				}
				newRoutes = append(newRoutes, r)
				if rc.to.Zero() && r.Valid() {
					rc.to = r
				}
			}
			rt.byNetwork[n].Routes = newRoutes
		}()
	}

	for _, rc := range routeChanges {
		rt.informObservers(rc.from, rc.to)
	}
}

// DeleteRoute deletes the route specified by the (target, netStart) tuple.
func (rt *RouteTable) DeleteRoute(target RouteTarget, netStart ddp.Network) error {
	class := target.Class()
	routeKey := RouteKey{
		TargetKey: target.RouteTargetKey(),
		NetStart:  netStart,
	}

	// Lookup the old best route for the network for comparisons.
	oldBest := rt.Lookup(netStart)
	if oldBest.Zero() {
		return fmt.Errorf("network %d not found", netStart)
	}

	// Find and delete the route from byClass
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
				return r.TargetKey == routeKey.TargetKey
			})
		}()
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
	oldBest := rt.Lookup(netStart)
	if oldBest.Zero() {
		return fmt.Errorf("network %d not found", netStart)
	}

	oldRoute := rt.find(target, netStart)
	if oldRoute.Zero() {
		return fmt.Errorf("route (%v,%d) not found", target, netStart)
	}

	newRoute := oldRoute // shallow clone

	newRoute.LastSeen = time.Now()
	if distance != oldRoute.Distance {
		newRoute.Distance = distance
	}

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

// UpsertRoute inserts a new route or updates an existing route. It always
// returns a new Route.
func (rt *RouteTable) UpsertRoute(target RouteTarget, extended bool, netStart, netEnd ddp.Network, metric uint8) (Route, error) {
	if netStart > netEnd {
		return Route{}, fmt.Errorf("invalid network range [%d, %d]", netStart, netEnd)
	}
	if netStart != netEnd && !extended {
		return Route{}, fmt.Errorf("invalid network range [%d, %d] for nonextended network", netStart, netEnd)
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
		Distance: metric,
		LastSeen: time.Now(),

		network: &rt.byNetwork[netStart],
	}

	update := false
	func() {
		rt.byClassMu[class].Lock()
		defer rt.byClassMu[class].Unlock()
		_, update = rt.byClass[class][key]
		rt.byClass[class][key] = newRoute
	}()

	for n := netStart; n <= netEnd; n++ {
		func() {
			rt.byNetwork[n].Lock()
			defer rt.byNetwork[n].Unlock()

			if update {
				for i, r := range rt.byNetwork[n].Routes {
					if r.RouteKey == key {
						rt.byNetwork[n].Routes[i] = newRoute
					}
				}
			} else {
				rt.byNetwork[n].Routes = append(rt.byNetwork[n].Routes, newRoute)
			}

			slices.SortFunc(rt.byNetwork[n].Routes, func(a, b Route) int {
				return cmp.Compare(a.Distance, b.Distance)
			})
		}()
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

	case oldBest.TargetKey != newBest.TargetKey || oldBest.Distance != newBest.Distance:
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
	ZoneNames StringSet
}
