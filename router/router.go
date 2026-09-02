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
	"context"
	"fmt"
	"log/slog"
	"sync"

	"drjosh.dev/jrouter/aurp"

	"github.com/sfiera/multitalk/pkg/ddp"
)

// Router implements the core routing logic.
type Router struct {
	// Utility
	Logger *slog.Logger

	// (Mostly) static configuration
	Config   *Config
	Identity aurp.IPDomainIdentifier // resolved from configuration or default

	// Dynamic information
	RouteTable *RouteTable
	Ports      []*EtherTalkPort
	AURPPeers  *AURPPeerTable

	loopProbeMu    sync.Mutex
	loopProbes     map[string]*loopProbeInvestigation
	loopProbeByKey map[string]string
}

func ddpHopCount(ddpkt *ddp.ExtPacket) uint16 {
	return (ddpkt.Size & 0x3C00) >> 10
}

func setDDPHopCount(ddpkt *ddp.ExtPacket, hopCount uint16) {
	ddpkt.Size &^= 0x3C00
	ddpkt.Size |= (hopCount & 0x0f) << 10
}

func ddpPacketBytes(ddpkt *ddp.ExtPacket) uint64 {
	if ddpkt == nil {
		return 0
	}
	return uint64(13 + len(ddpkt.Data))
}

func (rtr *Router) noteRouteTraffic(
	ddpkt *ddp.ExtPacket,
	destination Route,
	ingress RouteTarget,
) {
	if rtr.RouteTable == nil || ddpkt == nil || destination.Zero() {
		return
	}
	bytes := ddpPacketBytes(ddpkt)
	if bytes == 0 {
		return
	}

	source := Route{}
	if ingress != nil {
		source = rtr.RouteTable.lookupForTarget(ddpkt.SrcNet, ingress)
	}
	if source.Zero() {
		source = rtr.RouteTable.Lookup(ddpkt.SrcNet)
	}
	if !source.Zero() {
		source.noteDDPBytesIn(bytes)
	}
	destination.noteDDPBytesOut(bytes)
}

func reduceAURPHopCount(ddpkt *ddp.ExtPacket, route Route) (bool, error) {
	hopCount := ddpHopCount(ddpkt)
	if hopCount >= maxRouteDistance {
		return false, fmt.Errorf(
			"hop count exceeded limit (%d >= %d)",
			hopCount,
			maxRouteDistance,
		)
	}
	remaining := uint16(route.Distance)
	if remaining >= maxRouteDistance {
		return false, fmt.Errorf(
			"remaining route distance too high for hop-count reduction (%d)",
			remaining,
		)
	}
	if hopCount+remaining <= maxRouteDistance {
		return false, nil
	}
	newHopCount := uint16(maxRouteDistance) - remaining
	if newHopCount >= hopCount {
		return false, nil
	}
	setDDPHopCount(ddpkt, newHopCount)
	return true, nil
}

// Forward increments the hop count, then outputs the packet in the direction
// of the destination.
func (rtr *Router) Forward(ctx context.Context, ddpkt *ddp.ExtPacket) error {
	hopCount := ddpHopCount(ddpkt)
	if hopCount >= maxRouteDistance {
		return fmt.Errorf("hop count exceeded limit (%d >= %d)", hopCount, maxRouteDistance)
	}
	setDDPHopCount(ddpkt, hopCount+1)
	return rtr.Output(ctx, ddpkt)
}

func (rtr *Router) outputRoute(
	ddpkt *ddp.ExtPacket,
	ingressAURPID string,
) (Route, error) {
	if ingressAURPID == "" {
		route := rtr.RouteTable.Lookup(ddpkt.DstNet)
		if route.Zero() {
			return Route{}, fmt.Errorf(
				"no route for packet (dstnet %d); dropping packet",
				ddpkt.DstNet,
			)
		}
		return route, nil
	}

	route := rtr.RouteTable.LookupAvoidingAURPTunnel(
		ddpkt.DstNet,
		ingressAURPID,
	)
	if !route.Zero() {
		return route, nil
	}

	best := rtr.RouteTable.Lookup(ddpkt.DstNet)
	if best.Zero() {
		return Route{}, fmt.Errorf(
			"no route for packet (dstnet %d); dropping packet",
			ddpkt.DstNet,
		)
	}
	return Route{}, fmt.Errorf(
		"AURP reflection to ingress tunnel %q for dstnet %d; no alternative path",
		ingressAURPID,
		ddpkt.DstNet,
	)
}

// Output outputs the packet in the direction of the destination.
// (It does not check or adjust the hop count.)
func (rtr *Router) Output(ctx context.Context, ddpkt *ddp.ExtPacket) error {
	route, err := rtr.outputRoute(ddpkt, "")
	if err != nil {
		return err
	}
	if err := route.Target.Forward(ctx, ddpkt); err != nil {
		return err
	}
	rtr.noteRouteTraffic(ddpkt, route, nil)
	return nil
}

// OutputFromAURP outputs an encapsulated AppleTalk packet while preserving
// ingress tunnel provenance. A packet is never reflected back to the same
// logical AURP peer that delivered it, but may still be routed to a different
// AURP peer when the routing table explicitly selects that peer.
func (rtr *Router) OutputFromAURP(
	ctx context.Context,
	ingress *AURPPeer,
	ddpkt *ddp.ExtPacket,
) error {
	if (rtr.Config != nil && rtr.Config.AURP.networkHidden(ddpkt.DstNet)) ||
		(ingress != nil && ingress.exportNetworkHidden(ddpkt.DstNet)) {
		return fmt.Errorf(
			"AURP network %d is hidden from ingress peer; dropping packet",
			ddpkt.DstNet,
		)
	}
	best := rtr.RouteTable.Lookup(ddpkt.DstNet)
	route, err := rtr.outputRoute(ddpkt, ingress.TunnelID())
	if err != nil {
		if !best.Zero() {
			origin := best.RouteOrigin()
			if origin.Kind == RouteOriginAURP &&
				origin.ID == ingress.TunnelID() {
				ingress.reflectionDrops.Add(1)
			}
		}
		return err
	}
	if !best.Zero() {
		origin := best.RouteOrigin()
		if origin.Kind == RouteOriginAURP &&
			origin.ID == ingress.TunnelID() &&
			route.Target.RouteTargetKey() != best.Target.RouteTargetKey() {
			ingress.alternativePathForwards.Add(1)
		}
	}
	if ddpHopCount(ddpkt) >= maxRouteDistance &&
		route.Target.Class() != TargetClassDirect {
		return fmt.Errorf(
			"tunneled packet at hop-count limit %d cannot be forwarded to another router",
			ddpHopCount(ddpkt),
		)
	}
	if rtr.Config != nil &&
		rtr.Config.AURP.HopCountReduction &&
		route.Target.Class() != TargetClassAURPPeer {
		changed, err := reduceAURPHopCount(ddpkt, route)
		if err != nil {
			return err
		}
		if changed {
			ingress.hopCountReductions.Add(1)
		}
	}
	if err := route.Target.Forward(ctx, ddpkt); err != nil {
		return err
	}
	rtr.noteRouteTraffic(ddpkt, route, ingress)
	return nil
}
