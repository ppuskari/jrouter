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

	"github.com/sfiera/multitalk/pkg/ddp"
)

// EtherTalkPeer holds data needed to forward packets to another router on the
// EtherTalk network.
type EtherTalkPeer struct {
	Port     *EtherTalkPort
	PeerAddr ddp.Addr
}

// Forward forwards a DDP packet to the next router.
func (p *EtherTalkPeer) Forward(ctx context.Context, pkt *ddp.ExtPacket) error {
	destEth, waitCh := p.Port.aarpMachine.lookupOrWait(p.PeerAddr)
	if waitCh == nil {
		// Cached address still valid
		return p.Port.send(destEth, pkt)
	}
	// Add to outbox for the router's address
	p.Port.outboxPush(waitCh, p.PeerAddr, pkt)
	return nil
}

// RouteTargetKey returns "EtherTalkPeer|device name|peer address".
func (p *EtherTalkPeer) RouteTargetKey() string {
	return fmt.Sprintf("EtherTalkPeer|%s|%d.%d", p.Port.device, p.PeerAddr.Network, p.PeerAddr.Node)
}

// Class returns TargetClassAppleTalkPeer.
func (p *EtherTalkPeer) Class() TargetClass { return TargetClassAppleTalkPeer }

func (p *EtherTalkPeer) String() string {
	return fmt.Sprintf("%d.%d via %s", p.PeerAddr.Network, p.PeerAddr.Node, p.Port.device)
}
