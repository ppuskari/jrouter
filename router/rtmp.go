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
	"time"

	"gitea.drjosh.dev/josh/jrouter/atalk"
	"gitea.drjosh.dev/josh/jrouter/atalk/rtmp"
	"gitea.drjosh.dev/josh/jrouter/status"

	"github.com/sfiera/multitalk/pkg/ddp"
)

// RTMPMachine implements RTMP on an AppleTalk network attached to the router.
func (port *EtherTalkPort) HandleRTMP(ctx context.Context, pkt *ddp.ExtPacket) error {
	switch pkt.Proto {
	case ddp.ProtoRTMPReq:
		// I can answer RTMP requests!
		req, err := rtmp.UnmarshalRequestPacket(pkt.Data)
		if err != nil {
			return fmt.Errorf("unmarshal Request packet: %w", err)
		}

		switch req.Function {
		case rtmp.FunctionRequest:
			// Respond with RTMP Response
			respPkt := &rtmp.ResponsePacket{
				SenderAddr: port.MyAddr,
				Extended:   true,
				RangeStart: port.NetStart,
				RangeEnd:   port.NetEnd,
			}
			respPktRaw, err := respPkt.Marshal()
			if err != nil {
				return fmt.Errorf("marshal RTMP Response packet: %w", err)
			}
			ddpPkt := &ddp.ExtPacket{
				ExtHeader: ddp.ExtHeader{
					Size:      uint16(len(respPktRaw)) + atalk.DDPExtHeaderSize,
					Cksum:     0,
					DstNet:    pkt.SrcNet,
					DstNode:   pkt.SrcNode,
					DstSocket: 1, // the RTMP socket
					SrcNet:    port.MyAddr.Network,
					SrcNode:   port.MyAddr.Node,
					SrcSocket: 1, // the RTMP socket
					Proto:     ddp.ProtoRTMPResp,
				},
				Data: respPktRaw,
			}

			if err := port.Router.Output(ctx, ddpPkt); err != nil {
				return fmt.Errorf("send Response: %w", err)
			}

		case rtmp.FunctionRDRSplitHorizon, rtmp.FunctionRDRComplete:
			// Like the Data broadcast, but solicited by a request (RDR).
			splitHorizon := req.Function == rtmp.FunctionRDRSplitHorizon
			for _, dataPkt := range port.rtmpDataPackets(splitHorizon) {
				dataPktRaw, err := dataPkt.Marshal()
				if err != nil {
					return fmt.Errorf("marshal RTMP Data packet: %w", err)
				}

				ddpPkt := &ddp.ExtPacket{
					ExtHeader: ddp.ExtHeader{
						Size:      uint16(len(dataPktRaw)) + atalk.DDPExtHeaderSize,
						Cksum:     0,
						DstNet:    pkt.SrcNet,
						DstNode:   pkt.SrcNode,
						DstSocket: 1, // the RTMP socket
						SrcNet:    port.MyAddr.Network,
						SrcNode:   port.MyAddr.Node,
						SrcSocket: 1, // the RTMP socket
						Proto:     ddp.ProtoRTMPResp,
					},
					Data: dataPktRaw,
				}

				if err := port.Router.Output(ctx, ddpPkt); err != nil {
					return fmt.Errorf("send Data: %w", err)
				}
			}

		case rtmp.FunctionLoopProbe:
			log.Print("RTMP: TODO: handle Loop Probes")
			return nil
		}

	case ddp.ProtoRTMPResp:
		// It's a peer router on the AppleTalk network!
		log.Print("RTMP: Got Response or Data")
		dataPkt, err := rtmp.UnmarshalDataPacket(pkt.Data)
		if err != nil {
			log.Printf("RTMP: Couldn't unmarshal RTMP Data packet: %v", err)
			break
		}
		peer := &EtherTalkPeer{
			Port:     port,
			PeerAddr: dataPkt.RouterAddr,
		}

		for _, rt := range dataPkt.NetworkTuples {
			if err := port.Router.RouteTable.UpsertEtherTalkRoute(peer, rt.Extended, rt.RangeStart, rt.RangeEnd, rt.Distance+1); err != nil {
				log.Printf("RTMP: Couldn't upsert EtherTalk route: %v", err)
			}
		}

	default:
		log.Printf("RTMP: invalid DDP type %d on socket 1", pkt.Proto)
	}

	return nil
}

