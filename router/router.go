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

	"github.com/sfiera/multitalk/pkg/ddp"
)

// Router implements the core routing logic.
type Router struct {
	Logger     *slog.Logger
	Config     *Config
	RouteTable *RouteTable
	Ports      []*EtherTalkPort
	//AURPPeers  *AURPPeersTable
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
	route := rtr.RouteTable.Lookup(ddpkt.DstNet)
	if route.Zero() {
		return fmt.Errorf("no route for packet (dstnet %d); dropping packet", ddpkt.DstNet)
	}

	return route.Target.Forward(ctx, ddpkt)
}
