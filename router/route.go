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

// const maxRouteAge = 10 * time.Minute // TODO: confirm

type Route struct {
	Extended bool
	NetStart ddp.Network
	NetEnd   ddp.Network
	Peer     *Peer
	Distance uint8
	LastSeen time.Time
}

type RoutingTable struct {
	mu     sync.Mutex
	routes map[*Route]struct{}
}

func NewRoutingTable() *RoutingTable {
	return &RoutingTable{
		routes: make(map[*Route]struct{}),
	}
}

func (rt *RoutingTable) LookupRoute(network ddp.Network) *Route {
	rt.mu.Lock()
	defer rt.mu.Unlock()

	var bestRoute *Route
	for r := range rt.routes {
		if r.Peer == nil {
			continue
		}
		if network < r.NetStart || network > r.NetEnd {
			continue
		}
		// if time.Since(r.LastSeen) > maxRouteAge {
		// 	continue
		// }
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

func (rt *RoutingTable) UpsertRoute(extended bool, netStart, netEnd ddp.Network, peer *Peer, metric uint8) error {
	if netStart > netEnd {
		return fmt.Errorf("invalid network range [%d, %d]", netStart, netEnd)
	}

	// TODO: handle the Update part of "Upsert"

	r := &Route{
		Extended: extended,
		NetStart: netStart,
		NetEnd:   netEnd,
		Peer:     peer,
		Distance: metric,
		LastSeen: time.Now(),
	}

	rt.mu.Lock()
	defer rt.mu.Unlock()
	rt.routes[r] = struct{}{}
	return nil
}

func (rt *RoutingTable) ValidRoutes() []*Route {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	valid := make([]*Route, 0, len(rt.routes))
	for r := range rt.routes {
		if r.Peer == nil {
			continue
		}
		// if time.Since(r.LastSeen) > maxRouteAge {
		// 	continue
		// }
		valid = append(valid, r)
	}
	return valid
}
