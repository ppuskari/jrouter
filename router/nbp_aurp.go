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
	"log"

	"gitea.drjosh.dev/josh/jrouter/atalk"
	"gitea.drjosh.dev/josh/jrouter/atalk/nbp"
	"github.com/sfiera/multitalk/pkg/ddp"
	"github.com/sfiera/multitalk/pkg/ethertalk"
)

func (rtr *Router) HandleNBPInAURP(peer *Peer, ddpkt *ddp.ExtPacket) error {
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

	if len(nbpkt.Tuples) < 1 {
		return fmt.Errorf("no tuples in NBP packet")
	}
	tuple := &nbpkt.Tuples[0]

	if tuple.Zone != rtr.Config.EtherTalk.ZoneName {
		return fmt.Errorf("FwdReq querying zone %q, which is not our zone", tuple.Zone)
	}

	// TODO: Route the FwdReq to another router if it's not our zone

	log.Printf("NBP/DDP/AURP: Converting FwdReq to LkUp (%v)", tuple)

	// Convert it to a LkUp and broadcast on EtherTalk
	nbpkt.Function = nbp.FunctionLkUp
	nbpRaw, err := nbpkt.Marshal()
	if err != nil {
		return fmt.Errorf("couldn't marshal LkUp: %v", err)
	}

	// "If the destination network is extended, however, the router must also
	// change the destination network number to $0000, so that the packet is
	// received by all nodes on the network (within the correct zone multicast
	// address)."
	ddpkt.DstNet = 0x0000
	ddpkt.DstNode = 0xFF // Broadcast node address within the dest network
	ddpkt.Data = nbpRaw

	dstEth := ethertalk.AppleTalkBroadcast
	if tuple.Zone != "*" && tuple.Zone != "" {
		dstEth = atalk.MulticastAddr(tuple.Zone)
	}
	if err := rtr.sendEtherTalkDDP(dstEth, ddpkt); err != nil {
		return err
	}

	// But also... if it matches us, reply directly with a LkUp-Reply of our own
	outDDP, err := rtr.helloWorldThisIsMe(ddpkt, nbpkt.NBPID, tuple)
	if err != nil || outDDP == nil {
		return err
	}
	log.Print("NBP/DDP/AURP: Replying to BrRq with LkUp-Reply for myself")
	outDDPRaw, err := ddp.ExtMarshal(*outDDP)
	if err != nil {
		return err
	}
	_, err = peer.Send(peer.Transport.NewAppleTalkPacket(outDDPRaw))
	return err
}
