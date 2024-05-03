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
	"log"

	"gitea.drjosh.dev/josh/jrouter/atalk"
	"gitea.drjosh.dev/josh/jrouter/atalk/nbp"
	"github.com/sfiera/multitalk/pkg/ddp"
)

func (port *EtherTalkPort) HandleNBP(ctx context.Context, ddpkt *ddp.ExtPacket) error {
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
		outDDP, err := port.helloWorldThisIsMe(ddpkt, nbpkt.NBPID, &nbpkt.Tuples[0])
		if err != nil || outDDP == nil {
			return err
		}
		log.Print("NBP: Replying to LkUp with LkUp-Reply for myself")
		// Note: AARP can block
		return port.Send(ctx, outDDP)

	case nbp.FunctionFwdReq:
		// TODO: handle FwdReq input

	case nbp.FunctionBrRq:
		return port.handleNBPBrRq(ctx, ddpkt, nbpkt)

	default:
		return fmt.Errorf("TODO: handle function %v", nbpkt.Function)
	}
	return nil
}

func (port *EtherTalkPort) handleNBPBrRq(ctx context.Context, ddpkt *ddp.ExtPacket, nbpkt *nbp.Packet) error {
	// There must be 1!
	tuple := &nbpkt.Tuples[0]

	if tuple.Zone == "" || tuple.Zone == "*" {
		tuple.Zone = port.DefaultZoneName
	}

	zones := port.Router.ZoneTable.LookupName(tuple.Zone)

	for _, z := range zones {
		if outPort := z.LocalPort; outPort != nil {
			// If it's for a local zone, translate it to a LkUp and broadcast
			// out the corresponding EtherTalk port.
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

			outDDP := ddp.ExtPacket{
				ExtHeader: ddp.ExtHeader{
					Size:      atalk.DDPExtHeaderSize + uint16(len(nbpRaw)),
					Cksum:     0,
					SrcNet:    ddpkt.SrcNet,
					SrcNode:   ddpkt.SrcNode,
					SrcSocket: ddpkt.SrcSocket,
					DstNet:    0x0000, // Local network broadcast
					DstNode:   0xFF,   // Broadcast node address within the dest network
					DstSocket: 2,
					Proto:     ddp.ProtoNBP,
				},
				Data: nbpRaw,
			}

			log.Printf("NBP: zone multicasting LkUp for tuple %v", tuple)
			if err := outPort.ZoneMulticast(tuple.Zone, &outDDP); err != nil {
				return err
			}

			// But also...if we match the query, reply as though it was a LkUp
			// This uses the *input* port information.
			outDDP2, err := port.helloWorldThisIsMe(ddpkt, nbpkt.NBPID, tuple)
			if err != nil {
				return err
			}
			if outDDP2 == nil {
				continue
			}
			log.Print("NBP: Replying to BrRq directly with LkUp-Reply for myself")
			// Can reply to BrRq on the same port we got it, because it wasn't
			// routed
			if err := port.Send(ctx, outDDP2); err != nil {
				return err
			}

			continue
		}

		// The zone table row is *not* for a local network.
		// Translate it into a FwdReq and route that to the routers that do have
		// that zone as a local network.
		nbpkt.Function = nbp.FunctionFwdReq
		nbpRaw, err := nbpkt.Marshal()
		if err != nil {
			return fmt.Errorf("couldn't marshal FwdReq: %v", err)
		}

		outDDP := &ddp.ExtPacket{
			ExtHeader: ddp.ExtHeader{
				Size:      atalk.DDPExtHeaderSize + uint16(len(nbpRaw)),
				Cksum:     0,
				SrcNet:    ddpkt.SrcNet,
				SrcNode:   ddpkt.SrcNode,
				SrcSocket: ddpkt.SrcSocket,
				DstNet:    z.Network,
				DstNode:   0x00, // Any router for the dest network
				DstSocket: 2,
				Proto:     ddp.ProtoNBP,
			},
			Data: nbpRaw,
		}

		if err := port.Router.Forward(ctx, outDDP); err != nil {
			return err
		}
	}
	return nil
}

// Returns an NBP LkUp-Reply for the router itself, with the address from this port.
func (port *EtherTalkPort) helloWorldThisIsMe(ddpkt *ddp.ExtPacket, nbpID uint8, tuple *nbp.Tuple) (*ddp.ExtPacket, error) {
	if tuple.Object != "jrouter" && tuple.Object != "=" {
		return nil, nil
	}
	if tuple.Type != "AppleRouter" && tuple.Type != "=" {
		return nil, nil
	}
	if tuple.Zone != port.DefaultZoneName && tuple.Zone != "*" && tuple.Zone != "" {
		return nil, nil
	}
	respPkt := &nbp.Packet{
		Function: nbp.FunctionLkUpReply,
		NBPID:    nbpID,
		Tuples: []nbp.Tuple{
			{
				Network:    port.MyAddr.Network,
				Node:       port.MyAddr.Node,
				Socket:     253,
				Enumerator: 0,
				Object:     "jrouter",
				Type:       "AppleRouter",
				Zone:       port.DefaultZoneName,
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
			SrcNet:    port.MyAddr.Network,
			SrcNode:   port.MyAddr.Node,
			SrcSocket: 2,
			Proto:     ddp.ProtoNBP,
		},
		Data: respRaw,
	}, nil
}
