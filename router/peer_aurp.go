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

	"drjosh.dev/jrouter/aurp"
	"github.com/prometheus/client_golang/prometheus"
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
	// AURP-Tr state for producing packets.
	Transport *aurp.Transport

	// Connection to reply to packets on.
	UDPConn *net.UDPConn

	// The string that appeared in the config file / peer list file (with a
	// ":387" appended as necessary).
	// May be empty if this peer was not configured (it connected to us).
	ConfiguredAddr string

	// The resolved address of the peer.
	// NOTE: The port is ignored and replaced with 387.
	RemoteAddr net.IP

	// Incoming packet channel.
	ReceiveCh chan aurp.Packet

	// Route table (the peer will add/remove/update routes and zones)
	RouteTable *RouteTable

	// Event tuples yet to be sent to this peer in an RI-Upd.
	pendingEventsMu sync.Mutex
	pendingEvents   aurp.EventTuples

	// The internal states below are only set within the Handle loop, but can
	// be read concurrently from outside.
	mu            sync.RWMutex
	rstate        ReceiverState
	sstate        SenderState
	lastReconnect time.Time
	lastHeardFrom time.Time
	lastSend      time.Time // TODO: clarify use of lastSend / sendRetries
	lastUpdate    time.Time
	sendRetries   int
}

func NewAURPPeer(routes *RouteTable, udpConn *net.UDPConn, peerAddr string, raddr net.IP, localDI, remoteDI aurp.DomainIdentifier, connID uint16) *AURPPeer {
	if remoteDI == nil {
		remoteDI = aurp.IPDomainIdentifier(raddr)
	}
	return &AURPPeer{
		Transport: &aurp.Transport{
			LocalDI:     localDI,
			RemoteDI:    remoteDI,
			LocalConnID: connID,
		},
		UDPConn:        udpConn,
		ConfiguredAddr: peerAddr,
		// TODO: The port is assumed to be 387 - sensible?
		RemoteAddr: raddr,
		ReceiveCh:  make(chan aurp.Packet, 1024),
		RouteTable: routes,
	}
}

func (p *AURPPeer) addPendingEvent(ec aurp.EventCode, route *Route) {
	// Don't advertise routes to AURP peers to other AURP peers
	if _, isAURP := route.Target.(*AURPPeer); isAURP {
		return
	}
	et := aurp.EventTuple{
		EventCode:  ec,
		Extended:   route.Extended,
		RangeStart: route.NetStart,
		Distance:   route.Distance,
		RangeEnd:   route.NetEnd,
	}
	switch ec {
	case aurp.EventCodeND, aurp.EventCodeNRC:
		et.Distance = 0 // "The distance field does not apply to ND or NRC event tuples and should be set to 0."
	}
	p.pendingEventsMu.Lock()
	defer p.pendingEventsMu.Unlock()
	p.pendingEvents = append(p.pendingEvents, et)
}

func (p *AURPPeer) RouteAdded(route *Route) {
	p.addPendingEvent(aurp.EventCodeNA, route)
}

func (p *AURPPeer) RouteDeleted(route *Route) {
	p.addPendingEvent(aurp.EventCodeND, route)
}

func (p *AURPPeer) RouteDistanceChanged(route *Route) {
	p.addPendingEvent(aurp.EventCodeNDC, route)
}

func (p *AURPPeer) RouteForwarderChanged(route *Route) {
	p.addPendingEvent(aurp.EventCodeNRC, route)
}

// Forward encapsulates the DDP packet in an AURP AppleTalkPacket and sends it
// to the remote peer router.
func (p *AURPPeer) Forward(_ context.Context, ddpkt *ddp.ExtPacket) error {
	outPkt, err := ddp.ExtMarshal(*ddpkt)
	if err != nil {
		return err
	}
	_, err = p.send(p.Transport.NewAppleTalkPacket(outPkt))
	return err
}

// RouteTargetKey returns "AURPPeer|peer's IP address".
func (p *AURPPeer) RouteTargetKey() string {
	return "AURPPeer|" + p.RemoteAddr.String()
}

func (p *AURPPeer) String() string {
	return p.RemoteAddr.String()
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

func (p *AURPPeer) LastReconnectAgo() string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return ago(p.lastReconnect)
}

func (p *AURPPeer) LastHeardFromAgo() string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return ago(p.lastHeardFrom)
}

