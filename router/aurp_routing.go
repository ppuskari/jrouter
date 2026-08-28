/*
   Copyright 2026 Petar Puskarich

   Licensed under the Apache License, Version 2.0 (the "License");
   you may not use this file except in compliance with the License.
*/

package router

import (
	"bytes"
	"cmp"
	"fmt"
	"slices"

	"drjosh.dev/jrouter/aurp"
	"github.com/sfiera/multitalk/pkg/ddp"
)

// Keep AURP routing datagrams below a common Ethernet IPv4 MTU without relying
// on IP fragmentation. This is an implementation budget, not an AURP protocol
// maximum; routing information is streamed across as many ACK-gated packets as
// necessary.
const aurpRoutingDatagramBudget = 1400

type pendingAURPChange struct {
	before Route
	after  Route
}

func aurpRouteIsLocal(r Route) bool {
	return !r.Zero() && r.Target.Class() != TargetClassAURPPeer
}

func aurpRouteIsAdvertisable(r Route) bool {
	return aurpRouteIsLocal(r) && r.Distance < maxRouteDistance
}

func aurpEventTupleFromRoute(code aurp.EventCode, r Route) aurp.EventTuple {
	et := aurp.EventTuple{
		EventCode:  code,
		Extended:   r.Extended,
		RangeStart: r.NetStart,
		RangeEnd:   r.NetEnd,
		Distance:   r.Distance,
	}
	if code == aurp.EventCodeND || code == aurp.EventCodeNRC {
		et.Distance = 0
	}
	return et
}

// aurpEventForBestTransition converts a route-table best-path transition into
// the single AURP event needed to move a peer from the old exported view to the
// new exported view.
func aurpEventForBestTransition(before, after Route) (aurp.EventTuple, bool) {
	beforeAdvertised := aurpRouteIsAdvertisable(before)
	afterAdvertised := aurpRouteIsAdvertisable(after)

	switch {
	case !beforeAdvertised && afterAdvertised:
		// This includes a path changing from the AURP tunnel to the local
		// AppleTalk internet.
		return aurpEventTupleFromRoute(aurp.EventCodeNA, after), true

	case beforeAdvertised && afterAdvertised:
		if before.Distance != after.Distance ||
			before.Extended != after.Extended ||
			before.NetEnd != after.NetEnd {
			return aurpEventTupleFromRoute(aurp.EventCodeNDC, after), true
		}
		return aurp.EventTuple{}, false

	case beforeAdvertised && aurpRouteIsLocal(after) && after.Distance >= maxRouteDistance:
		// Distance 15 is explicitly carried in an NDC so the receiver can
		// remove the route.
		return aurpEventTupleFromRoute(aurp.EventCodeNDC, after), true

	case beforeAdvertised && !after.Zero() && after.Target.Class() == TargetClassAURPPeer:
		return aurpEventTupleFromRoute(aurp.EventCodeNRC, before), true

	case beforeAdvertised:
		return aurpEventTupleFromRoute(aurp.EventCodeND, before), true
	}
	return aurp.EventTuple{}, false
}

func aurpEventsForPendingChanges(changes map[ddp.Network]pendingAURPChange) aurp.EventTuples {
	if len(changes) == 0 {
		return nil
	}
	keys := make([]ddp.Network, 0, len(changes))
	for n := range changes {
		keys = append(keys, n)
	}
	slices.Sort(keys)

	events := make(aurp.EventTuples, 0, len(keys))
	for _, n := range keys {
		change := changes[n]
		if et, ok := aurpEventForBestTransition(change.before, change.after); ok {
			events = append(events, et)
		}
	}
	return events
}

// aurpExportedRoutes returns one deterministic best route for every network
// range currently exportable from the local AppleTalk internet to AURP.
func (rt *RouteTable) aurpExportedRoutes() []Route {
	seen := make(map[RouteKey]struct{})
	var routes []Route

	for _, class := range []TargetClass{TargetClassDirect, TargetClassAppleTalkPeer} {
		for r := range rt.ValidRoutesForClass(class) {
			best := rt.Lookup(r.NetStart)
			if !aurpRouteIsAdvertisable(best) || best.RouteKey != r.RouteKey {
				continue
			}
			if _, ok := seen[best.RouteKey]; ok {
				continue
			}
			seen[best.RouteKey] = struct{}{}
			routes = append(routes, best)
		}
	}

	slices.SortFunc(routes, func(a, b Route) int {
		return cmp.Or(
			cmp.Compare(a.NetStart, b.NetStart),
			cmp.Compare(a.NetEnd, b.NetEnd),
			cmp.Compare(a.Distance, b.Distance),
			cmp.Compare(a.TargetKey, b.TargetKey),
		)
	})
	return routes
}

func aurpNetworkTupleSize(nt aurp.NetworkTuple) int {
	if nt.Extended {
		return 6
	}
	return 3
}

func aurpEventTupleSize(et aurp.EventTuple) int {
	if et.EventCode == aurp.EventCodeNull {
		return 1
	}
	if et.Extended {
		return 6
	}
	return 4
}

func aurpRoutingPayloadBudget(pkt aurp.Packet) (int, error) {
	var b bytes.Buffer
	if _, err := pkt.WriteTo(&b); err != nil {
		return 0, err
	}
	budget := aurpRoutingDatagramBudget - b.Len()
	if budget <= 0 {
		return 0, fmt.Errorf(
			"AURP routing header size %d exceeds datagram budget %d",
			b.Len(), aurpRoutingDatagramBudget,
		)
	}
	return budget, nil
}

func chunkAURPNetworkTuples(tuples aurp.NetworkTuples, maxBytes int) ([]aurp.NetworkTuples, error) {
	if maxBytes <= 0 {
		return nil, fmt.Errorf("invalid network tuple payload budget %d", maxBytes)
	}
	if len(tuples) == 0 {
		return []aurp.NetworkTuples{nil}, nil
	}

	var chunks []aurp.NetworkTuples
	var chunk aurp.NetworkTuples
	size := 0
	for _, nt := range tuples {
		n := aurpNetworkTupleSize(nt)
		if n > maxBytes {
			return nil, fmt.Errorf("network tuple size %d exceeds payload budget %d", n, maxBytes)
		}
		if len(chunk) > 0 && size+n > maxBytes {
			chunks = append(chunks, chunk)
			chunk = nil
			size = 0
		}
		chunk = append(chunk, nt)
		size += n
	}
	if len(chunk) > 0 {
		chunks = append(chunks, chunk)
	}
	return chunks, nil
}

func chunkAURPEventTuples(events aurp.EventTuples, maxBytes int) ([]aurp.EventTuples, error) {
	if maxBytes <= 0 {
		return nil, fmt.Errorf("invalid event tuple payload budget %d", maxBytes)
	}

	var chunks []aurp.EventTuples
	var chunk aurp.EventTuples
	size := 0
	for _, et := range events {
		n := aurpEventTupleSize(et)
		if n > maxBytes {
			return nil, fmt.Errorf("event tuple size %d exceeds payload budget %d", n, maxBytes)
		}
		if len(chunk) > 0 && size+n > maxBytes {
			chunks = append(chunks, chunk)
			chunk = nil
			size = 0
		}
		chunk = append(chunk, et)
		size += n
	}
	if len(chunk) > 0 {
		chunks = append(chunks, chunk)
	}
	return chunks, nil
}
