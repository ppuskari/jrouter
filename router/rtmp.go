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
	"time"

	"drjosh.dev/jrouter/atalk"
	"drjosh.dev/jrouter/atalk/rtmp"
	"drjosh.dev/jrouter/atalk/zip"
	"drjosh.dev/jrouter/status"

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
			return fmt.Errorf("TODO: handle Loop Probes")
		}

	case ddp.ProtoRTMPResp:
		// It's a peer router on the AppleTalk network!
		slog.Debug("RTMP: Got Response or Data")
		dataPkt, err := rtmp.UnmarshalDataPacket(pkt.Data)
		if err != nil {
			return fmt.Errorf("unmarshal RTMP Data packet: %w", err)
		}
		peer := &EtherTalkPeer{
			Port:     port,
			PeerAddr: dataPkt.RouterAddr,
		}

		var noZones []ddp.Network
		for _, nt := range dataPkt.NetworkTuples {
			route, err := port.Router.RouteTable.UpsertRoute(
				peer,
				nt.Extended,
				nt.RangeStart,
				nt.RangeEnd,
				nt.Distance+1,
			)
			if err != nil {
				return fmt.Errorf("upsert EtherTalk route: %v", err)
			}
			if len(port.Router.RouteTable.byNetwork[nt.RangeStart].ZoneNames) == 0 {
				noZones = append(noZones, route.NetStart)
			}
		}
		if len(noZones) > 0 {
			// Send a ZIP Query for all networks we don't have zone names for.
			// TODO: split networks to fit in multiple packets as needed
			qryPkt, err := (&zip.QueryPacket{Networks: noZones}).Marshal()
			if err != nil {
				return fmt.Errorf("marshal ZIP Query packet: %w", err)
			}
			outDDP := &ddp.ExtPacket{
				ExtHeader: ddp.ExtHeader{
					Size:      uint16(len(qryPkt)) + atalk.DDPExtHeaderSize,
					Cksum:     0,
					SrcNet:    port.MyAddr.Network,
					SrcNode:   port.MyAddr.Node,
					SrcSocket: 6,
					DstNet:    pkt.SrcNet,
					DstNode:   pkt.SrcNode,
					DstSocket: 6, // ZIP socket
					Proto:     ddp.ProtoZIP,
				},
				Data: qryPkt,
			}
			if err := port.Send(ctx, outDDP); err != nil {
				return fmt.Errorf("sending ZIP Query: %w", err)
			}
		}

	default:
		return fmt.Errorf("invalid DDP type %d on socket 1", pkt.Proto)
	}

	return nil
}

// RunRTMP makes periodic RTMP Data broadcasts on this port.
func (port *EtherTalkPort) RunRTMP(ctx context.Context) (err error) {
	ctx, setStatus, _ := status.AddSimpleItem(ctx, fmt.Sprintf("RTMP on %s", port.Device))
	defer func() {
		setStatus(fmt.Sprintf("Run loop stopped! Return: %v", err))
	}()

	setStatus("Awaiting DDP address assignment")

	// Await local address assignment before doing anything
	<-port.AARPMachine.Assigned()

	setStatus("Starting broadcast loop")

	first := make(chan struct{}, 1)
	first <- struct{}{}

	bcastTicker := time.NewTicker(10 * time.Second)
	defer bcastTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()

		case <-bcastTicker.C:
			// continue below
		case <-first:
			// continue below
		}
		setStatus("Broadcasting RTMP Data")
		if err := port.broadcastRTMPData(); err != nil {
			setStatus(fmt.Sprintf("Couldn't broadcast Data: %v", err))
			slog.Error("RTMP: Couldn't broadcast Data", "error", err)
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
	var tuples []rtmp.NetworkTuple
	for r := range port.Router.RouteTable.ValidRoutes {
		if r.Target.RouteTargetKey() == port.RouteTargetKey() {
			// If the route is actually a direct connection to this port,
			// don't include it.
			// (It's manually set as the first tuple anyway.)
			continue
		}
		etPeer, _ := r.Target.(*EtherTalkPeer)
		if splitHorizon && etPeer != nil && etPeer.Port == port {
			// If the route is through a peer accessible on this port, don't
			// include it.
			continue
		}
		tuples = append(tuples, rtmp.NetworkTuple{
			Extended:   r.Extended,
			RangeStart: r.NetStart,
			RangeEnd:   r.NetEnd,
			Distance:   r.Distance,
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
