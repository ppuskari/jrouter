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
	"bytes"
	"context"
	"log"
	"net"
	"sync"
	"time"

	"gitea.drjosh.dev/josh/jrouter/aurp"
	"github.com/sfiera/multitalk/pkg/ddp"
)

const (
	// TODO: check these parameters
	lastHeardFromTimer = 90 * time.Second
	tickleRetryLimit   = 10
	sendRetryTimer     = 10 * time.Second
	sendRetryLimit     = 5
	reconnectTimer     = 10 * time.Minute
	updateTimer        = 10 * time.Second
)

type ReceiverState int

const (
	ReceiverUnconnected ReceiverState = iota
	ReceiverConnected
	ReceiverWaitForOpenRsp
	ReceiverWaitForRIRsp
	ReceiverWaitForTickleAck
)

func (rs ReceiverState) String() string {
	switch rs {
	case ReceiverUnconnected:
		return "unconnected"
	case ReceiverConnected:
		return "connected"
	case ReceiverWaitForOpenRsp:
		return "waiting for Open-Rsp"
	case ReceiverWaitForRIRsp:
		return "waiting for RI-Rsp"
	case ReceiverWaitForTickleAck:
		return "waiting for Tickle-Ack"
	default:
		return "unknown"
	}
}

type SenderState int

const (
	SenderUnconnected SenderState = iota
	SenderConnected
	SenderWaitForRIRspAck
	SenderWaitForRIUpdAck
	SenderWaitForRDAck
)

func (ss SenderState) String() string {
	switch ss {
	case SenderUnconnected:
		return "unconnected"
	case SenderConnected:
		return "connected"
	case SenderWaitForRIRspAck:
		return "waiting for RI-Ack for RI-Rsp"
	case SenderWaitForRIUpdAck:
		return "waiting for RI-Ack for RI-Upd"
	case SenderWaitForRDAck:
		return "waiting for RI-Ack for RD"
	default:
		return "unknown"
	}
}

// AURPPeer handles the peering with a peer AURP router.
type AURPPeer struct {
	// Whole router config.
	Config *Config

	// AURP-Tr state for producing packets.
	Transport *aurp.Transport

	// Connection to reply to packets on.
	UDPConn *net.UDPConn

	// The string that appeared in the config file / peer list file (with a
	// ":387" appended as necessary).
	// May be empty if this peer was not configured (it connected to us).
	ConfiguredAddr string

	// The resolved address of the peer.
	RemoteAddr *net.UDPAddr

	// Incoming packet channel.
	ReceiveCh chan aurp.Packet

	// Routing table (the peer will add/remove/update routes)
	RoutingTable *RoutingTable

	// Zone table (the peer will add/remove/update zones)
	ZoneTable *ZoneTable

	mu     sync.RWMutex
	rstate ReceiverState
	sstate SenderState
}

func (p *AURPPeer) ReceiverState() ReceiverState {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.rstate
}

func (p *AURPPeer) SenderState() SenderState {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.sstate
}

func (p *AURPPeer) setRState(rstate ReceiverState) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.rstate = rstate
}

func (p *AURPPeer) setSState(sstate SenderState) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.sstate = sstate
}

func (p *AURPPeer) disconnect() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.rstate = ReceiverUnconnected
	p.sstate = SenderUnconnected
}

// Send encodes and sends pkt to the remote host.
func (p *AURPPeer) Send(pkt aurp.Packet) (int, error) {
	var b bytes.Buffer
	if _, err := pkt.WriteTo(&b); err != nil {
		return 0, err
	}
	log.Printf("AURP Peer: Sending %T (len %d) to %v", pkt, b.Len(), p.RemoteAddr)
	return p.UDPConn.WriteToUDP(b.Bytes(), p.RemoteAddr)
}

