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
	"github.com/sfiera/multitalk/pkg/ethernet"
)

func (rtr *Router) HandleNBP(srcHWAddr ethernet.Addr, ddpkt *ddp.ExtPacket) error {
	if ddpkt.Proto != ddp.ProtoNBP {
		return fmt.Errorf("invalid DDP type %d on socket 2", ddpkt.Proto)
	}

	nbpkt, err := nbp.Unmarshal(ddpkt.Data)
	if err != nil {
		return fmt.Errorf("invalid packet: %w", err)
	}

	log.Printf("NBP: Got %v id %d with tuples %v", nbpkt.Function, nbpkt.NBPID, nbpkt.Tuples)

	switch nbpkt.Function {
	case nbp.FunctionLkUp:
		// when in AppleTalk, do as Apple Internet Router does...
		outDDP, err := rtr.helloWorldThisIsMe(ddpkt, nbpkt.NBPID, &nbpkt.Tuples[0])
		if err != nil || outDDP == nil {
			return err
		}
		log.Print("NBP: Replying to LkUp with LkUp-Reply for myself")
		return rtr.sendEtherTalkDDP(srcHWAddr, outDDP)

	case nbp.FunctionBrRq:
		// There must be 1!
		tuple := &nbpkt.Tuples[0]

		zones := rtr.ZoneTable.LookupName(tuple.Zone)

		for _, z := range zones {
			if z.Local {
				// If it's for the local zone, translate it to a LkUp and broadcast it back
				// out the EtherTalk port.
				// "Note: On an internet, nodes on extended networks performing lookups in
				// their own zone must replace a zone name of asterisk (*) with their actual
				// zone name before sending the packet to A-ROUTER. All nodes performing
				// lookups in their own zone will receive LkUp packets from themselves
				// (actually sent by a router). The node's NBP process should expect to
				// receive these packets and must reply to them."
				nbpkt.Function = nbp.FunctionLkUp
				nbpRaw, err := nbpkt.Marshal()
				if err != nil {
					return fmt.Errorf("couldn't marshal LkUp: %v", err)
				}

				outDDP := *ddpkt
				outDDP.Size = uint16(len(nbpRaw)) + atalk.DDPExtHeaderSize
				outDDP.DstNet = 0x0000 // Local network broadcast
				outDDP.DstNode = 0xFF  // Broadcast node address within the dest network
				outDDP.Data = nbpRaw

				log.Printf("NBP: zone multicasting LkUp for tuple %v", tuple)
				if err := rtr.ZoneMulticastEtherTalkDDP(tuple.Zone, &outDDP); err != nil {
					return err
				}

				// But also...if we match the query, reply as though it was a LkUp
				outDDP2, err := rtr.helloWorldThisIsMe(ddpkt, nbpkt.NBPID, tuple)
				if err != nil {
					return err
				}
				if outDDP2 == nil {
					continue
				}
				log.Print("NBP: Replying to BrRq with LkUp-Reply for myself")
				if err := rtr.sendEtherTalkDDP(srcHWAddr, outDDP2); err != nil {
					return err
				}

				continue
			}

			route := rtr.RouteTable.LookupRoute(z.Network)
			if route == nil {
				return fmt.Errorf("no route for network %d", z.Network)
			}
			peer := route.Peer
			if peer == nil {
				return fmt.Errorf("nil peer for route for network %d", z.Network)
			}

			// Translate it into a FwdReq and route it to the
			// routers with the appropriate zone(s).
			nbpkt.Function = nbp.FunctionFwdReq
			nbpRaw, err := nbpkt.Marshal()
			if err != nil {
				return fmt.Errorf("couldn't marshal FwdReq: %v", err)
			}

			outDDP := *ddpkt
			outDDP.Size = uint16(len(nbpRaw)) + atalk.DDPExtHeaderSize
			outDDP.DstNet = z.Network
			outDDP.DstNode = 0x00 // Router node address for the dest network
			outDDP.Data = nbpRaw

			outDDPRaw, err := ddp.ExtMarshal(outDDP)
			if err != nil {
				return err
			}

			log.Printf("NBP: Sending FwdReq to %v for tuple %v", peer.RemoteAddr, tuple)

			if _, err := peer.Send(peer.Transport.NewAppleTalkPacket(outDDPRaw)); err != nil {
				return fmt.Errorf("sending FwdReq on to peer: %w", err)
			}
		}

	default:
		return fmt.Errorf("TODO: handle function %v", nbpkt.Function)
	}

	return nil
}

func (rtr *Router) helloWorldThisIsMe(ddpkt *ddp.ExtPacket, nbpID uint8, tuple *nbp.Tuple) (*ddp.ExtPacket, error) {
	if tuple.Object != "jrouter" && tuple.Object != "=" {
		return nil, nil
	}
	if tuple.Type != "AppleRouter" && tuple.Type != "=" {
		return nil, nil
	}
	if tuple.Zone != rtr.Config.EtherTalk.ZoneName && tuple.Zone != "*" && tuple.Zone != "" {
		return nil, nil
	}
	respPkt := &nbp.Packet{
		Function: nbp.FunctionLkUpReply,
		NBPID:    nbpID,
		Tuples: []nbp.Tuple{
			{
				Network:    rtr.MyDDPAddr.Network,
				Node:       rtr.MyDDPAddr.Node,
				Socket:     253,
				Enumerator: 0,
				Object:     "jrouter",
				Type:       "AppleRouter",
				Zone:       rtr.Config.EtherTalk.ZoneName,
			},
		},
	}
	respRaw, err := respPkt.Marshal()
	if err != nil {
		return nil, fmt.Errorf("couldn't marshal LkUp-Reply: %v", err)
	}
	return &ddp.ExtPacket{
		ExtHeader: ddp.ExtHeader{
			Size:      uint16(len(respRaw)) + atalk.DDPExtHeaderSize,
			Cksum:     0,
			DstNet:    ddpkt.SrcNet,
			DstNode:   ddpkt.SrcNode,
			DstSocket: ddpkt.SrcSocket,
			SrcNet:    rtr.MyDDPAddr.Network,
			SrcNode:   rtr.MyDDPAddr.Node,
			SrcSocket: 2,
			Proto:     ddp.ProtoNBP,
		},
		Data: respRaw,
	}, nil
}
