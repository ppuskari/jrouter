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
	"log/slog"
	"net"
	"reflect"
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

// AURPPeer handles the peering with a peer AURP router.
type AURPPeer struct {
	// AURP-Tr state for producing packets.
	Transport *aurp.Transport

	// Connection to reply to packets on.
	UDPConn *net.UDPConn

	// The string that appeared in the config file / peer list file.
	// May be empty if this peer was not configured (it connected to us, with
	// open_peering enabled).
	ConfiguredAddr string

	// The resolved address of the peer.
	// NOTE: The UDP port is always assumed to be 387.
	RemoteAddr net.IP

	// Incoming packet channel.
	ReceiveCh chan aurp.Packet

	// Route table (the peer will add/remove/update routes and zones)
	RouteTable *RouteTable

	// Event tuples yet to be sent to this peer in an RI-Upd.
	pendingEventsMu sync.Mutex
	pendingEvents   aurp.EventTuples

	// The logger.
	logger *slog.Logger

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

func NewAURPPeer(logger *slog.Logger, routes *RouteTable, udpConn *net.UDPConn, peerAddr string, raddr net.IP, localDI, remoteDI aurp.DomainIdentifier, connID uint16) *AURPPeer {
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
		RemoteAddr:     raddr,
		ReceiveCh:      make(chan aurp.Packet, 1024),
		RouteTable:     routes,
		logger:         logger.With("raddr", raddr, "remote-di", remoteDI),
	}
}

