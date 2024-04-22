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

	"gitea.drjosh.dev/josh/jrouter/atalk"
	"github.com/google/gopacket/pcap"
	"github.com/sfiera/multitalk/pkg/ddp"
	"github.com/sfiera/multitalk/pkg/ethernet"
	"github.com/sfiera/multitalk/pkg/ethertalk"
)

type Router struct {
	Config      *Config
	PcapHandle  *pcap.Handle
	MyHWAddr    ethernet.Addr
	MyDDPAddr   ddp.Addr
	AARPMachine *AARPMachine
	RouteTable  *RoutingTable
	ZoneTable   *ZoneTable
}

func (rtr *Router) SendEtherTalkDDP(ctx context.Context, pkt *ddp.ExtPacket) error {
	dstEth := ethertalk.AppleTalkBroadcast
	if pkt.DstNode != 0xFF {
		de, err := rtr.AARPMachine.Resolve(ctx, ddp.Addr{Network: pkt.DstNet, Node: pkt.DstNode})
		if err != nil {
			return err
		}
		dstEth = de
	}
	return rtr.sendEtherTalkDDP(dstEth, pkt)
}

func (rtr *Router) BroadcastEtherTalkDDP(pkt *ddp.ExtPacket) error {
	return rtr.sendEtherTalkDDP(ethertalk.AppleTalkBroadcast, pkt)
}

func (rtr *Router) ZoneMulticastEtherTalkDDP(zone string, pkt *ddp.ExtPacket) error {
	return rtr.sendEtherTalkDDP(atalk.MulticastAddr(zone), pkt)
}

func (rtr *Router) sendEtherTalkDDP(dstEth ethernet.Addr, pkt *ddp.ExtPacket) error {
	outFrame, err := ethertalk.AppleTalk(rtr.MyHWAddr, *pkt)
	if err != nil {
		return err
	}
	outFrame.Dst = dstEth
	outFrameRaw, err := ethertalk.Marshal(*outFrame)
	if err != nil {
		return err
	}
	return rtr.PcapHandle.WritePacketData(outFrameRaw)
}
