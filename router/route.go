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
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/sfiera/multitalk/pkg/ddp"
)

const maxRouteAge = 10 * time.Minute // TODO: confirm

// RouteTarget implementations can forward packets somewhere.
type RouteTarget interface {
	// Forward should send the packet to the route target.
	Forward(context.Context, *ddp.ExtPacket) error

	// RouteTargetKey is used for determining if two targets are the same.
	RouteTargetKey() string
}

// RouteKey is a comparable struct for identifying a specific route.
// A route can be specified by the target and the start of the network range.
type RouteKey struct {
	TargetKey string
	NetStart  ddp.Network
}

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
	_, isEtherTalkPeer := r.Target.(*EtherTalkPeer)
	return len(r.ZoneNames) > 0 && (isEtherTalkPeer || time.Since(r.LastSeen) <= maxRouteAge)
}

type RouteTableObserver interface {
	RouteAdded(*Route)
	RouteDeleted(*Route)
	RouteDistanceChanged(*Route)
	RouteForwarderChanged(*Route)
}

type RouteTable struct {
	// allRoutes is used for maintenance operations.
	allRoutesMu sync.RWMutex
	allRoutes   map[RouteKey]*Route

	// routesByNetwork is used for packet forwarding, so it uses very fine-
	// grained locking and structures per network number. (There are only 2^16
	// of them, on a modern system that's tiny.)
	routesByNetworkMu [1 << 16]sync.RWMutex
	routesByNetwork   [1 << 16][]*Route

	// observers can observe
	observersMu sync.RWMutex
	observers   map[RouteTableObserver]struct{}
}

func NewRouteTable() *RouteTable {
	return &RouteTable{
		observers: make(map[RouteTableObserver]struct{}),
		allRoutes: make(map[RouteKey]*Route),
	}
}

func (rt *RouteTable) AddObserver(obs RouteTableObserver) {
	rt.observersMu.Lock()
	defer rt.observersMu.Unlock()
	rt.observers[obs] = struct{}{}
}

func (rt *RouteTable) RemoveObserver(obs RouteTableObserver) {
	rt.observersMu.Lock()
	defer rt.observersMu.Unlock()
	delete(rt.observers, obs)
}

// Dump returns all routes in the table.
func (rt *RouteTable) Dump() []*Route {
	rt.allRoutesMu.RLock()
	defer rt.allRoutesMu.RUnlock()
	return slices.Collect(maps.Values(rt.allRoutes))
}

// Lookup returns the best valid route for the network number.
func (rt *RouteTable) Lookup(network ddp.Network) *Route {
	rt.routesByNetworkMu[network].RLock()
	defer rt.routesByNetworkMu[network].RUnlock()

	// Routes are sorted by distance, so we can return the first valid route.
	for _, r := range rt.routesByNetwork[network] {
		if !r.Valid() {
			continue
		}
		return r
	}
	return nil
}

// DeleteTarget deletes the route target and all its routes.
func (rt *RouteTable) DeleteTarget(target RouteTarget) {
	targetKey := target.RouteTargetKey()
	networks := make(map[ddp.Network]struct{})

	// Scan allRoutes to find and delete routes for the target.
	func() {
		rt.allRoutesMu.Lock()
		defer rt.allRoutesMu.Unlock()
		for _, r := range rt.allRoutes {
			if r.TargetKey != targetKey {
				continue
			}
			for n := r.NetStart; n <= r.NetEnd; n++ {
				networks[n] = struct{}{}
			}
			delete(rt.allRoutes, r.RouteKey)
		}
	}()

	// Delete target routes from each network number.
	for n := range networks {
		func() {
			rt.routesByNetworkMu[n].Lock()
			defer rt.routesByNetworkMu[n].Unlock()

			oldRoutes := rt.routesByNetwork[n]
			newRoutes := make([]*Route, 0, len(oldRoutes))
			for _, route := range oldRoutes {
				if route.Target.RouteTargetKey() == targetKey {
					continue
				}
				newRoutes = append(newRoutes, route)
			}
			rt.routesByNetwork[n] = newRoutes
		}()
	}
}