func (p *AURPPeer) LastSendAgo() string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return ago(p.lastSend)
}

func (p *AURPPeer) LastUpdateAgo() string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return ago(p.lastUpdate)
}

func (p *AURPPeer) SendRetries() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.sendRetries
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

func (p *AURPPeer) incSendRetries() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.sendRetries++
}

func (p *AURPPeer) resetSendRetries() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.sendRetries = 0
}

func (p *AURPPeer) bumpLastHeardFrom() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.lastHeardFrom = time.Now()
}

func (p *AURPPeer) bumpLastReconnect() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.lastReconnect = time.Now()
}

func (p *AURPPeer) bumpLastSend() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.lastSend = time.Now()
}

func (p *AURPPeer) bumpLastUpdate() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.lastUpdate = time.Now()
}

func (p *AURPPeer) disconnect() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.rstate = ReceiverUnconnected
	p.sstate = SenderUnconnected
}

// send encodes and sends pkt to the remote host.
func (p *AURPPeer) send(pkt aurp.Packet) (int, error) {
	var b bytes.Buffer
	if _, err := pkt.WriteTo(&b); err != nil {
		return 0, err
	}

	promLabels := prometheus.Labels{"peer": p.RemoteAddr.String()}
	aurpPacketsOutCounter.With(promLabels).Inc()
	aurpBytesOutCounter.With(promLabels).Add(float64(b.Len()))

	log.Printf("AURP Peer: Sending %T (len %d) to %v", pkt, b.Len(), p.RemoteAddr)
	return p.UDPConn.WriteToUDP(b.Bytes(), &net.UDPAddr{IP: p.RemoteAddr, Port: 387})
}

