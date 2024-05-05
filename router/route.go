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
	"fmt"
	"sync"
	"time"

	"github.com/sfiera/multitalk/pkg/ddp"
)

const maxRouteAge = 10 * time.Minute // TODO: confirm

type Route struct {
	Extended bool
	NetStart ddp.Network
	NetEnd   ddp.Network
	Distance uint8

	LastSeen time.Time

	// ZoneNames may be empty between learning the existence of a route and
	// receiving zone information.
	ZoneNames StringSet

	// Exactly one of the following should be set
	AURPPeer        *AURPPeer      // Next hop is this peer router (over AURP)
	EtherTalkPeer   *EtherTalkPeer // Next hop is this peer router (over EtherTalk)
	EtherTalkDirect *EtherTalkPort // Directly connected to this network (via EtherTalk)
}

func (r Route) LastSeenAgo() string {
	if r.LastSeen.IsZero() {
		return "never"
	}
	return fmt.Sprintf("%v ago", time.Since(r.LastSeen).Truncate(time.Millisecond))
}

// Valid reports whether the route is valid.
// A valid route has one or more zone names, and if it is learned from a peer
// router over EtherTalk is not too old.
func (r *Route) Valid() bool {
	return len(r.ZoneNames) > 0 && (r.EtherTalkPeer == nil || time.Since(r.LastSeen) <= maxRouteAge)
}

type RouteTable struct {
	mu     sync.Mutex
	routes map[*Route]struct{}
}

func NewRouteTable() *RouteTable {
	return &RouteTable{
		routes: make(map[*Route]struct{}),
	}
}

func (rt *RouteTable) InsertEtherTalkDirect(port *EtherTalkPort) {
	r := &Route{
		Extended:        true,
		NetStart:        port.NetStart,
		NetEnd:          port.NetEnd,
		Distance:        0, // we're connected directly
		LastSeen:        time.Now(),
		ZoneNames:       port.AvailableZones,
		EtherTalkDirect: port,
	}

	rt.mu.Lock()
	defer rt.mu.Unlock()
	rt.routes[r] = struct{}{}
}

func (rt *RouteTable) Dump() []Route {
	rt.mu.Lock()
	defer rt.mu.Unlock()

	table := make([]Route, 0, len(rt.routes))
	for r := range rt.routes {
		table = append(table, *r)
	}
	return table
}

func (rt *RouteTable) LookupRoute(network ddp.Network) *Route {
	rt.mu.Lock()
	defer rt.mu.Unlock()

	var bestRoute *Route
	for r := range rt.routes {
		if network < r.NetStart || network > r.NetEnd {
			continue
		}
		if !r.Valid() {
			continue
		}
		if bestRoute == nil {
			bestRoute = r
			continue
		}
		if r.Distance < bestRoute.Distance {
			bestRoute = r
		}
	}
	return bestRoute
}

func (rt *RouteTable) DeleteAURPPeer(peer *AURPPeer) {
	rt.mu.Lock()
	defer rt.mu.Unlock()

	for route := range rt.routes {
		if route.AURPPeer == peer {
			delete(rt.routes, route)
		}
	}
}

func (rt *RouteTable) DeleteAURPPeerNetwork(peer *AURPPeer, network ddp.Network) {
	rt.mu.Lock()
	defer rt.mu.Unlock()

	for route := range rt.routes {
		if route.AURPPeer == peer && route.NetStart == network {
			delete(rt.routes, route)
		}
	}
}

func (rt *RouteTable) UpdateAURPRouteDistance(peer *AURPPeer, network ddp.Network, distance uint8) {
	rt.mu.Lock()
	defer rt.mu.Unlock()

	for route := range rt.routes {
		if route.AURPPeer == peer && route.NetStart == network {
			route.Distance = distance
			route.LastSeen = time.Now()
		}
	}
}

func (rt *RouteTable) UpsertEtherTalkRoute(peer *EtherTalkPeer, extended bool, netStart, netEnd ddp.Network, metric uint8) (*Route, error) {
	if netStart > netEnd {
		return nil, fmt.Errorf("invalid network range [%d, %d]", netStart, netEnd)
	}
	if netStart != netEnd && !extended {
		return nil, fmt.Errorf("invalid network range [%d, %d] for nonextended network", netStart, netEnd)
	}

	rt.mu.Lock()
	defer rt.mu.Unlock()

	// Update?
	for r := range rt.routes {
		if r.EtherTalkPeer != peer {
			continue
		}
		if r.Extended != extended {
			continue
		}
		if r.NetStart != netStart {
			continue
		}
		if r.NetEnd != netEnd {
			continue
		}
		r.Distance = metric
		r.LastSeen = time.Now()
		return r, nil
	}

	// Insert.
	r := &Route{
		Extended:      extended,
		NetStart:      netStart,
		NetEnd:        netEnd,
		Distance:      metric,
		LastSeen:      time.Now(),
		EtherTalkPeer: peer,
	}
	rt.routes[r] = struct{}{}
	return r, nil
}

func (rt *RouteTable) InsertAURPRoute(peer *AURPPeer, extended bool, netStart, netEnd ddp.Network, metric uint8) error {
	if netStart > netEnd {
		return fmt.Errorf("invalid network range [%d, %d]", netStart, netEnd)
	}
	if netStart != netEnd && !extended {
		return fmt.Errorf("invalid network range [%d, %d] for nonextended network", netStart, netEnd)
	}

	r := &Route{
		Extended: extended,
		NetStart: netStart,
		NetEnd:   netEnd,
		Distance: metric,
		LastSeen: time.Now(),
		AURPPeer: peer,
	}

	rt.mu.Lock()
	defer rt.mu.Unlock()
	rt.routes[r] = struct{}{}
	return nil
}

// ValidRoutes returns all valid routes.
func (rt *RouteTable) ValidRoutes() []*Route {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	valid := make([]*Route, 0, len(rt.routes))
	for r := range rt.routes {
		if r.Valid() {
			valid = append(valid, r)
		}
	}
	return valid
}

// ValidNonAURPRoutes returns all valid routes that were not learned via AURP.
func (rt *RouteTable) ValidNonAURPRoutes() []*Route {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	valid := make([]*Route, 0, len(rt.routes))
	for r := range rt.routes {
		if r.AURPPeer != nil {
			continue
		}
		if !r.Valid() {
			continue
		}
		valid = append(valid, r)
	}
	return valid
}
