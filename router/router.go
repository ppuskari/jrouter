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

	"github.com/sfiera/multitalk/pkg/ddp"
)

type Router struct {
	Config     *Config
	RouteTable *RouteTable
	Ports      []*EtherTalkPort
}

// Forward increments the hop count, then outputs the packet in the direction
// of the destination.
func (rtr *Router) Forward(ctx context.Context, ddpkt *ddp.ExtPacket) error {
	// Check and adjust the Hop Count
	// Note the ddp package doesn't make this simple
	hopCount := (ddpkt.Size & 0x3C00) >> 10
	if hopCount >= 15 {
		return fmt.Errorf("hop count exceeded (%d >= 15)", hopCount)
	}
	hopCount++
	ddpkt.Size &^= 0x3C00
	ddpkt.Size |= hopCount << 10

	return rtr.Output(ctx, ddpkt)
}

// Output outputs the packet in the direction of the destination.
// (It does not check or adjust the hop count.)
func (rtr *Router) Output(ctx context.Context, ddpkt *ddp.ExtPacket) error {
	switch route := rtr.RouteTable.LookupRoute(ddpkt.DstNet); {
	case route == nil:
		return fmt.Errorf("no route for packet (dstnet %d); dropping packet", ddpkt.DstNet)

	case route.AURPPeer != nil:
		// log.Printf("Forwarding packet to AURP peer %v", route.AURPPeer.RemoteAddr)
		return route.AURPPeer.Forward(ddpkt)

	case route.EtherTalkPeer != nil:
		// log.Printf("Forwarding to EtherTalk peer %v", route.EtherTalkPeer.PeerAddr)
		// Note: resolving AARP can block
		return route.EtherTalkPeer.Forward(ctx, ddpkt)

	case route.EtherTalkDirect != nil:
		// log.Printf("Outputting to EtherTalk directly")
		// Note: resolving AARP can block
		return route.EtherTalkDirect.Send(ctx, ddpkt)

	default:
		return fmt.Errorf("no forwarding mechanism for route! %+v", route)
	}
}