func (p *AURPPeer) Handle(ctx context.Context) error {
	// Stop listening to events if the goroutine exits
	defer p.RouteTable.RemoveObserver(p)

	rticker := time.NewTicker(1 * time.Second)
	defer rticker.Stop()
	sticker := time.NewTicker(1 * time.Second)
	defer sticker.Stop()

	p.mu.Lock()
	p.lastReconnect = time.Now()
	p.lastHeardFrom = time.Now()
	p.lastSend = time.Now() // TODO: clarify use of lastSend / sendRetries
	p.lastUpdate = time.Now()
	p.sendRetries = 0
	p.mu.Unlock()

	var lastRISent aurp.Packet

	p.disconnect()

	// Write an Open-Req packet
	if _, err := p.send(p.Transport.NewOpenReqPacket(nil)); err != nil {
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
			if _, err := p.send(lastRISent); err != nil {
				log.Printf("Couldn't send RD packet: %v", err)
			}
			return ctx.Err()

		case <-rticker.C:
			switch p.rstate {
			case ReceiverWaitForOpenRsp:
				if time.Since(p.lastSend) <= sendRetryTimer {
					break
				}
				if p.sendRetries >= sendRetryLimit {
					log.Printf("AURP Peer: Send retry limit reached while waiting for Open-Rsp, closing connection")
					p.setRState(ReceiverUnconnected)
					break
				}

				// Send another Open-Req
				p.incSendRetries()
				p.bumpLastSend()
				if _, err := p.send(p.Transport.NewOpenReqPacket(nil)); err != nil {
					log.Printf("AURP Peer: Couldn't send Open-Req packet: %v", err)
					return err
				}

			case ReceiverConnected:
				// Check LHFT, send tickle?
				if time.Since(p.lastHeardFrom) <= lastHeardFromTimer {
					break
				}
				if _, err := p.send(p.Transport.NewTicklePacket()); err != nil {
					log.Printf("AURP Peer: Couldn't send Tickle: %v", err)
					return err
				}
				p.setRState(ReceiverWaitForTickleAck)
				p.resetSendRetries()
				p.bumpLastSend()

			case ReceiverWaitForTickleAck:
				if time.Since(p.lastSend) <= sendRetryTimer {
					break
				}
				if p.sendRetries >= tickleRetryLimit {
					log.Printf("AURP Peer: Send retry limit reached while waiting for Tickle-Ack, closing connection")
					p.setRState(ReceiverUnconnected)
					p.RouteTable.DeleteTarget(p)
					break
				}

				p.incSendRetries()
				p.bumpLastSend()
				if _, err := p.send(p.Transport.NewTicklePacket()); err != nil {
					log.Printf("AURP Peer: Couldn't send Tickle: %v", err)
					return err
				}
				// still in Wait For Tickle-Ack

			case ReceiverWaitForRIRsp:
				if time.Since(p.lastSend) <= sendRetryTimer {
					break
				}
				if p.sendRetries >= sendRetryLimit {
					log.Printf("AURP Peer: Send retry limit reached while waiting for RI-Rsp, closing connection")
					p.setRState(ReceiverUnconnected)
					p.RouteTable.DeleteTarget(p)
					break
				}

				// RI-Req is stateless, so we don't need to cache the one we
				// sent earlier just to send it again
				p.incSendRetries()
				p.bumpLastSend()
				if _, err := p.send(p.Transport.NewRIReqPacket()); err != nil {
					log.Printf("AURP Peer: Couldn't send RI-Req packet: %v", err)
					return err
				}
				// still in Wait For RI-Rsp

			case ReceiverUnconnected:
				// Data receiver is unconnected. If data sender is connected,
				// send a null RI-Upd to check if the sender is also unconnected
				if p.sstate == SenderConnected && time.Since(p.lastSend) > sendRetryTimer {
					if p.sendRetries >= sendRetryLimit {
						log.Printf("AURP Peer: Send retry limit reached while probing sender connect, closing connection")
					}
					p.incSendRetries()
					p.bumpLastSend()
					aurp.Inc(&p.Transport.LocalSeq)
					events := aurp.EventTuples{{
						EventCode: aurp.EventCodeNull,
					}}
					lastRISent = p.Transport.NewRIUpdPacket(events)
					if _, err := p.send(lastRISent); err != nil {
						log.Printf("AURP Peer: Couldn't send RI-Upd packet: %v", err)
						return err
					}
					p.setSState(SenderWaitForRIUpdAck)
				}

				if p.ConfiguredAddr != "" {
					// Periodically try to reconnect, if this peer is in the config file
					if time.Since(p.lastReconnect) <= reconnectTimer {
						break
					}

					// In case it's a DNS name, re-resolve it before reconnecting
					raddr, err := net.ResolveUDPAddr("udp4", p.ConfiguredAddr)
					if err != nil {
						log.Printf("couldn't resolve UDP address, skipping: %v", err)
						break
					}
					log.Printf("AURP Peer: resolved %q to %v", p.ConfiguredAddr, raddr)
					p.RemoteAddr = raddr.IP

					p.bumpLastReconnect()
					p.resetSendRetries()
					p.bumpLastSend()
					if _, err := p.send(p.Transport.NewOpenReqPacket(nil)); err != nil {
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
				if time.Since(p.lastUpdate) <= updateTimer {
					break
				}

				// Are there routing updates to send?
				p.pendingEventsMu.Lock()
				if len(p.pendingEvents) == 0 {
					p.pendingEventsMu.Unlock()
					break
				}
				// Yes - swap the slices, release the mutex, then send them
				pending := p.pendingEvents
				p.pendingEvents = make(aurp.EventTuples, 0, cap(pending))
				p.pendingEventsMu.Unlock()

				// TODO: eliminate events that cancel out (e.g. NA then ND)
				// TODO: split pending events to fit within a packet

				p.bumpLastUpdate()
				aurp.Inc(&p.Transport.LocalSeq)
				lastRISent = p.Transport.NewRIUpdPacket(pending)
				if _, err := p.send(lastRISent); err != nil {
					log.Printf("AURP Peer: Couldn't send RI-Upd packet: %v", err)
					return err
				}
				p.setSState(SenderWaitForRIUpdAck)

			case SenderWaitForRIRspAck, SenderWaitForRIUpdAck:
				if time.Since(p.lastSend) <= sendRetryTimer {
					break
				}
				if lastRISent == nil {
					log.Print("AURP Peer: sender retry: lastRISent = nil?")
					continue
				}
				if p.sendRetries >= sendRetryLimit {
					log.Printf("AURP Peer: Send retry limit reached, closing connection")
					p.setSState(SenderUnconnected)
					p.RouteTable.RemoveObserver(p)
					continue
				}
				p.incSendRetries()
				p.bumpLastSend()
				if _, err := p.send(lastRISent); err != nil {
					log.Printf("AURP Peer: Couldn't re-send %T: %v", lastRISent, err)
					return err
				}

			case SenderWaitForRDAck:
				if time.Since(p.lastSend) <= sendRetryTimer {
					break
				}
				p.setSState(SenderUnconnected)
				p.RouteTable.RemoveObserver(p)
			}

		case pkt := <-p.ReceiveCh:
			p.bumpLastHeardFrom()

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

				if _, err := p.send(orsp); err != nil {
					log.Printf("AURP Peer: Couldn't send Open-Rsp: %v", err)
					return err
				}
				if orsp.RateOrErrCode >= 0 {
					// Data sender is successfully in connected state
					p.setSState(SenderConnected)
					p.RouteTable.AddObserver(p)
				}

				// If receiver is unconnected, commence connecting
				if p.rstate == ReceiverUnconnected {
					p.resetSendRetries()
					p.bumpLastSend()
					if _, err := p.send(p.Transport.NewOpenReqPacket(nil)); err != nil {
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
					log.Printf("AURP Peer: Open-Rsp error code from peer %v: %d", p.RemoteAddr, pkt.RateOrErrCode)
					p.setRState(ReceiverUnconnected)
					break
				}
				//log.Printf("AURP Peer: Data receiver is connected!")
				p.setRState(ReceiverConnected)

				// Send an RI-Req
				p.resetSendRetries()
				if _, err := p.send(p.Transport.NewRIReqPacket()); err != nil {
					log.Printf("AURP Peer: Couldn't send RI-Req packet: %v", err)
					return err
				}
				p.setRState(ReceiverWaitForRIRsp)

			case *aurp.RIReqPacket:
				if p.sstate != SenderConnected {
					log.Printf("AURP Peer: Received RI-Req but was not expecting one (sender state was %v)", p.sstate)
				}

				// TODO: Load ExtraAdvertisedZones and HiddenZones

				// Build up the slice of network tuples
				var nets aurp.NetworkTuples

				// TODO: filter these by HiddenZones
				for r := range p.RouteTable.ValidRoutesForClass(TargetClassDirect) {
					nets = append(nets, aurp.NetworkTuple{
						Extended:   r.Extended,
						RangeStart: r.NetStart,
						RangeEnd:   r.NetEnd,
						Distance:   r.Distance,
					})
				}
				// TODO: filter these by ExtraAdvertisedZones and HiddenZones
				for r := range p.RouteTable.ValidRoutesForClass(TargetClassAppleTalkPeer) {
					nets = append(nets, aurp.NetworkTuple{
						Extended:   r.Extended,
						RangeStart: r.NetStart,
						RangeEnd:   r.NetEnd,
						Distance:   r.Distance,
					})
				}
				p.Transport.LocalSeq = 1
				// TODO: Split tuples across multiple packets as required
				lastRISent = p.Transport.NewRIRspPacket(aurp.RoutingFlagLast, nets)
				if _, err := p.send(lastRISent); err != nil {
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
					p.RouteTable.UpsertRoute(
						p,
						nt.Extended,
						ddp.Network(nt.RangeStart),
						ddp.Network(nt.RangeEnd),
						nt.Distance+1,
					)
				}

				// TODO: track which networks we don't have zone info for, and
				// only set SZI for those ?
				if _, err := p.send(p.Transport.NewRIAckPacket(pkt.ConnectionID, pkt.Sequence, aurp.RoutingFlagSendZoneInfo)); err != nil {
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
				p.resetSendRetries()
				p.RouteTable.AddObserver(p)

				// If SZI flag is set, send ZI-Rsp (transaction)
				if pkt.Flags&aurp.RoutingFlagSendZoneInfo != 0 {
					// Inspect last routing info packet sent to determine
					// networks to gather names for
					var nets []ddp.Network
					switch last := lastRISent.(type) {
					case *aurp.RIRspPacket:
						for _, nt := range last.Networks {
							nets = append(nets, nt.RangeStart)
						}

					case *aurp.RIUpdPacket:
						for _, et := range last.Events {
							// Only networks that were added
							if et.EventCode != aurp.EventCodeNA {
								continue
							}
							nets = append(nets, et.RangeStart)
						}

					}
					zones := p.RouteTable.ZonesForNetworks(nets)
					// TODO: split ZI-Rsp packets similarly to ZIP Replies
					if _, err := p.send(p.Transport.NewZIRspPacket(zones)); err != nil {
						log.Printf("AURP Peer: Couldn't send ZI-Rsp packet: %v", err)
					}
				}

				// TODO: Continue sending next RI-Rsp (streamed)?

				if p.rstate == ReceiverUnconnected {
					// Receiver is unconnected, but their receiver sent us an
					// RI-Ack for something
					// Try to reconnect?
					p.resetSendRetries()
					p.bumpLastSend()
					if _, err := p.send(p.Transport.NewOpenReqPacket(nil)); err != nil {
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
						if _, err := p.RouteTable.UpsertRoute(
							p,
							et.Extended,
							et.RangeStart,
							et.RangeEnd,
							et.Distance+1,
						); err != nil {
							log.Printf("AURP Peer: NA event: couldn't insert route: %v", err)
						}
						ackFlag = aurp.RoutingFlagSendZoneInfo

					case aurp.EventCodeND:
						if err := p.RouteTable.DeleteRoute(p, et.RangeStart); err != nil {
							log.Printf("AURP Peer: ND event: couldn't delete route: %v", err)
						}

					case aurp.EventCodeNDC:
						if err := p.RouteTable.UpdateRoute(p, et.RangeStart, et.Distance+1); err != nil {
							log.Printf("AURP Peer: NDC event: couldn't update route: %v", err)
						}

					case aurp.EventCodeNRC:
						// "An exterior router sends a Network Route Change
						// (NRC) event if the path to an exported network
						// through its local internet changes to a path through
						// a tunneling port, causing split-horizoned processing
						// to eliminate that network's routing information."
						if err := p.RouteTable.DeleteRoute(p, et.RangeStart); err != nil {
							log.Printf("AURP Peer: NRC event: couldn't delete route: %v", err)
						}
					case aurp.EventCodeZC:
						// "This event is reserved for future use."
					}
				}

				if _, err := p.send(p.Transport.NewRIAckPacket(pkt.ConnectionID, pkt.Sequence, ackFlag)); err != nil {
					log.Printf("AURP Peer: Couldn't send RI-Ack: %v", err)
					return err
				}

			case *aurp.RDPacket:
				if p.rstate == ReceiverUnconnected || p.rstate == ReceiverWaitForOpenRsp {
					log.Printf("AURP Peer: Received RD but was not expecting one (receiver state was %v)", p.rstate)
				}

				log.Printf("AURP Peer: Router Down: error code %d %s", pkt.ErrorCode, pkt.ErrorCode)
				p.RouteTable.DeleteTarget(p)

				// Respond with RI-Ack
				if _, err := p.send(p.Transport.NewRIAckPacket(pkt.ConnectionID, pkt.Sequence, 0)); err != nil {
					log.Printf("AURP Peer: Couldn't send RI-Ack: %v", err)
					return err
				}
				// Connections closed
				p.disconnect()

			case *aurp.ZIReqPacket:
				// TODO: split ZI-Rsp packets similarly to ZIP Replies
				zones := p.RouteTable.ZonesForNetworks(pkt.Networks)
				if _, err := p.send(p.Transport.NewZIRspPacket(zones)); err != nil {
					log.Printf("AURP Peer: Couldn't send ZI-Rsp packet: %v", err)
					return err
				}

			case *aurp.ZIRspPacket:
				log.Printf("AURP Peer: Learned about these zones: %v", pkt.Zones)
				for _, zt := range pkt.Zones {
					p.RouteTable.AddZonesToRoute(p, zt.Network, zt.Name)
				}

			case *aurp.GDZLReqPacket:
				if _, err := p.send(p.Transport.NewGDZLRspPacket(-1, nil)); err != nil {
					log.Printf("AURP Peer: Couldn't send GDZL-Rsp packet: %v", err)
					return err
				}

			case *aurp.GDZLRspPacket:
				log.Printf("AURP Peer: Received a GDZL-Rsp, but I wouldn't have sent a GDZL-Req - that's weird")

			case *aurp.GZNReqPacket:
				if _, err := p.send(p.Transport.NewGZNRspPacket(pkt.ZoneName, false, nil)); err != nil {
					log.Printf("AURP Peer: Couldn't send GZN-Rsp packet: %v", err)
					return err
				}

			case *aurp.GZNRspPacket:
				log.Printf("AURP Peer: Received a GZN-Rsp, but I wouldn't have sent a GZN-Req - that's weird")

			case *aurp.TicklePacket:
				// Immediately respond with Tickle-Ack
				if _, err := p.send(p.Transport.NewTickleAckPacket()); err != nil {
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