// DeleteRoute deletes the route specified by the (target, netStart) tuple.
func (rt *RouteTable) DeleteRoute(target RouteTarget, netStart ddp.Network) error {
	routeKey := RouteKey{
		TargetKey: target.RouteTargetKey(),
		NetStart:  netStart,
	}

	// Find and delete the route from allRoutes.
	route := func() *Route {
		rt.allRoutesMu.Lock()
		defer rt.allRoutesMu.Unlock()
		defer delete(rt.allRoutes, routeKey)
		return rt.allRoutes[routeKey]
	}()
	if route == nil {
		return fmt.Errorf("route %v not found", routeKey)
	}

	// Delete the route from routesByNetwork.
	for n := route.NetStart; n <= route.NetEnd; n++ {
		func() {
			rt.routesByNetworkMu[n].Lock()
			defer rt.routesByNetworkMu[n].Unlock()

			oldRoutes := rt.routesByNetwork[n]
			newRoutes := make([]*Route, 0, len(oldRoutes))
			for _, r := range oldRoutes {
				if r.TargetKey != routeKey.TargetKey {
					newRoutes = append(newRoutes, r)
				}
			}
			rt.routesByNetwork[n] = newRoutes
		}()
	}

	return nil
}

// find looks up a route by target and network range start.
func (rt *RouteTable) find(target RouteTarget, netStart ddp.Network) (*Route, error) {
	routeKey := RouteKey{
		TargetKey: target.RouteTargetKey(),
		NetStart:  netStart,
	}

	rt.allRoutesMu.RLock()
	defer rt.allRoutesMu.RUnlock()
	route := rt.allRoutes[routeKey]
	if route == nil {
		return nil, fmt.Errorf("route %v not found", routeKey)
	}
	return route, nil
}

// UpdateRoute updates the distance for an existing route.
func (rt *RouteTable) UpdateRoute(target RouteTarget, netStart ddp.Network, distance uint8) error {
	route, err := rt.find(target, netStart)
	if err != nil {
		return err
	}

	route.Distance = distance
	route.LastSeen = time.Now()
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

	routeKey := RouteKey{
		TargetKey: target.RouteTargetKey(),
		NetStart:  netStart,
	}

	var route *Route
	insert := false
	update := false
	func() {
		rt.allRoutesMu.Lock()
		defer rt.allRoutesMu.Unlock()
		route = rt.allRoutes[routeKey]
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
			RouteKey: routeKey,
			Extended: extended,
			NetEnd:   netEnd,
			Target:   target,
			Distance: metric,
			LastSeen: time.Now(),
		}
		rt.allRoutes[routeKey] = route
	}()

	if !insert && !update {
		return route, nil
	}

	for n := netStart; n <= netEnd; n++ {
		func() {
			rt.routesByNetworkMu[n].Lock()
			defer rt.routesByNetworkMu[n].Unlock()

			if insert {
				rt.routesByNetwork[n] = append(rt.routesByNetwork[n], route)
			}
			slices.SortFunc(rt.routesByNetwork[n], func(a, b *Route) int {
				return cmp.Compare(a.Distance, b.Distance)
			})
		}()
	}

	return route, nil
}

// ValidRoutes returns all valid routes.
func (rt *RouteTable) ValidRoutes() []*Route {
	rt.allRoutesMu.RLock()
	defer rt.allRoutesMu.RUnlock()

	valid := make([]*Route, 0, len(rt.allRoutes))
	for _, r := range rt.allRoutes {
		if r.Valid() {
			valid = append(valid, r)
		}
	}
	return valid
}

// ValidLocalRoutes returns all valid routes that were not learned via AURP.
func (rt *RouteTable) ValidLocalRoutes() []*Route {
	rt.allRoutesMu.RLock()
	defer rt.allRoutesMu.RUnlock()

	valid := make([]*Route, 0, len(rt.allRoutes))
	for _, r := range rt.allRoutes {
		if _, isAURP := r.Target.(*AURPPeer); isAURP {
			continue
		}
		if r.Valid() {
			valid = append(valid, r)
		}
	}
	return valid
}
