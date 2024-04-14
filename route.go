package main

import (
	"cmp"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/sfiera/multitalk/pkg/ddp"
)

const maxRouteAge = 10 * time.Minute // TODO: confirm

type route struct {
	extended bool
	netStart ddp.Network
	netEnd   ddp.Network
	peer     *peer
	metric   uint8
	last     time.Time
}

type routingTable struct {
	tableMu sync.Mutex
	table   map[ddp.Network][]*route

	allRoutesMu sync.Mutex
	allRoutes   map[*route]struct{}
}

func (rt *routingTable) lookupRoute(network ddp.Network) *route {
	rt.tableMu.Lock()
	defer rt.tableMu.Unlock()

	for _, rs := range rt.table[network] {
		if time.Since(rs.last) > maxRouteAge {
			continue
		}
		return rs
	}
	return nil
}

func (rt *routingTable) upsertRoutes(extended bool, netStart, netEnd ddp.Network, peer *peer, metric uint8) error {
	if netStart > netEnd {
		return fmt.Errorf("invalid network range [%d, %d]", netStart, netEnd)
	}

	r := &route{
		extended: extended,
		netStart: netStart,
		netEnd:   netEnd,
		peer:     peer,
		metric:   metric,
		last:     time.Now(),
	}

	rt.allRoutesMu.Lock()
	rt.allRoutes[r] = struct{}{}
	rt.allRoutesMu.Unlock()

	rt.tableMu.Lock()
	defer rt.tableMu.Unlock()
	for n := netStart; n <= netEnd; n++ {
		rt.table[n] = append(rt.table[n], r)
		slices.SortFunc(rt.table[n], func(r, s *route) int {
			return cmp.Compare(r.metric, s.metric)
		})
	}
	return nil
}

func (rt *routingTable) validRoutes() []*route {
	rt.allRoutesMu.Lock()
	defer rt.allRoutesMu.Unlock()
	valid := make([]*route, 0, len(rt.allRoutes))
	for r := range rt.allRoutes {
		if r.peer == nil {
			continue
		}
		if time.Since(r.last) > maxRouteAge {
			continue
		}
		valid = append(valid, r)
	}
	return valid
}
