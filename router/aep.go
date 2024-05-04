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

	"gitea.drjosh.dev/josh/jrouter/atalk/aep"
	"github.com/sfiera/multitalk/pkg/ddp"
)

func (rtr *Router) HandleAEP(ctx context.Context, ddpkt *ddp.ExtPacket) error {
	if ddpkt.Proto != ddp.ProtoAEP {
		return fmt.Errorf("invalid DDP type %d on socket 4", ddpkt.Proto)
	}
	ep, err := aep.Unmarshal(ddpkt.Data)
	if err != nil {
		return err
	}
	switch ep.Function {
	case aep.EchoReply:
		// we didn't send a request? I don't think?
		// we shouldn't be sending them from this socket
		return fmt.Errorf("echo reply received at socket 4 why?")

	case aep.EchoRequest:
		// Uno Reverso the packet
		// "The client can send the Echo Request datagram through any socket
		// the client has open, and the Echo Reply will come back to this socket."
		ddpkt.DstNet, ddpkt.SrcNet = ddpkt.SrcNet, ddpkt.DstNet
		ddpkt.DstNode, ddpkt.SrcNode = ddpkt.SrcNode, ddpkt.DstNode
		ddpkt.DstSocket, ddpkt.SrcSocket = ddpkt.SrcSocket, ddpkt.DstSocket
		ddpkt.Data[0] = byte(aep.EchoReply)

		return rtr.Output(ctx, ddpkt)

	default:
		return fmt.Errorf("invalid AEP function %d", ep.Function)
	}
}
