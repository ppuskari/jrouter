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
		// A soft/non-seed router must not advertise provisional startup-range
		// routing information before it owns a real cable-range address.
		if _, _, operational := port.aarpMachine.OperationalRange(); !operational {
			return nil
		}

		// I can answer RTMP requests!
		req, err := rtmp.UnmarshalRequestPacket(pkt.Data)
		if err != nil {
			return fmt.Errorf("unmarshal Request packet: %w", err)
		}

		switch req.Function {
		case rtmp.FunctionRequest:
			// Respond with RTMP Response
			cableStart, cableEnd := port.cableRange()
			respPkt := &rtmp.ResponsePacket{
				SenderAddr: port.myAddr,
				Extended:   true,
				RangeStart: cableStart,
				RangeEnd:   cableEnd,
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
					SrcNet:    port.myAddr.Network,
					SrcNode:   port.myAddr.Node,
					SrcSocket: 1, // the RTMP socket
					Proto:     ddp.ProtoRTMPResp,
				},
				Data: respPktRaw,
			}

			if err := port.router.Output(ctx, ddpPkt); err != nil {
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
						SrcNet:    port.myAddr.Network,
						SrcNode:   port.myAddr.Node,
						SrcSocket: 1, // the RTMP socket
						Proto:     ddp.ProtoRTMPResp,
					},
					Data: dataPktRaw,
				}

				if err := port.router.Output(ctx, ddpPkt); err != nil {
					return fmt.Errorf("send Data: %w", err)
				}
			}

		case rtmp.FunctionLoopProbe:
			if port.router.handleLoopProbeReturn(port, pkt, req.Data) {
				return nil
			}
			port.logger.Debug(
				"RTMP: received foreign or stale Loop Probe",
				"source", fmt.Sprintf("%d.%d", pkt.SrcNet, pkt.SrcNode),
			)
			return nil
		}

	case ddp.ProtoRTMPResp:
		// It's a peer router on the AppleTalk network!
		port.logger.Debug("RTMP: Got Response or Data")
		dataPkt, err := rtmp.UnmarshalDataPacket(pkt.Data)
		if err != nil {
			return fmt.Errorf("unmarshal RTMP Data packet: %w", err)
		}
		if len(dataPkt.NetworkTuples) > 0 {
			first := dataPkt.NetworkTuples[0]
			port.observeCableRange(
				first.RangeStart,
				first.RangeEnd,
				fmt.Sprintf("RTMP %d.%d", dataPkt.RouterAddr.Network, dataPkt.RouterAddr.Node),
				false,
			)
		}
		peer := &EtherTalkPeer{
			Port:     port,
			PeerAddr: dataPkt.RouterAddr,
		}

		var noZones []ddp.Network
		for _, nt := range dataPkt.NetworkTuples {
			route, err := port.router.RouteTable.UpsertRoute(
				peer,
				nt.Extended,
				nt.RangeStart,
				nt.RangeEnd,
				nt.Distance+1,
			)
			if err != nil {
				return fmt.Errorf("upsert EtherTalk route: %v", err)
			}
			if len(port.router.RouteTable.byNetwork[nt.RangeStart].ZoneNames) == 0 {
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
					SrcNet:    port.myAddr.Network,
					SrcNode:   port.myAddr.Node,
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
	ctx, setStatus, _ := status.AddSimpleItem(ctx, fmt.Sprintf("RTMP on %s", port.device))
	defer func() {
		setStatus(fmt.Sprintf("Run loop stopped! Return: %v", err))
	}()

	setStatus("Awaiting DDP address assignment")

	// A soft/non-seed port may first own only a provisional startup-range
	// address. RTMP must not advertise until the real cable range is adopted.
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-port.aarpMachine.Operational():
	}
	if addr, ok := port.aarpMachine.Address(); ok {
		port.myAddr = addr.Proto
	}

	setStatus("Starting broadcast loop")

	first := make(chan struct{}, 1)
	first <- struct{}{}

	bcastTicker := time.Tick(10 * time.Second)

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()

		case <-bcastTicker:
			// continue below
		case <-first:
			// continue below
		}
		setStatus("Broadcasting RTMP Data")
		if err := port.broadcastRTMPData(); err != nil {
			setStatus(fmt.Sprintf("Couldn't broadcast Data: %v", err))
			port.logger.Error("RTMP: Couldn't broadcast Data", "error", err)
		}
	}
}

func (port *EtherTalkPort) broadcastRTMPData() error {
	start, end, operational := port.aarpMachine.OperationalRange()
	if !operational {
		// A dynamic non-seed may be rebinding to a newly learned range. Do not
		// advertise stale cable information during the provisional interval.
		return nil
	}
	if addr, ok := port.aarpMachine.Address(); ok {
		port.myAddr = addr.Proto
	}
	port.setCableRange(start, end)

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
				SrcNet:    port.myAddr.Network,
				SrcNode:   port.myAddr.Node,
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

func rtmpAdvertisedDistance(route Route, hopCountReduction bool) uint8 {
	if hopCountReduction && route.RouteOrigin().Kind == RouteOriginAURP {
		return 1
	}
	return route.Distance
}

func (port *EtherTalkPort) rtmpDataPackets(splitHorizon bool) []*rtmp.DataPacket {
	// Build up a slice of routing tuples.
	var tuples []rtmp.NetworkTuple
	for r := range port.router.RouteTable.ValidRoutes {
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
		hopCountReduction := port.router.Config != nil &&
			port.router.Config.AURP.HopCountReduction
		tuples = append(tuples, rtmp.NetworkTuple{
			Extended:   r.Extended,
			RangeStart: r.NetStart,
			RangeEnd:   r.NetEnd,
			Distance:   rtmpAdvertisedDistance(r, hopCountReduction),
		})
	}
	// "The first tuple in RTMP Data packets sent on extended
	// networks ... indicates the network number range assigned
	// to that network."
	// TODO: support non-extended local networks (LocalTalk)
	cableStart, cableEnd := port.cableRange()
	first := rtmp.NetworkTuple{
		Extended:   true,
		RangeStart: cableStart,
		RangeEnd:   cableEnd,
		Distance:   0,
	}

	var packets []*rtmp.DataPacket
	rem := tuples
	if len(rem) == 0 {
		return []*rtmp.DataPacket{{
			RouterAddr:    port.myAddr,
			Extended:      true,
			NetworkTuples: []rtmp.NetworkTuple{first},
		}}
	}
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
			RouterAddr:    port.myAddr,
			Extended:      true,
			NetworkTuples: chunk,
		})
	}
	return packets
}
