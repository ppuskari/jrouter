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

	"github.com/google/gopacket/pcap"

	"github.com/sfiera/multitalk/pkg/aarp"
	"github.com/sfiera/multitalk/pkg/ddp"
	"github.com/sfiera/multitalk/pkg/ethernet"
	"github.com/sfiera/multitalk/pkg/ethertalk"
)

// RTMPMachine implements RTMP on an AppleTalk network attached to the router.
type RTMPMachine struct {
	AARPMachine  *AARPMachine
	Config       *Config
	PcapHandle   *pcap.Handle
	RoutingTable *RouteTable

	IncomingCh chan *ddp.ExtPacket
}

func (m *RTMPMachine) Handle(ctx context.Context, pkt *ddp.ExtPacket) {
	select {
	case <-ctx.Done():
	case m.IncomingCh <- pkt:
	}
}

// Run executes the machine.
func (m *RTMPMachine) Run(ctx context.Context) (err error) {
	ctx, setStatus, _ := status.AddSimpleItem(ctx, "RTMP")
	defer func() {
		setStatus(fmt.Sprintf("Run loop stopped! Return: %v", err))
	}()

	setStatus("Awaiting DDP address assignment")

	// Await local address assignment before doing anything
	<-m.AARPMachine.Assigned()
	myAddr, ok := m.AARPMachine.Address()
	if !ok {
		return fmt.Errorf("AARP machine closed Assigned channel but Address is not valid")
	}

	setStatus("Initial RTMP Data broadcast")

	// Initial broadcast
	if err := m.broadcastData(myAddr); err != nil {
		log.Printf("RTMP: Couldn't broadcast Data: %v", err)
	}

	setStatus("Starting packet loop")

	bcastTicker := time.NewTicker(10 * time.Second)
	defer bcastTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()

		case <-bcastTicker.C:
			setStatus("Broadcasting RTMP Data")
			if err := m.broadcastData(myAddr); err != nil {
				log.Printf("RTMP: Couldn't broadcast Data: %v", err)
			}

		case pkt := <-m.IncomingCh:
			setStatus("Handling incoming packet")
			switch pkt.Proto {
			case ddp.ProtoRTMPReq:
				// I can answer RTMP requests!
				req, err := rtmp.UnmarshalRequestPacket(pkt.Data)
				if err != nil {
					log.Printf("RTMP: Couldn't unmarshal Request packet: %v", err)
				}

				// should be in the cache...
				theirHWAddr, err := m.AARPMachine.Resolve(ctx, ddp.Addr{Network: pkt.SrcNet, Node: pkt.SrcNode})
				if err != nil {
					log.Printf("RTMP: Couldn't resolve %d.%d to a hardware address: %v", pkt.SrcNet, pkt.SrcNode, err)
					continue
				}

				switch req.Function {
				case rtmp.FunctionRequest:
					// Respond with RTMP Response
					respPkt := &rtmp.ResponsePacket{
						SenderAddr: myAddr.Proto,
						Extended:   true,
						RangeStart: m.Config.EtherTalk.NetStart,
						RangeEnd:   m.Config.EtherTalk.NetEnd,
					}
					respPktRaw, err := respPkt.Marshal()
					if err != nil {
						log.Printf("RTMP: Couldn't marshal RTMP Response packet: %v", err)
						continue
					}
					ddpPkt := &ddp.ExtPacket{
						ExtHeader: ddp.ExtHeader{
							Size:      uint16(len(respPktRaw)) + atalk.DDPExtHeaderSize,
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

				case rtmp.FunctionRDRSplitHorizon, rtmp.FunctionRDRComplete:
					// Like the Data broadcast, but solicited by a request (RDR).
					// TODO: handle split-horizon processing
					for _, dataPkt := range m.dataPackets(myAddr.Proto) {
						dataPktRaw, err := dataPkt.Marshal()
						if err != nil {
							log.Printf("RTMP: Couldn't marshal Data packet: %v", err)
							break
						}

						ddpPkt := &ddp.ExtPacket{
							ExtHeader: ddp.ExtHeader{
								Size:      uint16(len(dataPktRaw)) + atalk.DDPExtHeaderSize,
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
							break
						}
					}

				case rtmp.FunctionLoopProbe:
					log.Print("RTMP: TODO: handle Loop Probes")

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
					PcapHandle: m.PcapHandle,
					MyHWAddr:   m.AARPMachine.myAddr.Hardware,
					AARP:       m.AARPMachine,
					PeerAddr:   dataPkt.RouterAddr,
				}

				for _, rt := range dataPkt.NetworkTuples {
					if err := m.RoutingTable.UpsertEthRoute(peer, rt.Extended, rt.RangeStart, rt.RangeEnd, rt.Distance+1); err != nil {
						log.Printf("RTMP: Couldn't upsert EtherTalk route: %v", err)
					}
				}

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
	return m.PcapHandle.WritePacketData(ethFrameRaw)
}

func (m *RTMPMachine) broadcastData(myAddr aarp.AddrPair) error {
	for _, dataPkt := range m.dataPackets(myAddr.Proto) {
		dataPktRaw, err := dataPkt.Marshal()
		if err != nil {
			return fmt.Errorf("marshal Data packet: %v", err)
		}

		ddpPkt := &ddp.ExtPacket{
			ExtHeader: ddp.ExtHeader{
				Size:      uint16(len(dataPktRaw)) + atalk.DDPExtHeaderSize,
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

		if err := m.send(myAddr.Hardware, ethertalk.AppleTalkBroadcast, ddpPkt); err != nil {
			return err
		}
	}
	return nil
}

func (m *RTMPMachine) dataPackets(myAddr ddp.Addr) []*rtmp.DataPacket {
	// Build up a slice of routing tuples.
	routes := m.RoutingTable.ValidRoutes()
	tuples := make([]rtmp.NetworkTuple, 0, len(routes))
	for _, rt := range routes {
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
		RangeStart: m.Config.EtherTalk.NetStart,
		RangeEnd:   m.Config.EtherTalk.NetEnd,
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
			RouterAddr:    myAddr,
			Extended:      true,
			NetworkTuples: chunk,
		})
	}
	return packets
}
