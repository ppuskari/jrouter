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
	"encoding/binary"
	"errors"
	"io"
	"log/slog"
	"strconv"

	"drjosh.dev/jrouter/atalk"
	"github.com/google/gopacket/pcap"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/sfiera/multitalk/pkg/ddp"
	"github.com/sfiera/multitalk/pkg/ethernet"
	"github.com/sfiera/multitalk/pkg/ethertalk"
)

// EtherTalkPort is all the data and helpers needed for EtherTalk on one port.
type EtherTalkPort struct {
	Device          string
	EthernetAddr    ethernet.Addr
	NetStart        ddp.Network
	NetEnd          ddp.Network
	MyAddr          ddp.Addr
	DefaultZoneName string
	AvailableZones  StringSet
	PcapHandle      *pcap.Handle
	AARPMachine     *AARPMachine
	Router          *Router
}

// Serve runs a loop that reads AARP or AppleTalk packets from the network
// device, and handles them.
func (port *EtherTalkPort) Serve(ctx context.Context) {
	for {
		if ctx.Err() != nil {
			return
		}

		rawPkt, _, err := port.PcapHandle.ReadPacketData()
		if errors.Is(err, pcap.NextErrorTimeoutExpired) {
			continue
		}
		if errors.Is(err, io.EOF) || errors.Is(err, pcap.NextErrorNoMorePackets) {
			return
		}
		if err != nil {
			slog.Error("Couldn't read AppleTalk / AARP packet data", "error", err)
			return
		}

		ethFrame := new(ethertalk.Packet)
		if err := ethertalk.Unmarshal(rawPkt, ethFrame); err != nil {
			atalkInvalidPacketsInCounter.With(prometheus.Labels{"port": port.Device}).Inc()
			slog.Error("Couldn't unmarshal EtherTalk frame", "error", err)
			continue
		}

		// Ignore if sent by me
		if ethFrame.Src == port.EthernetAddr {
			continue
		}

		switch ethFrame.SNAPProto {
		case ethertalk.AARPProto:
			// slog.Debug("Got an AARP frame")
			promLabels := prometheus.Labels{"port": port.Device}
			aarpPacketsInCounter.With(promLabels).Inc()
			aarpBytesInCounter.With(promLabels).Add(float64(len(rawPkt)))

			port.AARPMachine.Handle(ctx, ethFrame)

		case ethertalk.AppleTalkProto:
			// slog.Debug("Got an AppleTalk frame")

			// Workaround for strict length checking in sfiera/multitalk
			payload := ethFrame.Payload
			if len(payload) < 2 {
				slog.Error("Couldn't unmarshal DDP packet: too small", "payload-length", len(payload))
			}
			if size := binary.BigEndian.Uint16(payload[:2]) & 0x3ff; len(payload) > int(size) {
				payload = payload[:size]
			}

			ddpkt := new(ddp.ExtPacket)
			if err := ddp.ExtUnmarshal(payload, ddpkt); err != nil {
				slog.Error("Couldn't unmarshal DDP packet", "error", err)
				continue
			}

			promLabels := prometheus.Labels{
				"port":       port.Device,
				"src_net":    strconv.Itoa(int(ddpkt.SrcNet)),
				"src_node":   strconv.Itoa(int(ddpkt.SrcNode)),
				"src_socket": strconv.Itoa(int(ddpkt.SrcSocket)),
				"dst_net":    strconv.Itoa(int(ddpkt.DstNet)),
				"dst_node":   strconv.Itoa(int(ddpkt.DstNode)),
				"dst_socket": strconv.Itoa(int(ddpkt.DstSocket)),
				"proto":      strconv.Itoa(int(ddpkt.Proto)),
			}
			atalkPacketsInCounter.With(promLabels).Inc()
			atalkBytesInCounter.With(promLabels).Add(float64(len(rawPkt)))

			// slog.Debug(fmt.Sprintf("DDP: src (%d.%d s %d) dst (%d.%d s %d) proto %d data len %d",
			// 	ddpkt.SrcNet, ddpkt.SrcNode, ddpkt.SrcSocket,
			// 	ddpkt.DstNet, ddpkt.DstNode, ddpkt.DstSocket,
			// 	ddpkt.Proto, len(ddpkt.Data)))

			// Glean address info for AMT, but only if SrcNet is our net
			// (If it's not our net, then it was routed from elsewhere, and
			// we'd be filling the AMT with entries for a router.)
			if ddpkt.SrcNet >= port.NetStart && ddpkt.SrcNet <= port.NetEnd {
				srcAddr := ddp.Addr{Network: ddpkt.SrcNet, Node: ddpkt.SrcNode}
				port.AARPMachine.Learn(srcAddr, ethFrame.Src)
				// slog.Debug(fmt.Sprintf("DDP: Gleaned that %d.%d -> %v", srcAddr.Network, srcAddr.Node, ethFrame.Src))
			}

			// Packet for us? First, who am I?
			myAddr, ok := port.AARPMachine.Address()
			if !ok {
				continue
			}
			port.MyAddr = myAddr.Proto

			// Our network?
			// "The network number 0 is reserved to mean unknown; by default
			// it specifies the local network to which the node is
			// connected. Packets whose destination network number is 0 are
			// addressed to a node on the local network."
			// TODO: more generic routing
			if ddpkt.DstNet != 0 && !(ddpkt.DstNet >= port.NetStart && ddpkt.DstNet <= port.NetEnd) {
				// Is it for a network in the routing table?
				if err := port.Router.Forward(ctx, ddpkt); err != nil {
					slog.Error("DDP: Couldn't forward packet", "error", err)
				}
				continue
			}

			// To me?
			// "Node ID 0 indicates any router on the network"- I'm a router
			// "node ID $FF indicates either a network-wide or zone-specific
			// broadcast"- that's relevant
			if ddpkt.DstNode != 0 && ddpkt.DstNode != 0xff && ddpkt.DstNode != myAddr.Proto.Node {
				continue
			}

			switch ddpkt.DstSocket {
			case 1: // The RTMP socket
				if err := port.HandleRTMP(ctx, ddpkt); err != nil {
					slog.Error("RTMP: Couldn't handle packet", "error", err)
				}

			case 2: // The NIS (name information socket / NBP socket)
				if err := port.HandleNBP(ctx, ddpkt); err != nil {
					slog.Error("NBP: Couldn't handle packet", "error", err)
				}

			case 4: // The AEP socket
				if err := port.Router.HandleAEP(ctx, ddpkt); err != nil {
					slog.Error("AEP: Couldn't handle packet", "error", err)
				}

			case 6: // The ZIS (zone information socket / ZIP socket)
				if err := port.HandleZIP(ctx, ddpkt); err != nil {
					slog.Error("ZIP: Couldn't handle packet", "error", err)
				}

			default:
				slog.Error("DDP: No handler for socket", "dst-socket", ddpkt.DstSocket)
			}

		default:
			slog.Error("Read unknown packet",
				"ethernet-src", ethFrame.Src,
				"ethernet-dst", ethFrame.Dst,
				"payload", ethFrame.Payload,
			)

		}
	}
}

