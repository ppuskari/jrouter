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

	"github.com/sfiera/multitalk/pkg/ddp"
	"github.com/sfiera/multitalk/pkg/ethertalk"
)

// EtherTalkPeer holds data needed to forward packets to another router on the
// EtherTalk network.
type EtherTalkPeer struct {
	Port     *EtherTalkPort
	PeerAddr ddp.Addr
}

// Forward forwards a DDP packet to the next router.
func (p *EtherTalkPeer) Forward(ctx context.Context, pkt *ddp.ExtPacket) error {
	// TODO: AARP resolution can block
	de, err := p.Port.AARPMachine.Resolve(ctx, p.PeerAddr)
	if err != nil {
		return err
	}
	outFrame, err := ethertalk.AppleTalk(p.Port.EthernetAddr, *pkt)
	if err != nil {
		return err
	}
	outFrame.Dst = de
	outFrameRaw, err := ethertalk.Marshal(*outFrame)
	if err != nil {
		return err
	}
	if len(outFrameRaw) < 64 {
		outFrameRaw = append(outFrameRaw, make([]byte, 64-len(outFrameRaw))...)
	}
	return p.Port.PcapHandle.WritePacketData(outFrameRaw)
}
