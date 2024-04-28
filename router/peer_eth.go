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

	"github.com/google/gopacket/pcap"
	"github.com/sfiera/multitalk/pkg/ddp"
	"github.com/sfiera/multitalk/pkg/ethernet"
	"github.com/sfiera/multitalk/pkg/ethertalk"
)

// EtherTalkPeer holds data needed to exchange routes and zones with another
// router on the EtherTalk network.
type EtherTalkPeer struct {
	PcapHandle *pcap.Handle
	MyHWAddr   ethernet.Addr
	AARP       *AARPMachine
	PeerAddr   ddp.Addr
}

// Forward forwards a DDP packet to the next router.
func (p *EtherTalkPeer) Forward(ctx context.Context, pkt *ddp.ExtPacket) error {
	// TODO: AARP resolution can block
	de, err := p.AARP.Resolve(ctx, p.PeerAddr)
	if err != nil {
		return err
	}
	outFrame, err := ethertalk.AppleTalk(p.MyHWAddr, *pkt)
	if err != nil {
		return err
	}
	outFrame.Dst = de
	outFrameRaw, err := ethertalk.Marshal(*outFrame)
	if err != nil {
		return err
	}
	return p.PcapHandle.WritePacketData(outFrameRaw)
}
