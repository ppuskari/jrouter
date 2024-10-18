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

	"drjosh.dev/jrouter/atalk/nbp"
	"github.com/sfiera/multitalk/pkg/ddp"
)

func (rtr *Router) HandleNBPFromAURP(ctx context.Context, ddpkt *ddp.ExtPacket) error {
	if ddpkt.Proto != ddp.ProtoNBP {
		return fmt.Errorf("invalid DDP type %d on socket 2", ddpkt.Proto)
	}
	nbpkt, err := nbp.Unmarshal(ddpkt.Data)
	if err != nil {
		return fmt.Errorf("invalid NBP packet: %v", err)
	}
	if nbpkt.Function != nbp.FunctionFwdReq {
		// It's something else??
		return fmt.Errorf("can't handle %v", nbpkt.Function)
	}
	return rtr.handleNBPFwdReq(ctx, ddpkt, nbpkt)
}