// RunRTMP makes periodic RTMP Data broadcasts on this port.
func (port *EtherTalkPort) RunRTMP(ctx context.Context) (err error) {
	ctx, setStatus, _ := status.AddSimpleItem(ctx, "RTMP")
	defer func() {
		setStatus(fmt.Sprintf("Run loop stopped! Return: %v", err))
	}()

	setStatus("Awaiting DDP address assignment")

	// Await local address assignment before doing anything
	<-port.AARPMachine.Assigned()

	setStatus("Initial RTMP Data broadcast")

	// Initial broadcast
	if err := port.broadcastRTMPData(); err != nil {
		log.Printf("RTMP: Couldn't broadcast Data: %v", err)
	}

	setStatus("Starting broadcast loop")

	bcastTicker := time.NewTicker(10 * time.Second)
	defer bcastTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()

		case <-bcastTicker.C:
			setStatus("Broadcasting RTMP Data")
			if err := port.broadcastRTMPData(); err != nil {
				st := fmt.Sprintf("Couldn't broadcast Data: %v", err)
				setStatus(st)
				log.Print(st)
			}
		}
	}
}

func (port *EtherTalkPort) broadcastRTMPData() error {
	for _, dataPkt := range port.rtmpDataPackets(true) {
		dataPktRaw, err := dataPkt.Marshal()
		if err != nil {
			return fmt.Errorf("marshal Data packet: %v", err)
		}

		ddpPkt := &ddp.ExtPacket{
			ExtHeader: ddp.ExtHeader{
				Size:      uint16(len(dataPktRaw)) + atalk.DDPExtHeaderSize,
				Cksum:     0,
				DstNet:    0x0000, // this network
				DstNode:   0xff,   // broadcast packet
				DstSocket: 1,      // the RTMP socket
				SrcNet:    port.MyAddr.Network,
				SrcNode:   port.MyAddr.Node,
				SrcSocket: 1, // the RTMP socket
				Proto:     ddp.ProtoRTMPResp,
			},
			Data: dataPktRaw,
		}

		if err := port.Broadcast(ddpPkt); err != nil {
			return err
		}
	}
	return nil
}

func (port *EtherTalkPort) rtmpDataPackets(splitHorizon bool) []*rtmp.DataPacket {
	// Build up a slice of routing tuples.
	routes := port.Router.RouteTable.ValidRoutes()
	tuples := make([]rtmp.NetworkTuple, 0, len(routes))
	for _, rt := range routes {
		if rt.EtherTalkDirect == port {
			// If the route is actually a direct connection to this port,
			// don't include it.
			// (It's manually set as the first tuple anyway.)
			continue
		}
		if splitHorizon && rt.EtherTalkPeer.Port == port {
			// If the route is through a peer accessible on this port, don't
			// include it.
			continue
		}
		tuples = append(tuples, rtmp.NetworkTuple{
			Extended:   rt.Extended,
			RangeStart: rt.NetStart,
			RangeEnd:   rt.NetEnd,
			Distance:   rt.Distance,
		})
	}
	// "The first tuple in RTMP Data packets sent on extended
	// networks ... indicates the network number range assigned
	// to that network."
	// TODO: support non-extended local networks (LocalTalk)
	first := rtmp.NetworkTuple{
		Extended:   true,
		RangeStart: port.NetStart,
		RangeEnd:   port.NetEnd,
		Distance:   0,
	}

	var packets []*rtmp.DataPacket
	rem := tuples
	for len(rem) > 0 {
		chunk := []rtmp.NetworkTuple{first}

		size := 10 // router network + 1 + router node ID + first tuple
		for _, nt := range rem {
			size += nt.Size()
			if size > atalk.DDPMaxDataSize {
				break
			}
			chunk = append(chunk, nt)
		}
		rem = rem[len(chunk)-1:]

		packets = append(packets, &rtmp.DataPacket{
			RouterAddr:    port.MyAddr,
			Extended:      true,
			NetworkTuples: chunk,
		})
	}
	return packets
}
