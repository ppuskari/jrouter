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

	"github.com/sfiera/multitalk/pkg/ddp"
)

const maxRouteAge = 10 * time.Minute // TODO: confirm

// Route represents a route: a destination network range, a way to send packets
// towards the destination, and some other data that affects whether the route
// is used.
type Route struct {
	RouteKey

	Extended bool
	NetEnd   ddp.Network

	// Target provides a way to forward packets using this route
	Target RouteTarget

	Distance uint8
	LastSeen time.Time

	// ZoneNames may be empty between learning the existence of a route and
	// receiving zone information.
	ZoneNames StringSet
}

func (r Route) LastSeenAgo() string {
	return ago(r.LastSeen)
}

// Valid reports whether the route is valid.
// A valid route has one or more zone names, and if it is learned from a peer
// router over EtherTalk is not too old.
func (r *Route) Valid() bool {
	if len(r.ZoneNames) == 0 {
		return false
	}
	if _, isEtherTalkPeer := r.Target.(*EtherTalkPeer); isEtherTalkPeer {
		return time.Since(r.LastSeen) <= maxRouteAge
	}
	return true
}

// RouteTarget implementations can forward packets somewhere.
type RouteTarget interface {
	// Forward should send the packet to the route target.
	Forward(context.Context, *ddp.ExtPacket) error

	// Class returns the target class for this target.
	Class() TargetClass

	// RouteTargetKey is used for determining if two targets are the same.
	RouteTargetKey() string
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
	// NetworkAdded is called when networks become routable.
	NetworkAdded(*Route)

	// NetworkDeleted is called when networks become _un_routable.
	NetworkDeleted(*Route)

	// NetworkDistanceChanged is called when the best routing distance
	// for a network has changed.
	NetworkDistanceChanged(*Route)

	// NetworkRouteChanged is called when the best routing path for a network
	// has changed (from e.g. a direct EtherTalk connection to an AURP peer).
	NetworkRouteChanged(*Route)
}

// RouteTable is an in-memory database of routes.
type RouteTable struct {
	// byNetwork is used for packet forwarding, so it uses very fine-
	// grained locking and structures per network number. (There are only 2^16
	// of them, on a modern system that's tiny.)
	byNetworkMu [1 << 16]sync.RWMutex
	byNetwork   [1 << 16][]*Route

	// byClassMu divides routes broadly by target type.
	byClassMu [TargetClassCount]sync.RWMutex
	byClass   [TargetClassCount]map[RouteKey]*Route

	// networksByZone maps zone names to network numbers.
	networksByZoneMu sync.RWMutex
	networksByZone   map[string][]ddp.Network

	// observers can observe changes to routing information
	observersMu sync.RWMutex
	observers   map[RouteTableObserver]struct{}
}

// NewRouteTable initialises a new empty route table.
func NewRouteTable() *RouteTable {
	rt := &RouteTable{
		observers:      make(map[RouteTableObserver]struct{}),
		networksByZone: make(map[string][]ddp.Network),
	}
	for i := range TargetClassCount {
		rt.byClass[i] = make(map[RouteKey]*Route)
	}
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

func (rt *RouteTable) notifyObservers(r *Route, event func(RouteTableObserver, *Route)) {
	rt.observersMu.RLock()
	defer rt.observersMu.RUnlock()
	for o := range rt.observers {
		event(o, r)
	}
}

// Dump returns all routes in the table.
func (rt *RouteTable) Dump() (allRoutes []*Route) {
	for i := range rt.byClass {
		func() {
			rt.byClassMu[i].RLock()
			defer rt.byClassMu[i].RUnlock()
			allRoutes = append(allRoutes, slices.Collect(maps.Values(rt.byClass[i]))...)
		}()
	}
	return allRoutes
}

// Lookup returns the best valid route for the network number.
func (rt *RouteTable) Lookup(network ddp.Network) *Route {
	rt.byNetworkMu[network].RLock()
	defer rt.byNetworkMu[network].RUnlock()

	// Routes are sorted by distance, so we can return the first valid route.
	for _, r := range rt.byNetwork[network] {
		if r.Valid() {
			return r
		}
	}
	return nil
}

// DeleteTarget deletes the route target and all its routes.
func (rt *RouteTable) DeleteTarget(target RouteTarget) {
	class := target.Class()
	targetKey := target.RouteTargetKey()

	type routeChange struct{ oldBest, newBest *Route }
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
			rt.byNetworkMu[n].Lock()
			defer rt.byNetworkMu[n].Unlock()

			oldRoutes := rt.byNetwork[n]
			newRoutes := make([]*Route, 0, len(oldRoutes))
			for _, route := range oldRoutes {
				if rc.oldBest == nil && route.Valid() {
					rc.oldBest = route
				}
				if route.Target.RouteTargetKey() == targetKey {
					continue
				}
				newRoutes = append(newRoutes, route)
				if rc.newBest == nil && route.Valid() {
					rc.newBest = route
				}
			}
			rt.byNetwork[n] = newRoutes
		}()
	}

	// Notify observers of necessary changes
	for _, rc := range routeChanges {
		rt.notifyObserversOfChange(rc.oldBest, rc.newBest)
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
	if oldBest == nil {
		return fmt.Errorf("network %d not found", netStart)
	}

	// Find and delete the route from byClass
	route := func() *Route {
		rt.byClassMu[class].Lock()
		defer rt.byClassMu[class].Unlock()
		defer delete(rt.byClass[class], routeKey)
		return rt.byClass[class][routeKey]
	}()
	if route == nil {
		return fmt.Errorf("route %v not found", routeKey)
	}

	// Delete the route from byNetwork.
	for n := route.NetStart; n <= route.NetEnd; n++ {
		func() {
			rt.byNetworkMu[n].Lock()
			defer rt.byNetworkMu[n].Unlock()

			oldRoutes := rt.byNetwork[n]
			newRoutes := make([]*Route, 0, len(oldRoutes))
			for _, r := range oldRoutes {
				if r.TargetKey != routeKey.TargetKey {
					newRoutes = append(newRoutes, r)
				}
			}
			rt.byNetwork[n] = newRoutes
		}()
	}

	newBest := rt.Lookup(route.NetStart)
	rt.notifyObserversOfChange(oldBest, newBest)
	return nil
}

