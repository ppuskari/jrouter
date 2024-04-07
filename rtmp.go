package main

import (
	"context"
	"log"
	"time"

	"gitea.drjosh.dev/josh/jrouter/atalk/rtmp"
	"github.com/google/gopacket/pcap"
	"github.com/sfiera/multitalk/pkg/ddp"
	"github.com/sfiera/multitalk/pkg/ethertalk"
)

// RTMPMachine implements RTMP on an AppleTalk network attached to the router.
type RTMPMachine struct {
	aarp       *AARPMachine
	cfg        *config
	pcapHandle *pcap.Handle
}

func (m *RTMPMachine) Run(ctx context.Context) error {
	bcastTicker := time.NewTicker(10 * time.Second)
	defer bcastTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()

		case <-bcastTicker.C:
			// Broadcast an RTMP Data
			myAddr, ok := m.aarp.Address()
			if !ok {
				continue
			}

			dataPkt := &rtmp.DataPacket{
				RouterAddr: myAddr.Proto,
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
			// TODO: append more networks!

			dataPktRaw, err := dataPkt.Marshal()
			if err != nil {
				log.Printf("RTMP: Couldn't marshal Data packet: %v", err)
				continue
			}

			ddpPkt := ddp.ExtPacket{
				ExtHeader: ddp.ExtHeader{
					Size:      uint16(len(dataPktRaw)),
					Cksum:     0,
					DstNet:    0,    // this network
					DstNode:   0xff, // broadcast packet
					DstSocket: 1,    // the special RTMP socket
					SrcNet:    myAddr.Proto.Network,
					SrcNode:   myAddr.Proto.Node,
					SrcSocket: 1, // the special RTMP socket
					Proto:     ddp.ProtoRTMPResp,
				},
				Data: dataPktRaw,
			}

			ethFrame, err := ethertalk.AppleTalk(myAddr.Hardware, ddpPkt)
			if err != nil {
				log.Printf("RTMP: Couldn't create EtherTalk frame: %v", err)
			}
			ethFrame.Dst = ethertalk.AppleTalkBroadcast

			ethFrameRaw, err := ethertalk.Marshal(*ethFrame)
			if err != nil {
				log.Printf("RTMP: Couldn't marshal EtherTalk frame: %v", err)
			}

			if err := m.pcapHandle.WritePacketData(ethFrameRaw); err != nil {
				log.Printf("RTMP: Couldn't write frame: %v", err)
			}
		}
	}
}
