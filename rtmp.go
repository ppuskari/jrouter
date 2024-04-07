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

package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"gitea.drjosh.dev/josh/jrouter/atalk/rtmp"
	"github.com/google/gopacket/pcap"
	"github.com/sfiera/multitalk/pkg/aarp"
	"github.com/sfiera/multitalk/pkg/ddp"
	"github.com/sfiera/multitalk/pkg/ethernet"
	"github.com/sfiera/multitalk/pkg/ethertalk"
)

// RTMPMachine implements RTMP on an AppleTalk network attached to the router.
type RTMPMachine struct {
	aarp       *AARPMachine
	cfg        *config
	pcapHandle *pcap.Handle
}

// Run executes the machine.
func (m *RTMPMachine) Run(ctx context.Context, incomingCh <-chan *ddp.ExtPacket) error {
	// Await local address assignment before doing anything
	<-m.aarp.Assigned()
	myAddr, ok := m.aarp.Address()
	if !ok {
		return fmt.Errorf("AARP machine closed Assigned channel but Address is not valid")
	}

	// Initial broadcast
	if err := m.broadcastData(myAddr); err != nil {
		log.Printf("RTMP: Couldn't broadcast Data: %v", err)
	}

	bcastTicker := time.NewTicker(10 * time.Second)
	defer bcastTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()

		case <-bcastTicker.C:
			if err := m.broadcastData(myAddr); err != nil {
				log.Printf("RTMP: Couldn't broadcast Data: %v", err)
			}

		case pkt := <-incomingCh:
			switch pkt.Proto {
			case ddp.ProtoRTMPReq:
				// I can answer RTMP requests!
				req, err := rtmp.UnmarshalRequestPacket(pkt.Data)
				if err != nil {
					log.Printf("RTMP: Couldn't unmarshal Request packet: %v", err)
				}

				// should be in the cache...
				theirHWAddr, err := m.aarp.Resolve(ctx, ddp.Addr{Network: pkt.SrcNet, Node: pkt.SrcNode})
				if err != nil {
					log.Printf("RTMP: Couldn't resolve %d.%d to a hardware address: %v", pkt.SrcNet, pkt.SrcNode, err)
					continue
				}

				switch req.Function {
				case 1: // RTMP Request
					// Respond with RTMP Response
					respPkt := &rtmp.ResponsePacket{
						SenderAddr: myAddr.Proto,
						Extended:   true,
						RangeStart: m.cfg.EtherTalk.NetStart,
						RangeEnd:   m.cfg.EtherTalk.NetEnd,
					}
					respPktRaw, err := respPkt.Marshal()
					if err != nil {
						log.Printf("RTMP: Couldn't marshal RTMP Response packet: %v", err)
						continue
					}
					ddpPkt := &ddp.ExtPacket{
						ExtHeader: ddp.ExtHeader{
							Size:      uint16(len(respPktRaw)),
							Cksum:     0,
							DstNet:    pkt.SrcNet,
							DstNode:   pkt.SrcNode,
							DstSocket: 1, // the RTMP socket
							SrcNet:    myAddr.Proto.Network,
							SrcNode:   myAddr.Proto.Node,
							SrcSocket: 1, // the RTMP socket
							Proto:     ddp.ProtoRTMPResp,
						},
						Data: respPktRaw,
					}

					if err := m.send(myAddr.Hardware, theirHWAddr, ddpPkt); err != nil {
						log.Printf("RTMP: Couldn't send Data broadcast: %v", err)
					}

				case 2, 3:
					// Like the Data broadcast, but solicited by a request (RDR).
					// TODO: handle split-horizon processing
					dataPkt := m.dataPacket(myAddr.Proto)

					dataPktRaw, err := dataPkt.Marshal()
					if err != nil {
						log.Printf("RTMP: Couldn't marshal Data packet: %v", err)
						continue
					}

					ddpPkt := &ddp.ExtPacket{
						ExtHeader: ddp.ExtHeader{
							Size:      uint16(len(dataPktRaw)),
							Cksum:     0,
							DstNet:    pkt.SrcNet,
							DstNode:   pkt.SrcNode,
							DstSocket: 1, // the RTMP socket
							SrcNet:    myAddr.Proto.Network,
							SrcNode:   myAddr.Proto.Node,
							SrcSocket: 1, // the RTMP socket
							Proto:     ddp.ProtoRTMPResp,
						},
						Data: dataPktRaw,
					}

					if err := m.send(myAddr.Hardware, theirHWAddr, ddpPkt); err != nil {
						log.Printf("RTMP: Couldn't send Data response: %v", err)
					}
				}

			case ddp.ProtoRTMPResp:
				// It's a peer router on the AppleTalk network!
				// TODO: integrate this information with the routing table
				log.Print("RTMP: Got Response or Data")

			default:
				log.Printf("RTMP: invalid DDP type %d on socket 1", pkt.Proto)
			}

		}
	}
}

func (m *RTMPMachine) send(src, dst ethernet.Addr, ddpPkt *ddp.ExtPacket) error {
	ethFrame, err := ethertalk.AppleTalk(src, *ddpPkt)
	if err != nil {
		return err
	}
	ethFrame.Dst = dst

	ethFrameRaw, err := ethertalk.Marshal(*ethFrame)
	if err != nil {
		return err
	}
	return m.pcapHandle.WritePacketData(ethFrameRaw)
}

func (m *RTMPMachine) broadcastData(myAddr aarp.AddrPair) error {
	dataPkt := m.dataPacket(myAddr.Proto)

	dataPktRaw, err := dataPkt.Marshal()
	if err != nil {
		return fmt.Errorf("marshal Data packet: %v", err)
	}

	ddpPkt := &ddp.ExtPacket{
		ExtHeader: ddp.ExtHeader{
			Size:      uint16(len(dataPktRaw)),
			Cksum:     0,
			DstNet:    0,    // this network
			DstNode:   0xff, // broadcast packet
			DstSocket: 1,    // the RTMP socket
			SrcNet:    myAddr.Proto.Network,
			SrcNode:   myAddr.Proto.Node,
			SrcSocket: 1, // the RTMP socket
			Proto:     ddp.ProtoRTMPResp,
		},
		Data: dataPktRaw,
	}

	return m.send(myAddr.Hardware, ethertalk.AppleTalkBroadcast, ddpPkt)
}

func (m *RTMPMachine) dataPacket(myAddr ddp.Addr) *rtmp.DataPacket {
	return &rtmp.DataPacket{
		RouterAddr: myAddr,
		Extended:   true,
		NetworkTuples: []rtmp.NetworkTuple{
			// "The first tuple in RTMP Data packets sent on extended
			// networks ... indicates the network number range assigned
			// to that network."
			{
				Extended:   true,
				RangeStart: m.cfg.EtherTalk.NetStart,
				RangeEnd:   m.cfg.EtherTalk.NetEnd,
				Distance:   0,
			},
		},
	}
	// TODO: append more networks! implement a route table!
}