func (p *AURPPeer) addPendingEvent(ec aurp.EventCode, route *Route) {
	// Don't save up route updates happening while sender is unconnected
	if p.SenderState() == SenderUnconnected {
		return
	}
	// Don't advertise routes to AURP peers to other AURP peers
	// NRC is effectively an ND where the route is now available by a different
	// AURP peer
	if _, isAURP := route.Target.(*AURPPeer); isAURP && ec != aurp.EventCodeNRC {
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

// NetworkAdded implements RouteTableObserver.
func (p *AURPPeer) NetworkAdded(newBest Route) {
	p.addPendingEvent(aurp.EventCodeNA, &newBest)
}

// NetworkDeleted implements RouteTableObserver.
func (p *AURPPeer) NetworkDeleted(oldBest Route) {
	p.addPendingEvent(aurp.EventCodeND, &oldBest)
}

// BestNetworkChanged implements RouteTableObserver.
func (p *AURPPeer) BestNetworkChanged(oldBest, newBest Route) {
	switch {
	case oldBest.Target.Class() != TargetClassAURPPeer && newBest.Target.Class() == TargetClassAURPPeer:
		// NRC is a fancy variation on ND.
		p.addPendingEvent(aurp.EventCodeNRC, &newBest)

	case oldBest.Distance != newBest.Distance:
		p.addPendingEvent(aurp.EventCodeNDC, &newBest)
	}
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

// Class returns TargetClassAURPPeer.
func (p *AURPPeer) Class() TargetClass { return TargetClassAURPPeer }

func (p *AURPPeer) String() string {
	return p.RemoteAddr.String()
}

// ReceiverState returns the current route-data receiver state.
func (p *AURPPeer) ReceiverState() ReceiverState {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.rstate
}

// SenderState returns the current route-data sender state.
func (p *AURPPeer) SenderState() SenderState {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.sstate
}

// LastReconnectAgo returns a description of how long ago the last reconnect
// was.
func (p *AURPPeer) LastReconnectAgo() string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return ago(p.lastReconnect)
}

// LastReconnectAgo returns a description of how long ago the last packet
// was received from this peer.
func (p *AURPPeer) LastHeardFromAgo() string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return ago(p.lastHeardFrom)
}

// LastSendAgo returns a description of how long ago the last packet
// was sent to this peer.
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
		p.logger.Error("AURP Peer: Couldn't send Open-Req packet", "error", err)
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
				p.logger.Error("Couldn't send RD packet", "error", err)
			}
			return ctx.Err()

		case <-rticker.C:
			switch p.rstate {
			case ReceiverWaitForOpenRsp:
				if time.Since(p.lastSend) <= sendRetryTimer {
					break
				}
				if p.sendRetries >= sendRetryLimit {
					p.logger.Warn("AURP Peer: Send retry limit reached while waiting for Open-Rsp, closing connection")
					p.setRState(ReceiverUnconnected)
					break
				}

				// Send another Open-Req
				p.incSendRetries()
				p.bumpLastSend()
				if _, err := p.send(p.Transport.NewOpenReqPacket(nil)); err != nil {
					p.logger.Error("AURP Peer: Couldn't send Open-Req packet", "error", err)
					return err
				}

			case ReceiverConnected:
				// Check LHFT, send tickle?
				if time.Since(p.lastHeardFrom) <= lastHeardFromTimer {
					break
				}
				if _, err := p.send(p.Transport.NewTicklePacket()); err != nil {
					p.logger.Error("AURP Peer: Couldn't send Tickle", "error", err)
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
					p.logger.Warn("AURP Peer: Send retry limit reached while waiting for Tickle-Ack, closing connection")
					p.setRState(ReceiverUnconnected)
					p.RouteTable.DeleteTarget(p)
					break
				}

				p.incSendRetries()
				p.bumpLastSend()
				if _, err := p.send(p.Transport.NewTicklePacket()); err != nil {
					p.logger.Error("AURP Peer: Couldn't send Tickle", "error", err)
					return err
				}
				// still in Wait For Tickle-Ack

			case ReceiverWaitForRIRsp:
				if time.Since(p.lastSend) <= sendRetryTimer {
					break
				}
				if p.sendRetries >= sendRetryLimit {
					p.logger.Warn("AURP Peer: Send retry limit reached while waiting for RI-Rsp, closing connection")
					p.setRState(ReceiverUnconnected)
					p.RouteTable.DeleteTarget(p)
					break
				}

				// RI-Req is stateless, so we don't need to cache the one we
				// sent earlier just to send it again
				p.incSendRetries()
				p.bumpLastSend()
				if _, err := p.send(p.Transport.NewRIReqPacket()); err != nil {
					p.logger.Error("AURP Peer: Couldn't send RI-Req packet", "error", err)
					return err
				}
				// still in Wait For RI-Rsp

			case ReceiverUnconnected:
				// Data receiver is unconnected. If data sender is connected,
				// send a null RI-Upd to check if the sender is also unconnected
				if p.sstate == SenderConnected && time.Since(p.lastSend) > sendRetryTimer {
					if p.sendRetries >= sendRetryLimit {
						p.logger.Warn("AURP Peer: Send retry limit reached while probing sender connect, closing connection")
					}
					p.incSendRetries()
					p.bumpLastSend()
					aurp.Inc(&p.Transport.LocalSeq)
					events := aurp.EventTuples{{
						EventCode: aurp.EventCodeNull,
					}}
					lastRISent = p.Transport.NewRIUpdPacket(events)
					if _, err := p.send(lastRISent); err != nil {
						p.logger.Error("AURP Peer: Couldn't send RI-Upd packet: %v", "error", err)
						return err
					}
					p.setSState(SenderWaitForRIUpdAck)
				}

				// TODO: lift the retry logic out into main, so that if the IP changes
				// the peersByIP map can be updated easily
				if p.ConfiguredAddr != "" {
					// Periodically try to reconnect, if this peer is in the config file
					if time.Since(p.lastReconnect) <= reconnectTimer {
						break
					}

					// In case it's a DNS name, re-resolve it before reconnecting
					raddr, err := net.ResolveIPAddr("ip4", p.ConfiguredAddr)
					if err != nil {
						p.logger.Warn("Couldn't resolve UDP address, skipping", "configured-addr", p.ConfiguredAddr, "error", err)
						break
					}
					p.logger.Debug("AURP Peer: resolved address", "configured-addr", p.ConfiguredAddr, "raddr", raddr)
					if raddr.IP.To4() == nil {
						p.logger.Warn("Resolved peer address is not an IPv4 address, skipping", "configured-addr", p.ConfiguredAddr, "raddr", raddr)
					}
					p.RemoteAddr = raddr.IP

					p.bumpLastReconnect()
					p.resetSendRetries()
					p.bumpLastSend()
					if _, err := p.send(p.Transport.NewOpenReqPacket(nil)); err != nil {
						p.logger.Error("AURP Peer: Couldn't send Open-Req packet", "error", err)
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
					p.logger.Error("AURP Peer: Couldn't send RI-Upd packet", "error", err)
					return err
				}
				p.setSState(SenderWaitForRIUpdAck)

			case SenderWaitForRIRspAck, SenderWaitForRIUpdAck:
				if time.Since(p.lastSend) <= sendRetryTimer {
					break
				}
				if lastRISent == nil {
					p.logger.Error("AURP Peer: sender retry: lastRISent = nil?")
					continue
				}
				if p.sendRetries >= sendRetryLimit {
					p.logger.Warn("AURP Peer: Send retry limit reached, closing connection")
					p.setSState(SenderUnconnected)
					p.RouteTable.RemoveObserver(p)
					continue
				}
				p.incSendRetries()
				p.bumpLastSend()
				if _, err := p.send(lastRISent); err != nil {
					p.logger.Error("AURP Peer: Couldn't re-send", "last-RI-sent-type", reflect.TypeOf(lastRISent), "error", err)
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
					p.logger.Warn("AURP Peer: Open-Req received but sender state is not unconnected", "sender-state", p.sstate)
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
					p.logger.Error("AURP Peer: Couldn't send Open-Rsp", "error", err)
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
						p.logger.Error("AURP Peer: Couldn't send Open-Req packet", "error", err)
						return err
					}
					p.setRState(ReceiverWaitForOpenRsp)
				}

			case *aurp.OpenRspPacket:
				if p.rstate != ReceiverWaitForOpenRsp {
					p.logger.Warn("AURP Peer: Received Open-Rsp but was not waiting for one", "receiver-state", p.rstate)
				}
				if pkt.RateOrErrCode < 0 {
					// It's an error code.
					p.logger.Warn("AURP Peer: Open-Rsp error code from peer", "code", pkt.RateOrErrCode)
					p.setRState(ReceiverUnconnected)
					break
				}
				//p.logger.Debug("AURP Peer: Data receiver is connected!")
				p.setRState(ReceiverConnected)

				// Send an RI-Req
				p.resetSendRetries()
				if _, err := p.send(p.Transport.NewRIReqPacket()); err != nil {
					p.logger.Error("AURP Peer: Couldn't send RI-Req packet", "error", err)
					return err
				}
				p.setRState(ReceiverWaitForRIRsp)

			case *aurp.RIReqPacket:
				if p.sstate != SenderConnected {
					p.logger.Warn("AURP Peer: Received RI-Req but was not expecting one", "sender-state", p.sstate)
				}

				// TODO: Load ExtraAdvertisedZones and HiddenZones

				// Build up the slice of network tuples
				var nets aurp.NetworkTuples

				// TODO: filter these by HiddenZones
				for r := range p.RouteTable.ValidRoutesForClass(TargetClassDirect) {
					// Being direct, the best route should be the direct, and
					// the best distance should always be 0.
					nets = append(nets, aurp.NetworkTuple{
						Extended:   r.Extended,
						RangeStart: r.NetStart,
						RangeEnd:   r.NetEnd,
						Distance:   r.Distance,
					})
				}
				// TODO: filter these by ExtraAdvertisedZones and HiddenZones
				for r := range p.RouteTable.ValidRoutesForClass(TargetClassAppleTalkPeer) {
					// Check this route is the best route for the network.
					// If not, per split-horizon it should be hidden.
					best := p.RouteTable.Lookup(r.NetStart)
					if best.Zero() || best.Target.Class() != TargetClassAppleTalkPeer {
						continue
					}

					nets = append(nets, aurp.NetworkTuple{
						Extended:   best.Extended,
						RangeStart: best.NetStart,
						RangeEnd:   best.NetEnd,
						Distance:   best.Distance,
					})
				}
				p.Transport.LocalSeq = 1
				// TODO: Split tuples across multiple packets as required
				lastRISent = p.Transport.NewRIRspPacket(aurp.RoutingFlagLast, nets)
				if _, err := p.send(lastRISent); err != nil {
					p.logger.Error("AURP Peer: Couldn't send RI-Rsp packet", "error", err)
					return err
				}
				p.setSState(SenderWaitForRIRspAck)

			case *aurp.RIRspPacket:
				if p.rstate != ReceiverWaitForRIRsp {
					p.logger.Warn("Received RI-Rsp but was not waiting for one", "receiver-state", p.rstate)
				}

				p.logger.Debug("AURP Peer: Learned about these networks", "networks", pkt.Networks)

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
					p.logger.Error("AURP Peer: Couldn't send RI-Ack packet", "error", err)
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
					p.logger.Warn("AURP Peer: Received RI-Ack but was not waiting for one", "sender-state", p.sstate)
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
						p.logger.Error("AURP Peer: Couldn't send ZI-Rsp packet", "error", err)
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
						p.logger.Error("AURP Peer: Couldn't send Open-Req packet", "error", err)
						return err
					}
					p.setRState(ReceiverWaitForOpenRsp)
				}

			case *aurp.RIUpdPacket:
				var ackFlag aurp.RoutingFlag

				for _, et := range pkt.Events {
					p.logger.Debug("AURP Peer: RI-Upd event", "event", et)
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
							p.logger.Error("AURP Peer: NA event: couldn't insert route", "error", err)
						}
						ackFlag = aurp.RoutingFlagSendZoneInfo

					case aurp.EventCodeND:
						if err := p.RouteTable.DeleteRoute(p, et.RangeStart); err != nil {
							p.logger.Error("AURP Peer: ND event: couldn't delete route", "error", err)
						}

					case aurp.EventCodeNDC:
						if err := p.RouteTable.UpdateDistance(p, et.RangeStart, et.Distance+1); err != nil {
							p.logger.Error("AURP Peer: NDC event: couldn't update route", "error", err)
						}

					case aurp.EventCodeNRC:
						// "An exterior router sends a Network Route Change
						// (NRC) event if the path to an exported network
						// through its local internet changes to a path through
						// a tunneling port, causing split-horizoned processing
						// to eliminate that network's routing information."
						if err := p.RouteTable.DeleteRoute(p, et.RangeStart); err != nil {
							p.logger.Error("AURP Peer: NRC event: couldn't delete route", "error", err)
						}
					case aurp.EventCodeZC:
						// "This event is reserved for future use."
					}
				}

				if _, err := p.send(p.Transport.NewRIAckPacket(pkt.ConnectionID, pkt.Sequence, ackFlag)); err != nil {
					p.logger.Error("AURP Peer: Couldn't send RI-Ack", "error", err)
					return err
				}

			case *aurp.RDPacket:
				if p.rstate == ReceiverUnconnected || p.rstate == ReceiverWaitForOpenRsp {
					p.logger.Error("AURP Peer: Received RD but was not expecting one", "receiver-state", p.rstate)
				}

				p.logger.Info("AURP Peer: Router Down", "code", int(pkt.ErrorCode), "code-str", pkt.ErrorCode)
				p.RouteTable.DeleteTarget(p)

				// Respond with RI-Ack
				if _, err := p.send(p.Transport.NewRIAckPacket(pkt.ConnectionID, pkt.Sequence, 0)); err != nil {
					p.logger.Error("AURP Peer: Couldn't send RI-Ack", "error", err)
					return err
				}
				// Connections closed
				p.disconnect()

			case *aurp.ZIReqPacket:
				// TODO: split ZI-Rsp packets similarly to ZIP Replies
				zones := p.RouteTable.ZonesForNetworks(pkt.Networks)
				if _, err := p.send(p.Transport.NewZIRspPacket(zones)); err != nil {
					p.logger.Error("AURP Peer: Couldn't send ZI-Rsp packet", "error", err)
					return err
				}

			case *aurp.ZIRspPacket:
				p.logger.Debug("AURP Peer: Learned about these zones", "zones", pkt.Zones)
				for _, zt := range pkt.Zones {
					p.RouteTable.AddZonesToNetwork(zt.Network, zt.Name)
				}

			case *aurp.GDZLReqPacket:
				if _, err := p.send(p.Transport.NewGDZLRspPacket(-1, nil)); err != nil {
					p.logger.Error("AURP Peer: Couldn't send GDZL-Rsp packet", "error", err)
					return err
				}

			case *aurp.GDZLRspPacket:
				p.logger.Warn("AURP Peer: Received a GDZL-Rsp, but I wouldn't have sent a GDZL-Req - so that's weird")

			case *aurp.GZNReqPacket:
				if _, err := p.send(p.Transport.NewGZNRspPacket(pkt.ZoneName, false, nil)); err != nil {
					p.logger.Error("AURP Peer: Couldn't send GZN-Rsp packet", "error", err)
					return err
				}

			case *aurp.GZNRspPacket:
				p.logger.Warn("AURP Peer: Received a GZN-Rsp, but I wouldn't have sent a GZN-Req - so that's weird")

			case *aurp.TicklePacket:
				// Immediately respond with Tickle-Ack
				if _, err := p.send(p.Transport.NewTickleAckPacket()); err != nil {
					p.logger.Error("AURP Peer: Couldn't send Tickle-Ack", "error", err)
					return err
				}

			case *aurp.TickleAckPacket:
				if p.rstate != ReceiverWaitForTickleAck {
					p.logger.Warn("AURP Peer: Received Tickle-Ack but was not waiting for one", "receiver-state", p.rstate)
				}
				p.setRState(ReceiverConnected)
			}
		}
	}
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

	p.logger.Debug("AURP Peer: Sending", "pkt-type", reflect.TypeOf(pkt), "length", b.Len())
	return p.UDPConn.WriteToUDP(b.Bytes(), &net.UDPAddr{IP: p.RemoteAddr, Port: 387})
}

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
