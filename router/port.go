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
	"errors"
	"io"
	"log"

	"gitea.drjosh.dev/josh/jrouter/atalk"
	"github.com/google/gopacket/pcap"
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
	AvailableZones  []string
	PcapHandle      *pcap.Handle
	AARPMachine     *AARPMachine
	Router          *Router
}

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
			log.Printf("Couldn't read AppleTalk / AARP packet data: %v", err)
			return
		}

		ethFrame := new(ethertalk.Packet)
		if err := ethertalk.Unmarshal(rawPkt, ethFrame); err != nil {
			log.Printf("Couldn't unmarshal EtherTalk frame: %v", err)
			continue
		}

		// Ignore if sent by me
		if ethFrame.Src == port.EthernetAddr {
			continue
		}

		switch ethFrame.SNAPProto {
		case ethertalk.AARPProto:
			// log.Print("Got an AARP frame")
			port.AARPMachine.Handle(ctx, ethFrame)

		case ethertalk.AppleTalkProto:
			// log.Print("Got an AppleTalk frame")
			ddpkt := new(ddp.ExtPacket)
			if err := ddp.ExtUnmarshal(ethFrame.Payload, ddpkt); err != nil {
				log.Printf("Couldn't unmarshal DDP packet: %v", err)
				continue
			}
			// log.Printf("DDP: src (%d.%d s %d) dst (%d.%d s %d) proto %d data len %d",
			// 	ddpkt.SrcNet, ddpkt.SrcNode, ddpkt.SrcSocket,
			// 	ddpkt.DstNet, ddpkt.DstNode, ddpkt.DstSocket,
			// 	ddpkt.Proto, len(ddpkt.Data))

			// Glean address info for AMT, but only if SrcNet is our net
			// (If it's not our net, then it was routed from elsewhere, and
			// we'd be filling the AMT with entries for a router.)
			if ddpkt.SrcNet >= port.NetStart && ddpkt.SrcNet <= port.NetEnd {
				srcAddr := ddp.Addr{Network: ddpkt.SrcNet, Node: ddpkt.SrcNode}
				port.AARPMachine.Learn(srcAddr, ethFrame.Src)
				// log.Printf("DDP: Gleaned that %d.%d -> %v", srcAddr.Network, srcAddr.Node, ethFrame.Src)
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
					log.Printf("DDP: Couldn't forward packet: %v", err)
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
					log.Printf("RTMP: Couldn't handle: %v", err)
				}

			case 2: // The NIS (name information socket / NBP socket)
				if err := port.HandleNBP(ctx, ddpkt); err != nil {
					log.Printf("NBP: Couldn't handle: %v", err)
				}

			case 4: // The AEP socket
				if err := port.Router.HandleAEP(ctx, ddpkt); err != nil {
					log.Printf("AEP: Couldn't handle: %v", err)
				}

			case 6: // The ZIS (zone information socket / ZIP socket)
				if err := port.HandleZIP(ctx, ddpkt); err != nil {
					log.Printf("ZIP: couldn't handle: %v", err)
				}

			default:
				log.Printf("DDP: No handler for socket %d", ddpkt.DstSocket)
			}

		default:
			log.Printf("Read unknown packet %s -> %s with payload %x", ethFrame.Src, ethFrame.Dst, ethFrame.Payload)

		}
	}
}

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

func (port *EtherTalkPort) Broadcast(pkt *ddp.ExtPacket) error {
	return port.send(ethertalk.AppleTalkBroadcast, pkt)
}

func (port *EtherTalkPort) ZoneMulticast(zone string, pkt *ddp.ExtPacket) error {
	return port.send(atalk.MulticastAddr(zone), pkt)
}

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
	return port.PcapHandle.WritePacketData(outFrameRaw)
}