// find looks up a route by target and network range start.
func (rt *RouteTable) find(target RouteTarget, netStart ddp.Network) (*Route, error) {
	class := target.Class()
	routeKey := RouteKey{
		TargetKey: target.RouteTargetKey(),
		NetStart:  netStart,
	}

	rt.byClassMu[class].RLock()
	defer rt.byClassMu[class].RUnlock()
	route := rt.byClass[class][routeKey]
	if route == nil {
		return nil, fmt.Errorf("route %v not found", routeKey)
	}
	return route, nil
}

// UpdateRoute updates the distance for an existing route.
func (rt *RouteTable) UpdateRoute(target RouteTarget, netStart ddp.Network, distance uint8) error {
	oldBest := rt.Lookup(netStart)
	if oldBest == nil {
		return fmt.Errorf("network %d not found", netStart)
	}

	route, err := rt.find(target, netStart)
	if err != nil {
		return err
	}

	route.LastSeen = time.Now()
	if distance != route.Distance {
		route.Distance = distance
	}

	for n := route.NetStart; n <= route.NetEnd; n++ {
		func() {
			rt.byNetworkMu[n].Lock()
			defer rt.byNetworkMu[n].Unlock()
			slices.SortFunc(rt.byNetwork[n], func(a, b *Route) int {
				return cmp.Compare(a.Distance, b.Distance)
			})
		}()
	}

	newBest := rt.Lookup(route.NetStart)
	rt.notifyObserversOfChange(oldBest, newBest)

	return nil
}

// UpsertRoute inserts a new route or updates an existing route.
func (rt *RouteTable) UpsertRoute(target RouteTarget, extended bool, netStart, netEnd ddp.Network, metric uint8) (*Route, error) {
	if netStart > netEnd {
		return nil, fmt.Errorf("invalid network range [%d, %d]", netStart, netEnd)
	}
	if netStart != netEnd && !extended {
		return nil, fmt.Errorf("invalid network range [%d, %d] for nonextended network", netStart, netEnd)
	}

	oldBest := rt.Lookup(netStart) // may not exist yet

	class := target.Class()
	key := RouteKey{
		TargetKey: target.RouteTargetKey(),
		NetStart:  netStart,
	}

	var route *Route
	insert := false
	update := false
	func() {
		rt.byClassMu[class].Lock()
		defer rt.byClassMu[class].Unlock()
		route = rt.byClass[class][key]
		if route != nil {
			// Update
			route.LastSeen = time.Now()
			if route.Distance != metric {
				route.Distance = metric
				update = true
			}
			return
		}
		// Route insert.
		insert = true
		route = &Route{
			RouteKey: key,
			Extended: extended,
			NetEnd:   netEnd,
			Target:   target,
			Distance: metric,
			LastSeen: time.Now(),
		}
		rt.byClass[class][key] = route
	}()

	if !insert && !update {
		return route, nil
	}

	for n := netStart; n <= netEnd; n++ {
		func() {
			rt.byNetworkMu[n].Lock()
			defer rt.byNetworkMu[n].Unlock()

			if insert {
				rt.byNetwork[n] = append(rt.byNetwork[n], route)
			}
			slices.SortFunc(rt.byNetwork[n], func(a, b *Route) int {
				return cmp.Compare(a.Distance, b.Distance)
			})
		}()
	}

	newBest := rt.Lookup(netStart)
	rt.notifyObserversOfChange(oldBest, newBest)

	return route, nil
}

func (rt *RouteTable) notifyObserversOfChange(oldBest, newBest *Route) {
	switch {
	case oldBest == nil && newBest == nil:
		// neither old nor new route is valid (yet)

	case oldBest == nil: // newBest != nil
		rt.notifyObservers(newBest, RouteTableObserver.NetworkAdded)

	case newBest == nil: // oldBest != nil
		rt.notifyObservers(oldBest, RouteTableObserver.NetworkDeleted)

	case oldBest.TargetKey != newBest.TargetKey:
		rt.notifyObservers(newBest, RouteTableObserver.NetworkRouteChanged)

	case oldBest.Distance != newBest.Distance:
		rt.notifyObservers(newBest, RouteTableObserver.NetworkDistanceChanged)
	}
}

// ValidRoutes yields all valid routes.
func (rt *RouteTable) ValidRoutes(yield func(*Route) bool) {
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
func (rt *RouteTable) ValidRoutesForClass(class TargetClass) iter.Seq[*Route] {
	return func(yield func(*Route) bool) {
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

// TargetClass is an enum type for representing the broad classes of route
// targets.
type TargetClass int

// Target class values.
const (
	TargetClassDirect        TargetClass = iota // directly attached EtherTalk / LocalTalk / etc network
	TargetClassAURPPeer                         // another router over AURP
	TargetClassAppleTalkPeer                    // another router via EtherTalk / LocalTalk / etc
	TargetClassCount                            // how many valid target types there are - insert new classes above.
)
