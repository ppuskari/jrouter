package main

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
	Peer     *peer
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

func (rt *RoutingTable) UpsertRoute(extended bool, netStart, netEnd ddp.Network, peer *peer, metric uint8) error {
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