// Send sends a DDP packet out this port to the destination node.
// If pkt.DstNode = 0xFF, then the packet is broadcast.
func (port *EtherTalkPort) Send(ctx context.Context, pkt *ddp.ExtPacket) error {
	dstEth := ethertalk.AppleTalkBroadcast
	if pkt.DstNode != 0xFF {
		de, err := port.AARPMachine.Resolve(ctx, ddp.Addr{Network: pkt.DstNet, Node: pkt.DstNode})
		if err != nil {
			return err
		}
		dstEth = de
	}
	return port.send(dstEth, pkt)
}

// Broadcast broadcasts the DDP packet on this port.
func (port *EtherTalkPort) Broadcast(pkt *ddp.ExtPacket) error {
	return port.send(ethertalk.AppleTalkBroadcast, pkt)
}

// ZoneMulticast broadcasts the DDP packet to a zone multicast hwaddr.
// The specific address used is computed from the zone name.
func (port *EtherTalkPort) ZoneMulticast(zone string, pkt *ddp.ExtPacket) error {
	return port.send(atalk.MulticastAddr(zone), pkt)
}

// Forward is another name for Send.
// (EtherTalk ports can be used as a route target.)
func (port *EtherTalkPort) Forward(ctx context.Context, pkt *ddp.ExtPacket) error {
	return port.Send(ctx, pkt)
}

// RouteTargetKey returns "EtherTalkPort|device name".
func (port *EtherTalkPort) RouteTargetKey() string {
	return "EtherTalkPort|" + port.Device
}

// Class returns TargetClassDirect.
func (port *EtherTalkPort) Class() TargetClass { return TargetClassDirect }

func (port *EtherTalkPort) String() string {
	return port.Device
}

// send is used to send EtherTalk packets. dstEth is either the destination node
// or another AppleTalk router that will forward the packet. Or it's a broadcast
// packet and dstEth should be a broadcast hwaddr.
func (port *EtherTalkPort) send(dstEth ethernet.Addr, pkt *ddp.ExtPacket) error {
	outFrame, err := ethertalk.AppleTalk(port.EthernetAddr, *pkt)
	if err != nil {
		return err
	}
	outFrame.Dst = dstEth
	outFrameRaw, err := ethertalk.Marshal(*outFrame)
	if err != nil {
		return err
	}
	if len(outFrameRaw) < 64 {
		outFrameRaw = append(outFrameRaw, make([]byte, 64-len(outFrameRaw))...)
	}

	promLabels := prometheus.Labels{
		"port":       port.Device,
		"src_net":    strconv.Itoa(int(pkt.SrcNet)),
		"src_node":   strconv.Itoa(int(pkt.SrcNode)),
		"src_socket": strconv.Itoa(int(pkt.SrcSocket)),
		"dst_net":    strconv.Itoa(int(pkt.DstNet)),
		"dst_node":   strconv.Itoa(int(pkt.DstNode)),
		"dst_socket": strconv.Itoa(int(pkt.DstSocket)),
		"proto":      strconv.Itoa(int(pkt.Proto)),
	}
	atalkPacketsOutCounter.With(promLabels).Inc()
	atalkBytesOutCounter.With(promLabels).Add(float64(len(outFrameRaw)))

	return port.PcapHandle.WritePacketData(outFrameRaw)
}