func (p *AURPPeer) Handle(ctx context.Context) error {
	rticker := time.NewTicker(1 * time.Second)
	defer rticker.Stop()
	sticker := time.NewTicker(1 * time.Second)
	defer sticker.Stop()

	lastReconnect := time.Now()
	lastHeardFrom := time.Now()
	lastSend := time.Now() // TODO: clarify use of lastSend / sendRetries
	lastUpdate := time.Now()
	sendRetries := 0

	var lastRISent aurp.Packet

	p.disconnect()

	// Write an Open-Req packet
	if _, err := p.Send(p.Transport.NewOpenReqPacket(nil)); err != nil {
		log.Printf("AURP Peer: Couldn't send Open-Req packet: %v", err)
		return err
	}

	p.setRState(ReceiverWaitForOpenRsp)

	for {
		select {
		case <-ctx.Done():
			if p.sstate == SenderUnconnected {
				// Return immediately
				return ctx.Err()
			}
			// Send a best-effort Router Down before returning
			lastRISent = p.Transport.NewRDPacket(aurp.ErrCodeNormalClose)
			if _, err := p.Send(lastRISent); err != nil {
				log.Printf("Couldn't send RD packet: %v", err)
			}
			return ctx.Err()

		case <-rticker.C:
			switch p.rstate {
			case ReceiverWaitForOpenRsp:
				if time.Since(lastSend) <= sendRetryTimer {
					break
				}
				if sendRetries >= sendRetryLimit {
					log.Printf("AURP Peer: Send retry limit reached while waiting for Open-Rsp, closing connection")
					p.setRState(ReceiverUnconnected)
					break
				}

				// Send another Open-Req
				sendRetries++
				lastSend = time.Now()
				if _, err := p.Send(p.Transport.NewOpenReqPacket(nil)); err != nil {
					log.Printf("AURP Peer: Couldn't send Open-Req packet: %v", err)
					return err
				}

			case ReceiverConnected:
				// Check LHFT, send tickle?
				if time.Since(lastHeardFrom) <= lastHeardFromTimer {
					break
				}
				if _, err := p.Send(p.Transport.NewTicklePacket()); err != nil {
					log.Printf("AURP Peer: Couldn't send Tickle: %v", err)
					return err
				}
				p.setRState(ReceiverWaitForTickleAck)
				sendRetries = 0
				lastSend = time.Now()

			case ReceiverWaitForTickleAck:
				if time.Since(lastSend) <= sendRetryTimer {
					break
				}
				if sendRetries >= tickleRetryLimit {
					log.Printf("AURP Peer: Send retry limit reached while waiting for Tickle-Ack, closing connection")
					p.setRState(ReceiverUnconnected)
					p.RoutingTable.DeleteAURPPeer(p)
					break
				}

				sendRetries++
				lastSend = time.Now()
				if _, err := p.Send(p.Transport.NewTicklePacket()); err != nil {
					log.Printf("AURP Peer: Couldn't send Tickle: %v", err)
					return err
				}
				// still in Wait For Tickle-Ack

			case ReceiverWaitForRIRsp:
				if time.Since(lastSend) <= sendRetryTimer {
					break
				}
				if sendRetries >= sendRetryLimit {
					log.Printf("AURP Peer: Send retry limit reached while waiting for RI-Rsp, closing connection")
					p.setRState(ReceiverUnconnected)
					p.RoutingTable.DeleteAURPPeer(p)
					break
				}

				// RI-Req is stateless, so we don't need to cache the one we
				// sent earlier just to send it again
				sendRetries++
				if _, err := p.Send(p.Transport.NewRIReqPacket()); err != nil {
					log.Printf("AURP Peer: Couldn't send RI-Req packet: %v", err)
					return err
				}
				// still in Wait For RI-Rsp

			case ReceiverUnconnected:
				// Data receiver is unconnected. If data sender is connected,
				// send a null RI-Upd to check if the sender is also unconnected
				if p.sstate == SenderConnected && time.Since(lastSend) > sendRetryTimer {
					if sendRetries >= sendRetryLimit {
						log.Printf("AURP Peer: Send retry limit reached while probing sender connect, closing connection")
					}
					sendRetries++
					lastSend = time.Now()
					aurp.Inc(&p.Transport.LocalSeq)
					events := aurp.EventTuples{{
						EventCode: aurp.EventCodeNull,
					}}
					lastRISent = p.Transport.NewRIUpdPacket(events)
					if _, err := p.Send(lastRISent); err != nil {
						log.Printf("AURP Peer: Couldn't send RI-Upd packet: %v", err)
						return err
					}
					p.setSState(SenderWaitForRIUpdAck)
				}

				if p.ConfiguredAddr != "" {
					// Periodically try to reconnect, if this peer is in the config file
					if time.Since(lastReconnect) <= reconnectTimer {
						break
					}

					// In case it's a DNS name, re-resolve it before reconnecting
					raddr, err := net.ResolveUDPAddr("udp4", p.ConfiguredAddr)
					if err != nil {
						log.Printf("couldn't resolve UDP address, skipping: %v", err)
						break
					}
					log.Printf("AURP Peer: resolved %q to %v", p.ConfiguredAddr, raddr)
					p.RemoteAddr = raddr

					lastReconnect = time.Now()
					sendRetries = 0
					lastSend = time.Now()
					if _, err := p.Send(p.Transport.NewOpenReqPacket(nil)); err != nil {
						log.Printf("AURP Peer: Couldn't send Open-Req packet: %v", err)
						return err
					}
					p.setRState(ReceiverWaitForOpenRsp)
				}
			}

		case <-sticker.C:
			switch p.sstate {
			case SenderUnconnected:
				// Do nothing

			case SenderConnected:
				if time.Since(lastUpdate) <= updateTimer {
					break
				}
				// TODO: is there a routing update to send?

			case SenderWaitForRIRspAck, SenderWaitForRIUpdAck:
				if time.Since(lastSend) <= sendRetryTimer {
					break
				}
				if lastRISent == nil {
					log.Print("AURP Peer: sender retry: lastRISent = nil?")
					continue
				}
				if sendRetries >= sendRetryLimit {
					log.Printf("AURP Peer: Send retry limit reached, closing connection")
					p.setSState(SenderUnconnected)
					continue
				}
				sendRetries++
				lastSend = time.Now()
				if _, err := p.Send(lastRISent); err != nil {
					log.Printf("AURP Peer: Couldn't re-send %T: %v", lastRISent, err)
					return err
				}

			case SenderWaitForRDAck:
				if time.Since(lastSend) <= sendRetryTimer {
					break
				}
				p.setSState(SenderUnconnected)
			}

		case pkt := <-p.ReceiveCh:
			lastHeardFrom = time.Now()

			switch pkt := pkt.(type) {
			case *aurp.OpenReqPacket:
				if p.sstate != SenderUnconnected {
					log.Printf("AURP Peer: Open-Req received but sender state is not unconnected (was %v)", p.sstate)
				}

				// The peer tells us their connection ID in Open-Req.
				p.Transport.RemoteConnID = pkt.ConnectionID

				// Formulate a response.
				var orsp *aurp.OpenRspPacket
				switch {
				case pkt.Version != 1:
					// Respond with Open-Rsp with unknown version error.
					orsp = p.Transport.NewOpenRspPacket(0, int16(aurp.ErrCodeInvalidVersion), nil)

				case len(pkt.Options) > 0:
					// Options? OPTIONS? We don't accept no stinkin' _options_
					orsp = p.Transport.NewOpenRspPacket(0, int16(aurp.ErrCodeOptionNegotiation), nil)

				default:
					// Accept it I guess.
					orsp = p.Transport.NewOpenRspPacket(0, 1, nil)
				}

				if _, err := p.Send(orsp); err != nil {
					log.Printf("AURP Peer: Couldn't send Open-Rsp: %v", err)
					return err
				}
				if orsp.RateOrErrCode >= 0 {
					p.setSState(SenderConnected)
				}

				// If receiver is unconnected, commence connecting
				if p.rstate == ReceiverUnconnected {
					lastSend = time.Now()
					sendRetries = 0
					if _, err := p.Send(p.Transport.NewOpenReqPacket(nil)); err != nil {
						log.Printf("AURP Peer: Couldn't send Open-Req packet: %v", err)
						return err
					}
					p.setRState(ReceiverWaitForOpenRsp)
				}

			case *aurp.OpenRspPacket:
				if p.rstate != ReceiverWaitForOpenRsp {
					log.Printf("AURP Peer: Received Open-Rsp but was not waiting for one (receiver state was %v)", p.rstate)
				}
				if pkt.RateOrErrCode < 0 {
					// It's an error code.
					log.Printf("AURP Peer: Open-Rsp error code from peer %v: %d", p.RemoteAddr.IP, pkt.RateOrErrCode)
					p.setRState(ReceiverUnconnected)
					break
				}
				//log.Printf("AURP Peer: Data receiver is connected!")
				p.setRState(ReceiverConnected)

				// Send an RI-Req
				sendRetries = 0
				if _, err := p.Send(p.Transport.NewRIReqPacket()); err != nil {
					log.Printf("AURP Peer: Couldn't send RI-Req packet: %v", err)
					return err
				}
				p.setRState(ReceiverWaitForRIRsp)

			case *aurp.RIReqPacket:
				if p.sstate != SenderConnected {
					log.Printf("AURP Peer: Received RI-Req but was not expecting one (sender state was %v)", p.sstate)
				}

				nets := aurp.NetworkTuples{
					{
						Extended:   true,
						RangeStart: p.Config.EtherTalk.NetStart,
						RangeEnd:   p.Config.EtherTalk.NetEnd,
						Distance:   0,
					},
				}
				p.Transport.LocalSeq = 1
				lastRISent = p.Transport.NewRIRspPacket(aurp.RoutingFlagLast, nets)
				if _, err := p.Send(lastRISent); err != nil {
					log.Printf("AURP Peer: Couldn't send RI-Rsp packet: %v", err)
					return err
				}
				p.setSState(SenderWaitForRIRspAck)

			case *aurp.RIRspPacket:
				if p.rstate != ReceiverWaitForRIRsp {
					log.Printf("Received RI-Rsp but was not waiting for one (receiver state was %v)", p.rstate)
				}

				log.Printf("AURP Peer: Learned about these networks: %v", pkt.Networks)

				for _, nt := range pkt.Networks {
					p.RoutingTable.InsertAURPRoute(
						p,
						nt.Extended,
						ddp.Network(nt.RangeStart),
						ddp.Network(nt.RangeEnd),
						nt.Distance+1,
					)
				}

				// TODO: track which networks we don't have zone info for, and
				// only set SZI for those ?
				if _, err := p.Send(p.Transport.NewRIAckPacket(pkt.ConnectionID, pkt.Sequence, aurp.RoutingFlagSendZoneInfo)); err != nil {
					log.Printf("AURP Peer: Couldn't send RI-Ack packet: %v", err)
					return err
				}
				if pkt.Flags&aurp.RoutingFlagLast != 0 {
					// No longer waiting for an RI-Rsp
					p.setRState(ReceiverConnected)
				}

			case *aurp.RIAckPacket:
				switch p.sstate {
				case SenderWaitForRIRspAck:
					// We sent an RI-Rsp, this is the RI-Ack we expected.

				case SenderWaitForRIUpdAck:
					// We sent an RI-Upd, this is the RI-Ack we expected.

				case SenderWaitForRDAck:
					// We sent an RD... Why are we here?
					continue

				default:
					log.Printf("AURP Peer: Received RI-Ack but was not waiting for one (sender state was %v)", p.sstate)
				}

				p.setSState(SenderConnected)
				sendRetries = 0

				// If SZI flag is set, send ZI-Rsp (transaction)
				// TODO: split ZI-Rsp packets similarly to ZIP Replies
				if pkt.Flags&aurp.RoutingFlagSendZoneInfo != 0 {
					zones := map[ddp.Network][]string{
						p.Config.EtherTalk.NetStart: {p.Config.EtherTalk.ZoneName},
					}
					if _, err := p.Send(p.Transport.NewZIRspPacket(zones)); err != nil {
						log.Printf("AURP Peer: Couldn't send ZI-Rsp packet: %v", err)
					}
				}

				// TODO: Continue sending next RI-Rsp (streamed)?

				if p.rstate == ReceiverUnconnected {
					// Receiver is unconnected, but their receiver sent us an
					// RI-Ack for something
					// Try to reconnect?
					lastSend = time.Now()
					sendRetries = 0
					if _, err := p.Send(p.Transport.NewOpenReqPacket(nil)); err != nil {
						log.Printf("AURP Peer: Couldn't send Open-Req packet: %v", err)
						return err
					}
					p.setRState(ReceiverWaitForOpenRsp)
				}

			case *aurp.RIUpdPacket:

				var ackFlag aurp.RoutingFlag

				for _, et := range pkt.Events {
					log.Printf("AURP Peer: RI-Upd event %v", et)
					switch et.EventCode {
					case aurp.EventCodeNull:
						// Do nothing except respond with RI-Ack

					case aurp.EventCodeNA:
						if err := p.RoutingTable.InsertAURPRoute(
							p,
							et.Extended,
							et.RangeStart,
							et.RangeEnd,
							et.Distance+1,
						); err != nil {
							log.Printf("AURP Peer: couldn't insert route: %v", err)
						}
						ackFlag = aurp.RoutingFlagSendZoneInfo

					case aurp.EventCodeND:
						p.RoutingTable.DeleteAURPPeerNetwork(p, et.RangeStart)

					case aurp.EventCodeNDC:
						p.RoutingTable.UpdateAURPRouteDistance(p, et.RangeStart, et.Distance+1)

					case aurp.EventCodeNRC:
						// "An exterior router sends a Network Route Change
						// (NRC) event if the path to an exported network
						// through its local internet changes to a path through
						// a tunneling port, causing split-horizoned processing
						// to eliminate that network’s routing information."
						p.RoutingTable.DeleteAURPPeerNetwork(p, et.RangeStart)

					case aurp.EventCodeZC:
						// "This event is reserved for future use."
					}
				}

				if _, err := p.Send(p.Transport.NewRIAckPacket(pkt.ConnectionID, pkt.Sequence, ackFlag)); err != nil {
					log.Printf("AURP Peer: Couldn't send RI-Ack: %v", err)
					return err
				}

			case *aurp.RDPacket:
				if p.rstate == ReceiverUnconnected || p.rstate == ReceiverWaitForOpenRsp {
					log.Printf("AURP Peer: Received RD but was not expecting one (receiver state was %v)", p.rstate)
				}

				log.Printf("AURP Peer: Router Down: error code %d %s", pkt.ErrorCode, pkt.ErrorCode)
				p.RoutingTable.DeleteAURPPeer(p)

				// Respond with RI-Ack
				if _, err := p.Send(p.Transport.NewRIAckPacket(pkt.ConnectionID, pkt.Sequence, 0)); err != nil {
					log.Printf("AURP Peer: Couldn't send RI-Ack: %v", err)
					return err
				}
				// Connections closed
				p.disconnect()

			case *aurp.ZIReqPacket:
				// TODO: split ZI-Rsp packets similarly to ZIP Replies
				zones := p.ZoneTable.Query(pkt.Networks)
				if _, err := p.Send(p.Transport.NewZIRspPacket(zones)); err != nil {
					log.Printf("AURP Peer: Couldn't send ZI-Rsp packet: %v", err)
					return err
				}

			case *aurp.ZIRspPacket:
				log.Printf("AURP Peer: Learned about these zones: %v", pkt.Zones)
				for _, zt := range pkt.Zones {
					p.ZoneTable.Upsert(ddp.Network(zt.Network), zt.Name, false)
				}

			case *aurp.GDZLReqPacket:
				if _, err := p.Send(p.Transport.NewGDZLRspPacket(-1, nil)); err != nil {
					log.Printf("AURP Peer: Couldn't send GDZL-Rsp packet: %v", err)
					return err
				}

			case *aurp.GDZLRspPacket:
				log.Printf("AURP Peer: Received a GDZL-Rsp, but I wouldn't have sent a GDZL-Req - that's weird")

			case *aurp.GZNReqPacket:
				if _, err := p.Send(p.Transport.NewGZNRspPacket(pkt.ZoneName, false, nil)); err != nil {
					log.Printf("AURP Peer: Couldn't send GZN-Rsp packet: %v", err)
					return err
				}

			case *aurp.GZNRspPacket:
				log.Printf("AURP Peer: Received a GZN-Rsp, but I wouldn't have sent a GZN-Req - that's weird")

			case *aurp.TicklePacket:
				// Immediately respond with Tickle-Ack
				if _, err := p.Send(p.Transport.NewTickleAckPacket()); err != nil {
					log.Printf("AURP Peer: Couldn't send Tickle-Ack: %v", err)
					return err
				}

			case *aurp.TickleAckPacket:
				if p.rstate != ReceiverWaitForTickleAck {
					log.Printf("AURP Peer: Received Tickle-Ack but was not waiting for one (receiver state was %v)", p.rstate)
				}
				p.setRState(ReceiverConnected)
			}
		}
	}
}
