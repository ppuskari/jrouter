package main

import (
	"cmp"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/sfiera/multitalk/pkg/ddp"
)

type route struct {
	peer   *peer
	metric uint8
	last   time.Time
}

var (
	routingTableMu sync.Mutex
	routingTable   = make(map[ddp.Network][]*route)
)

func lookupRoute(network ddp.Network) *route {
	routingTableMu.Lock()
	defer routingTableMu.Unlock()

	rs := routingTable[network]
	if len(rs) == 0 {
		return nil
	}
	return rs[0]
}

func upsertRoutes(netStart, netEnd ddp.Network, peer *peer, metric uint8) error {
	if netStart > netEnd {
		return fmt.Errorf("invalid network range [%d, %d]", netStart, netEnd)
	}
	r := &route{
		peer:   peer,
		metric: metric,
		last:   time.Now(),
	}

	routingTableMu.Lock()
	defer routingTableMu.Unlock()
	for n := netStart; n <= netEnd; n++ {
		routingTable[n] = append(routingTable[n], r)
		slices.SortFunc(routingTable[n], func(r, s *route) int {
			return cmp.Compare(r.metric, s.metric)
		})
	}
	return nil
}
