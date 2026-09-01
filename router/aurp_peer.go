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
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net"
	"reflect"
	"sync"
	"sync/atomic"
	"time"

	"drjosh.dev/jrouter/aurp"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/sfiera/multitalk/pkg/ddp"
)

const (
	lastHeardFromTimer   = 90 * time.Second
	tickleRetryLimit     = 10
	sendRetryTimer       = 10 * time.Second
	sendRetryLimit       = 5
	reconnectBackoffBase = 10 * time.Minute
	reconnectBackoffCap  = 2 * time.Hour
	reconnectJitterPct   = 10
	updateTimer          = 10 * time.Second

	chatLogLimit = 200
)

var errDropPacket = errors.New("drop packet")

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

	// tunnelID is the immutable logical identity of this AURP peer. It must
	// not change when DNS selects a different active endpoint.
	tunnelID string

	// The active resolved address of the peer. It can change when a configured
	// hostname's DNS candidate set changes. Use RemoteAddr() to read it safely.
	// NOTE: The UDP port is always assumed to be 387.
	remoteAddr atomic.Value // net.IP

	// Incoming packet channel.
	ReceiveCh chan aurp.RoutingPacket

	// Route table (the peer will add/remove/update routes and zones)
	RouteTable *RouteTable

	// Used to signal that the peer handler loop should attempt to reconnect.
	reconnectCh chan struct{}

	// Best-route changes waiting to be advertised in an RI-Upd.
	// They are coalesced by network so a peer sees at most one event for a
	// network in each update interval.
	pendingEventsMu sync.Mutex
	pendingEvents   map[ddp.Network]pendingAURPChange

	// Remaining chunks in an ACK-gated routing-information sequence.
	// These fields are owned by the Handle goroutine.
	pendingRIRsp    []aurp.NetworkTuples
	pendingRIUpd    []aurp.EventTuples
	pendingZoneInfo map[ddp.Network]*pendingAURPZoneInfo

	// The logger.
	logger *slog.Logger

	// The internal states below are only set within the Handle loop, but can
	// be read concurrently from outside (e.g. status, metrics).
	running           atomic.Bool
	rstate            atomic.Int32 // ReceiverState
	sstate            atomic.Int32 // SenderState
	lastReconnect     atomic.Value // time.Time
	lastHeardFrom     atomic.Value // time.Time
	lastSend          atomic.Value // time.Time // TODO: clarify use of lastSend / sendRetries
	lastUpdate        atomic.Value // time.Time
	lastSuccess       atomic.Value // time.Time
	sendRetries       atomic.Int32
	reconnectFailures atomic.Int32
	nextReconnect     atomic.Value // time.Time

	duplicateRoutingPackets atomic.Uint64
	reacksSent              atomic.Uint64
	staleRoutingPackets     atomic.Uint64
	futureRoutingPackets    atomic.Uint64
	connectionIDMismatches  atomic.Uint64
	lateTickleAcks          atomic.Uint64
	senderRouterDowns       atomic.Uint64
	receiverRouterDowns     atomic.Uint64
	routerDownAcks          atomic.Uint64
	earlyRIUpdates          atomic.Uint64
	earlyRIUpdateAcks       atomic.Uint64
	extendedZIFragments     atomic.Uint64
	extendedZICompleted     atomic.Uint64
	zoneTuplesAccepted      atomic.Uint64
	zoneTuplesIgnored       atomic.Uint64

	// SUI flags requested by the remote data receiver. These control which
	// incremental routing events we send after the initial RI-Rsp exchange.
	suiFlags atomic.Uint32

	// Used for debugging AURP conversations.
	chatLogMu sync.RWMutex
	chatLog   []ChatLogEntry

	// Protocol timing copied from configuration when the peer is created.
	timing AURPConfig

	// Other bits of internal state
	lastRISent   aurp.RoutingPacket
	restartProbe bool
}

// ChatLogEntry is a record of a packet either sent or received and a timestamp.
// It's used for logging AURP conversations for diagnosis.
type ChatLogEntry struct {
	Packet    aurp.RoutingPacket
	Sent      bool // as opposed to Received
	Timestamp time.Time
}

func (p *AURPPeer) lastHeardTimeout() time.Duration {
	if p.timing.LastHeardFromTimeout > 0 {
		return p.timing.LastHeardFromTimeout
	}
	return lastHeardFromTimer
}

func (p *AURPPeer) retryInterval() time.Duration {
	if p.timing.RetryInterval > 0 {
		return p.timing.RetryInterval
	}
	return sendRetryTimer
}

func (p *AURPPeer) routingRetryLimit() int {
	if p.timing.SendRetryLimit > 0 {
		return p.timing.SendRetryLimit
	}
	return sendRetryLimit
}

func (p *AURPPeer) tickleRetriesLimit() int {
	if p.timing.TickleRetryLimit > 0 {
		return p.timing.TickleRetryLimit
	}
	return tickleRetryLimit
}

func (p *AURPPeer) zoneInfoRetryInterval() time.Duration {
	if p.timing.ZoneInfoRetryInterval > 0 {
		return p.timing.ZoneInfoRetryInterval
	}
	return aurpZoneInfoRetryTimer
}

func (p *AURPPeer) setSUIFlags(flags aurp.RoutingFlag) {
	p.suiFlags.Store(uint32(flags & aurp.RoutingFlagAllSUI))
}

func (p *AURPPeer) allowsUpdateEvent(code aurp.EventCode) bool {
	flags := aurp.RoutingFlag(p.suiFlags.Load())
	var required aurp.RoutingFlag
	switch code {
	case aurp.EventCodeNA:
		required = aurp.RoutingFlagSUINA
	case aurp.EventCodeND, aurp.EventCodeNRC:
		required = aurp.RoutingFlagSUINDOrNRC
	case aurp.EventCodeNDC:
		required = aurp.RoutingFlagSUINDC
	case aurp.EventCodeZC:
		required = aurp.RoutingFlagSUIZC
	default:
		return true
	}
	return flags&required != 0
}

func (p *AURPPeer) queueBestNetworkTransition(oldBest, newBest Route) {
	// A fresh RI-Rsp will reconstruct the peer's view after reconnect, so
	// updates accumulated while the sender is disconnected are unnecessary.
	if p.SenderState() == SenderUnconnected {
		return
	}

	var netStart ddp.Network
	switch {
	case !oldBest.Zero():
		netStart = oldBest.NetStart
	case !newBest.Zero():
		netStart = newBest.NetStart
	default:
		return
	}

	netEnd := netStart
	if !newBest.Zero() {
		netEnd = newBest.NetEnd
	} else if !oldBest.Zero() {
		netEnd = oldBest.NetEnd
	}
	if p.timing.rangeHidden(netStart, netEnd) {
		return
	}

	p.pendingEventsMu.Lock()
	defer p.pendingEventsMu.Unlock()

	if p.pendingEvents == nil {
		p.pendingEvents = make(map[ddp.Network]pendingAURPChange)
	}
	change, exists := p.pendingEvents[netStart]
	if !exists {
		change.before = oldBest
	}
	change.after = newBest

	// If the accumulated transition leaves the remote peer with exactly the
	// same exported view it started with, discard it entirely. Also honor the
	// remote receiver's SUI subscription flags from Open-Req / RI-Req.
	event, advertise := aurpEventForBestTransition(change.before, change.after)
	if !advertise || !p.allowsUpdateEvent(event.EventCode) {
		delete(p.pendingEvents, netStart)
		return
	}
	p.pendingEvents[netStart] = change
}

func (p *AURPPeer) takePendingEvents() aurp.EventTuples {
	p.pendingEventsMu.Lock()
	changes := p.pendingEvents
	p.pendingEvents = nil
	p.pendingEventsMu.Unlock()
	return aurpEventsForPendingChanges(changes)
}

// NetworkAdded implements RouteTableObserver.
func (p *AURPPeer) NetworkAdded(newBest Route) {
	p.queueBestNetworkTransition(Route{}, newBest)
}

// NetworkDeleted implements RouteTableObserver.
func (p *AURPPeer) NetworkDeleted(oldBest Route) {
	p.queueBestNetworkTransition(oldBest, Route{})
}

// BestNetworkChanged implements RouteTableObserver.
func (p *AURPPeer) BestNetworkChanged(oldBest, newBest Route) {
	p.queueBestNetworkTransition(oldBest, newBest)
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

// TunnelID returns the immutable logical identity of this AURP tunnel peer.
func (p *AURPPeer) TunnelID() string {
	return p.tunnelID
}

// RouteTargetKey uses the immutable tunnel identity rather than the active IP
// endpoint, so DNS failover cannot orphan route-table entries.
func (p *AURPPeer) RouteTargetKey() string {
	return "AURPPeer|" + p.tunnelID
}

// Class returns TargetClassAURPPeer.
func (p *AURPPeer) Class() TargetClass { return TargetClassAURPPeer }

func (p *AURPPeer) String() string {
	return p.RemoteAddrString()
}

// setRemoteAddr updates the active IPv4 endpoint atomically.
func (p *AURPPeer) setRemoteAddr(raddr net.IP) {
	raddr4 := raddr.To4()
	if raddr4 == nil {
		return
	}
	p.remoteAddr.Store(append(net.IP(nil), raddr4...))
}

// RemoteAddr returns a copy of the active resolved IPv4 endpoint.
func (p *AURPPeer) RemoteAddr() net.IP {
	raddr := nilToZero[net.IP](p.remoteAddr.Load())
	if raddr == nil {
		return nil
	}
	return append(net.IP(nil), raddr...)
}

// RemoteAddrString returns the active resolved IPv4 endpoint as text.
func (p *AURPPeer) RemoteAddrString() string {
	return p.RemoteAddr().String()
}

// Running reports whether the handler loop is running.
func (p *AURPPeer) Running() bool { return p.running.Load() }

// ReceiverState returns the current route-data receiver state.
func (p *AURPPeer) ReceiverState() ReceiverState {
	return ReceiverState(p.rstate.Load())
}

// SenderState returns the current route-data sender state.
func (p *AURPPeer) SenderState() SenderState {
	return SenderState(p.sstate.Load())
}

// ReceiverConnected returns a simple bool reflecting whether the receiver is
// connected.
func (p *AURPPeer) ReceiverConnected() bool {
	rstate := p.ReceiverState()
	return rstate != ReceiverUnconnected && rstate != ReceiverWaitForOpenRsp
}

// SenderConnected returns a simple bool reflecting whether the sender is
// connected.
func (p *AURPPeer) SenderConnected() bool {
	sstate := p.SenderState()
	return sstate != SenderUnconnected && sstate != SenderWaitForRDAck
}

// LastReconnect returns the time of the last reconnect to this peer.
func (p *AURPPeer) LastReconnect() time.Time {
	return nilToZero[time.Time](p.lastReconnect.Load())
}

// LastHeardFromAgo returns the time of the last packet received from this peer.
func (p *AURPPeer) LastHeardFrom() time.Time {
	return nilToZero[time.Time](p.lastHeardFrom.Load())
}

func (p *AURPPeer) markReceiverAlive() {
	p.lastHeardFrom.Store(time.Now())
}

// LastSendAgo returns the time of the last packet sent to this peer.
func (p *AURPPeer) LastSend() time.Time {
	return nilToZero[time.Time](p.lastSend.Load())
}

// LastUpdateAgo returns the time of the last (route) update received from the
// peer.
func (p *AURPPeer) LastUpdate() time.Time {
	return nilToZero[time.Time](p.lastUpdate.Load())
}

// SendRetries returns the number of send-retries for the last route update
// send to this peer.
func (p *AURPPeer) SendRetries() int {
	return int(p.sendRetries.Load())
}

// LastSuccess returns the time of the last complete RI-Rsp exchange.
func (p *AURPPeer) LastSuccess() time.Time {
	return nilToZero[time.Time](p.lastSuccess.Load())
}

// DuplicateRoutingPackets returns the number of n-1 routing packets seen.
func (p *AURPPeer) DuplicateRoutingPackets() uint64 {
	return p.duplicateRoutingPackets.Load()
}

// ReacksSent returns the number of duplicate-packet RI-Acks re-sent.
func (p *AURPPeer) ReacksSent() uint64 {
	return p.reacksSent.Load()
}

// StaleRoutingPackets returns the number of stale routing packets dropped.
func (p *AURPPeer) StaleRoutingPackets() uint64 {
	return p.staleRoutingPackets.Load()
}

// FutureRoutingPackets returns the number of n+1 routing packets that forced
// the receiver connection to reset.
func (p *AURPPeer) FutureRoutingPackets() uint64 {
	return p.futureRoutingPackets.Load()
}

// ConnectionIDMismatches returns the number of routing packets dropped because
// the connection ID did not match the active one-way connection.
func (p *AURPPeer) ConnectionIDMismatches() uint64 {
	return p.connectionIDMismatches.Load()
}

func (p *AURPPeer) LateTickleAcks() uint64 {
	return p.lateTickleAcks.Load()
}

func (p *AURPPeer) SenderRouterDowns() uint64 {
	return p.senderRouterDowns.Load()
}

func (p *AURPPeer) ReceiverRouterDowns() uint64 {
	return p.receiverRouterDowns.Load()
}

func (p *AURPPeer) RouterDownAcks() uint64 {
	return p.routerDownAcks.Load()
}

func (p *AURPPeer) EarlyRIUpdates() uint64 {
	return p.earlyRIUpdates.Load()
}

func (p *AURPPeer) EarlyRIUpdateAcks() uint64 {
	return p.earlyRIUpdateAcks.Load()
}

func (p *AURPPeer) ExtendedZIFragments() uint64 {
	return p.extendedZIFragments.Load()
}

func (p *AURPPeer) ExtendedZICompleted() uint64 {
	return p.extendedZICompleted.Load()
}

func (p *AURPPeer) ZoneTuplesAccepted() uint64 {
	return p.zoneTuplesAccepted.Load()
}

func (p *AURPPeer) ZoneTuplesIgnored() uint64 {
	return p.zoneTuplesIgnored.Load()
}

// ReconnectFailures returns the number of consecutive failed receiver
// connection attempts since the last fully established routing connection.
func (p *AURPPeer) ReconnectFailures() int {
	return int(p.reconnectFailures.Load())
}

// NextReconnect returns the earliest time the configured peer should be
// retried after a failed connection attempt.
func (p *AURPPeer) NextReconnect() time.Time {
	return nilToZero[time.Time](p.nextReconnect.Load())
}

func reconnectBackoff(failures int) time.Duration {
	if failures <= 0 {
		return 0
	}
	d := reconnectBackoffBase
	for i := 1; i < failures && d < reconnectBackoffCap; i++ {
		d *= 2
		if d >= reconnectBackoffCap {
			return reconnectBackoffCap
		}
	}
	return min(d, reconnectBackoffCap)
}

func jitterReconnectBackoff(d time.Duration) time.Duration {
	if d <= 0 {
		return 0
	}
	span := int64(d) * reconnectJitterPct / 100
	if span <= 0 {
		return d
	}
	return d + time.Duration(rand.Int64N(2*span+1)-span)
}

func (p *AURPPeer) noteReconnectFailure(now time.Time) {
	failures := int(p.reconnectFailures.Add(1))
	delay := jitterReconnectBackoff(reconnectBackoff(failures))
	p.nextReconnect.Store(now.Add(delay))
	p.logger.Info(
		"AURP Peer: reconnect backoff scheduled",
		"failures", failures,
		"delay", delay,
		"next-reconnect", now.Add(delay),
	)
}

func (p *AURPPeer) resetReconnectBackoff() {
	p.reconnectFailures.Store(0)
	p.nextReconnect.Store(time.Time{})
}

func (p *AURPPeer) reconnectReady(now time.Time) bool {
	next := p.NextReconnect()
	return next.IsZero() || !now.Before(next)
}

// ReceiveChLen returns len(p.ReceiveCh).
func (p *AURPPeer) ReceiveChLen() int {
	return len(p.ReceiveCh)
}

// DumpChatLog returns the "chat log" for this peer: the AURP conversation.
// It only includes routing packets, and not encapsulated AppleTalk.
func (p *AURPPeer) DumpChatLog() []ChatLogEntry {
	p.chatLogMu.RLock()
	defer p.chatLogMu.RUnlock()
	return p.chatLog
}

// Handle handles incoming packets, maintains the connections, and runs periodic
// tasks for this peer. It is safe to call multiple times concurrently - only
// one will run.
func (p *AURPPeer) Handle(ctx context.Context) {
	if !p.running.CompareAndSwap(false, true) {
		p.logger.Debug("AURP: handle loop for peer already running", "raddr", p.RemoteAddr())
		return
	}
	defer p.running.Store(false)

	// Stop listening to events if the goroutine exits
	defer p.RouteTable.RemoveObserver(p)

	p.disconnect()
	now := time.Now()
	p.lastReconnect.Store(now)
	p.lastHeardFrom.Store(now)
	p.lastSend.Store(now) // TODO: clarify use of lastSend / sendRetries
	p.lastUpdate.Store(now)
	p.sendRetries.Store(0)

	rticker := time.Tick(1 * time.Second)
	sticker := time.Tick(1 * time.Second)

	// Write an Open-Req packet
	if _, err := p.send(p.Transport.NewOpenReqPacket(nil)); err != nil {
		p.logger.Error("AURP Peer: Couldn't send Open-Req packet", "error", err)
		return
	}

	p.setRState(ReceiverWaitForOpenRsp)

	for {
		select {
		case <-ctx.Done():
			if err := p.gracefulSenderShutdown(); err != nil {
				p.logger.Warn("AURP Peer: graceful sender shutdown ended with error", "error", err)
			}
			return

		case <-p.reconnectCh:
			now := time.Now()
			if p.ReceiverState() != ReceiverUnconnected || !p.reconnectReady(now) {
				continue
			}
			p.lastReconnect.Store(now)
			p.lastSend.Store(now)
			p.sendRetries.Store(0)
			if _, err := p.send(p.Transport.NewOpenReqPacket(nil)); err != nil {
				p.logger.Error("AURP Peer: Couldn't send Open-Req packet", "error", err)
				return
			}
			p.setRState(ReceiverWaitForOpenRsp)

		case <-rticker:
			if err := p.rtickerTasks(); err != nil {
				return
			}

		case <-sticker:
			if err := p.stickerTasks(); err != nil {
				return
			}

		case pkt := <-p.ReceiveCh:
			if err := p.handlePacket(pkt); err != nil {
				return
			}
		}
	}
}

func (p *AURPPeer) startSenderShutdown() error {
	if p.SenderState() != SenderConnected {
		return nil
	}

	p.Transport.IncLocalSeq()
	p.lastRISent = p.Transport.NewSenderRDPacket(aurp.ErrCodeNormalClose)
	p.sendRetries.Store(0)
	if _, err := p.send(p.lastRISent); err != nil {
		return err
	}
	p.setSState(SenderWaitForRDAck)
	return nil
}

func (p *AURPPeer) gracefulSenderShutdown() error {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	for {
		switch p.SenderState() {
		case SenderUnconnected:
			return nil
		case SenderConnected:
			if err := p.startSenderShutdown(); err != nil {
				p.disconnectSender()
				return err
			}
		}

		select {
		case pkt := <-p.ReceiveCh:
			if err := p.handlePacket(pkt); err != nil {
				return err
			}
		case <-ticker.C:
			if err := p.stickerTasks(); err != nil {
				return err
			}
		}
	}
}

func (p *AURPPeer) rtickerTasks() error {
	switch p.ReceiverState() {
	case ReceiverWaitForOpenRsp:
		if time.Since(p.LastSend()) <= p.retryInterval() {
			break
		}
		if p.SendRetries() >= p.routingRetryLimit() {
			p.logger.Warn("AURP Peer: Send retry limit reached while waiting for Open-Rsp, closing connection")
			p.disconnectReceiver()
			p.noteReconnectFailure(time.Now())
			break
		}

		// Send another Open-Req
		p.sendRetries.Add(1)
		p.lastSend.Store(time.Now())
		if _, err := p.send(p.Transport.NewOpenReqPacket(nil)); err != nil {
			p.logger.Error("AURP Peer: Couldn't send Open-Req packet", "error", err)
			return err
		}

	case ReceiverConnected:
		// Check LHFT, send tickle?
		if time.Since(p.LastHeardFrom()) <= p.lastHeardTimeout() {
			break
		}
		if _, err := p.send(p.Transport.NewTicklePacket()); err != nil {
			p.logger.Error("AURP Peer: Couldn't send Tickle", "error", err)
			return err
		}
		p.setRState(ReceiverWaitForTickleAck)
		p.sendRetries.Store(0)
		p.lastSend.Store(time.Now())

	case ReceiverWaitForTickleAck:
		if time.Since(p.LastSend()) <= p.retryInterval() {
			break
		}
		if p.SendRetries() >= p.tickleRetriesLimit() {
			p.logger.Warn("AURP Peer: Send retry limit reached while waiting for Tickle-Ack, closing connection")
			p.disconnectReceiver()
			p.noteReconnectFailure(time.Now())
			break
		}

		p.sendRetries.Add(1)
		p.lastSend.Store(time.Now())
		if _, err := p.send(p.Transport.NewTicklePacket()); err != nil {
			p.logger.Error("AURP Peer: Couldn't send Tickle", "error", err)
			return err
		}
		// still in Wait For Tickle-Ack

	case ReceiverWaitForRIRsp:
		if time.Since(p.LastSend()) <= p.retryInterval() {
			break
		}
		if p.SendRetries() >= p.routingRetryLimit() {
			p.logger.Warn("AURP Peer: Send retry limit reached while waiting for RI-Rsp, closing connection")
			p.disconnectReceiver()
			p.RouteTable.DeleteTarget(p)
			p.noteReconnectFailure(time.Now())
			break
		}

		// RI-Req is stateless, so we don't need to cache the one we
		// sent earlier just to send it again
		p.sendRetries.Add(1)
		p.lastSend.Store(time.Now())
		if _, err := p.send(p.Transport.NewRIReqPacket()); err != nil {
			p.logger.Error("AURP Peer: Couldn't send RI-Req packet", "error", err)
			return err
		}
		// still in Wait For RI-Rsp

	case ReceiverUnconnected:
		// Data receiver is unconnected. If data sender is connected,
		// send a null RI-Upd to check if the sender is also unconnected
		if p.SenderState() == SenderConnected && time.Since(p.LastSend()) > p.retryInterval() {
			if p.SendRetries() >= p.routingRetryLimit() {
				p.logger.Warn("AURP Peer: Send retry limit reached while probing sender connect, closing connection")
			}
			p.sendRetries.Add(1)
			p.lastSend.Store(time.Now())
			p.Transport.IncLocalSeq()
			events := aurp.EventTuples{{
				EventCode: aurp.EventCodeNull,
			}}
			p.lastRISent = p.Transport.NewRIUpdPacket(events)
			if _, err := p.send(p.lastRISent); err != nil {
				p.logger.Error("AURP Peer: Couldn't send RI-Upd packet: %v", "error", err)
				return err
			}
			p.setSState(SenderWaitForRIUpdAck)
		}
	}

	switch p.ReceiverState() {
	case ReceiverConnected, ReceiverWaitForTickleAck:
		if err := p.retryIncompleteZoneInfo(time.Now()); err != nil {
			p.logger.Error("AURP Peer: Couldn't re-request incomplete zone information", "error", err)
			return err
		}
	}
	return nil
}

func (p *AURPPeer) nextRIRspPacket(advanceSequence bool) (*aurp.RIRspPacket, error) {
	if len(p.pendingRIRsp) == 0 {
		return nil, errors.New("no RI-Rsp chunks pending")
	}
	if advanceSequence {
		p.Transport.IncLocalSeq()
	}

	chunk := p.pendingRIRsp[0]
	p.pendingRIRsp = p.pendingRIRsp[1:]

	var flags aurp.RoutingFlag
	if len(p.pendingRIRsp) == 0 {
		flags = aurp.RoutingFlagLast
	}
	return p.Transport.NewRIRspPacket(flags, chunk), nil
}

func (p *AURPPeer) sendNextRIRsp(advanceSequence bool) error {
	pkt, err := p.nextRIRspPacket(advanceSequence)
	if err != nil {
		return err
	}
	p.lastRISent = pkt
	if _, err := p.send(pkt); err != nil {
		return err
	}
	p.setSState(SenderWaitForRIRspAck)
	return nil
}

func (p *AURPPeer) nextRIUpdPacket(advanceSequence bool) (*aurp.RIUpdPacket, error) {
	if len(p.pendingRIUpd) == 0 {
		return nil, errors.New("no RI-Upd chunks pending")
	}
	if advanceSequence {
		p.Transport.IncLocalSeq()
	}

	chunk := p.pendingRIUpd[0]
	p.pendingRIUpd = p.pendingRIUpd[1:]
	return p.Transport.NewRIUpdPacket(chunk), nil
}

func (p *AURPPeer) sendNextRIUpd(advanceSequence bool) error {
	pkt, err := p.nextRIUpdPacket(advanceSequence)
	if err != nil {
		return err
	}
	p.lastRISent = pkt
	if _, err := p.send(pkt); err != nil {
		return err
	}
	p.setSState(SenderWaitForRIUpdAck)
	return nil
}

func (p *AURPPeer) stickerTasks() error {
	switch p.SenderState() {
	case SenderUnconnected:
		// Do nothing

	case SenderConnected:
		if time.Since(p.LastUpdate()) <= updateTimer {
			break
		}

		events := p.takePendingEvents()
		if len(events) == 0 {
			break
		}

		payloadBudget, err := aurpRoutingPayloadBudget(
			p.Transport.NewRIUpdPacket(nil),
		)
		if err != nil {
			return err
		}
		chunks, err := chunkAURPEventTuples(events, payloadBudget)
		if err != nil {
			return err
		}

		p.pendingRIUpd = chunks
		p.lastUpdate.Store(time.Now())
		if err := p.sendNextRIUpd(true); err != nil {
			p.logger.Error("AURP Peer: Couldn't send RI-Upd packet", "error", err)
			return err
		}

	case SenderWaitForRIRspAck, SenderWaitForRIUpdAck:
		if time.Since(p.LastSend()) <= p.retryInterval() {
			break
		}
		if p.lastRISent == nil {
			p.logger.Error("AURP Peer: sender retry: lastRISent = nil?")
			break
		}
		if p.SendRetries() >= p.routingRetryLimit() {
			if p.restartProbe {
				p.logger.Warn(
					"AURP Peer: restarted-peer probe failed; closing old sender connection",
					"remote-conn-id", p.Transport.RemoteConnID(),
				)
				p.RouteTable.RemoveObserver(p)
				p.disconnectSender()
				break
			}

			p.logger.Warn("AURP Peer: Send retry limit reached, closing connection")
			p.setSState(SenderUnconnected)
			p.RouteTable.RemoveObserver(p)
			break
		}
		p.sendRetries.Add(1)
		p.lastSend.Store(time.Now())
		if _, err := p.send(p.lastRISent); err != nil {
			p.logger.Error("AURP Peer: Couldn't re-send", "last-RI-sent-type", reflect.TypeOf(p.lastRISent), "error", err)
			return err
		}

	case SenderWaitForRDAck:
		if time.Since(p.LastSend()) <= p.retryInterval() {
			break
		}
		if p.lastRISent == nil {
			p.logger.Warn("AURP Peer: RD retry state has no cached RD; closing sender connection")
			p.RouteTable.RemoveObserver(p)
			p.disconnectSender()
			break
		}
		if p.SendRetries() >= p.routingRetryLimit() {
			p.logger.Warn("AURP Peer: RD acknowledgement retry limit reached; closing sender connection")
			p.RouteTable.RemoveObserver(p)
			p.disconnectSender()
			break
		}
		p.sendRetries.Add(1)
		if _, err := p.send(p.lastRISent); err != nil {
			p.logger.Error("AURP Peer: Couldn't re-send RD", "error", err)
			return err
		}
	}

	return nil
}

func (p *AURPPeer) handlePacket(pkt aurp.RoutingPacket) error {
	p.addToChatLog(pkt, false /* received */)

	header := pkt.AURPHeader()
	logger := p.logger.With(
		"conn-id", header.ConnectionID,
		"seq", header.Sequence,
		"cmd-code", header.CommandCode,
		"flags", header.Flags,
		"receiver-state", p.ReceiverState(),
		"sender-state", p.SenderState(),
	)

	switch pkt := pkt.(type) {
	case *aurp.OpenReqPacket:
		return p.handleOpenReq(logger, pkt)

	case *aurp.OpenRspPacket:
		return p.handleOpenRsp(logger, pkt)

	case *aurp.RIReqPacket:
		return p.handleRIReq(logger, pkt)

	case *aurp.RIRspPacket:
		return p.handleRIRsp(logger, pkt)

	case *aurp.RIAckPacket:
		return p.handleRIAck(logger, pkt)

	case *aurp.RIUpdPacket:
		return p.handleRIUpd(logger, pkt)

	case *aurp.RDPacket:
		return p.handleRD(logger, pkt)

	case *aurp.ZIReqPacket:
		return p.handleZIReq(logger, pkt)

	case *aurp.ZIRspPacket:
		return p.handleZIRsp(logger, pkt)

	case *aurp.GDZLReqPacket:
		return p.handleGDZLReq(logger, pkt)

	case *aurp.GDZLRspPacket:
		return p.handleGDZLRsp(logger, pkt)

	case *aurp.GZNReqPacket:
		return p.handleGZNReq(logger, pkt)

	case *aurp.GZNRspPacket:
		return p.handleGZNRsp(logger, pkt)

	case *aurp.TicklePacket:
		return p.handleTickle(logger, pkt)

	case *aurp.TickleAckPacket:
		return p.handleTickleAck(logger, pkt)

	default:
		logger.Error("AURP Peer: unknown routing information packet; dropping", "type", reflect.TypeOf(pkt))
		return nil
	}
}

func (p *AURPPeer) handleOpenReq(logger *slog.Logger, pkt *aurp.OpenReqPacket) error {
	// We are: sender
	// They are: receiver

	sstate := p.SenderState()
	currentConnID := p.Transport.RemoteConnID()

	if sstate != SenderUnconnected {
		if currentConnID == 0 {
			logger.Warn(
				"AURP Peer: active sender has no remote connection ID; dropping Open-Req",
				"sender-state", sstate,
				"new-conn-id", pkt.ConnectionID,
			)
			return nil
		}

		if pkt.ConnectionID == currentConnID {
			// A repeated Open-Req with the established connection ID
			// can mean our earlier Open-Rsp was lost. Re-send it only
			// when no routing-information transaction is outstanding.
			if sstate != SenderConnected {
				logger.Warn(
					"AURP Peer: duplicate Open-Req while sender transaction is outstanding; dropping",
					"sender-state", sstate,
					"conn-id", pkt.ConnectionID,
				)
				return nil
			}

			logger.Info(
				"AURP Peer: duplicate Open-Req for existing sender connection; re-sending Open-Rsp",
				"conn-id", pkt.ConnectionID,
			)
		} else {
			// RFC 1504 restarted-peer reconciliation.
			//
			// A different connection ID may mean the remote data
			// receiver restarted without closing its old one-way
			// connection. Before accepting the replacement, send a
			// Null RI-Upd over the OLD connection.
			if sstate != SenderConnected {
				logger.Warn(
					"AURP Peer: replacement Open-Req deferred while routing transaction is outstanding",
					"sender-state", sstate,
					"current-conn-id", currentConnID,
					"new-conn-id", pkt.ConnectionID,
				)
				return nil
			}

			p.sendRetries.Store(0)
			p.Transport.IncLocalSeq()
			p.restartProbe = true
			p.lastRISent = p.Transport.NewRIUpdPacket(
				aurp.EventTuples{{
					EventCode: aurp.EventCodeNull,
				}},
			)
			p.lastSend.Store(time.Now())

			if _, err := p.send(p.lastRISent); err != nil {
				p.restartProbe = false
				p.lastRISent = nil
				logger.Error(
					"AURP Peer: couldn't send restarted-peer probe",
					"error", err,
				)
				return err
			}

			p.setSState(SenderWaitForRIUpdAck)

			logger.Info(
				"AURP Peer: probing existing sender connection before accepting replacement Open-Req",
				"current-conn-id", currentConnID,
				"new-conn-id", pkt.ConnectionID,
			)

			return nil
		}
	}

	// The peer tells us their connection ID and which update classes it wants
	// in Open-Req. RFC 1504 requires the sender to honor these SUI flags.
	p.Transport.SetRemoteConnID(pkt.ConnectionID)
	p.setSUIFlags(pkt.Flags)

	// RFC 1504 permits a sender that does not implement requested options to
	// discard the option tuples and continue opening the connection.
	if len(pkt.Options) > 0 {
		logger.Info(
			"AURP Peer: ignoring unsupported Open-Req option data",
			"option-count", len(pkt.Options),
		)
	}

	// Formulate a response.
	var orsp *aurp.OpenRspPacket
	switch {
	case pkt.Version != 1:
		// Respond with Open-Rsp with unknown version error.
		orsp = p.Transport.NewOpenRspPacket(0, int16(aurp.ErrCodeInvalidVersion), nil)

	default:
		orsp = p.Transport.NewOpenRspPacket(0, 1, nil)
	}

	if _, err := p.send(orsp); err != nil {
		logger.Error("AURP Peer: Couldn't send Open-Rsp", "error", err)
		return err
	}
	if orsp.RateOrErrCode >= 0 {
		// Data sender is successfully in connected state
		p.setSState(SenderConnected)
		p.RouteTable.AddObserver(p)
	}

	// If receiver is unconnected, commence connecting
	if p.ReceiverState() == ReceiverUnconnected {
		p.sendRetries.Store(0)
		p.lastSend.Store(time.Now())
		if _, err := p.send(p.Transport.NewOpenReqPacket(nil)); err != nil {
			logger.Error("AURP Peer: Couldn't send Open-Req packet", "error", err)
			return err
		}
		p.setRState(ReceiverWaitForOpenRsp)
	}

	return nil
}

func (p *AURPPeer) handleOpenRsp(logger *slog.Logger, pkt *aurp.OpenRspPacket) error {
	// We are: receiver
	// They are: sender

	if err := p.checkLocalConnID(logger, &pkt.TrHeader); err != nil {
		if err == errDropPacket {
			return nil
		}
		return err
	}

	if rstate := p.ReceiverState(); rstate != ReceiverWaitForOpenRsp {
		logger.Warn("AURP Peer: Received Open-Rsp but was not waiting for one")
	}
	if pkt.RateOrErrCode < 0 {
		// It's an error code.
		logger.Warn("AURP Peer: Open-Rsp error code from peer", "code", pkt.RateOrErrCode, "error", aurp.ErrorCode(pkt.RateOrErrCode))
		p.disconnectReceiver()
		p.noteReconnectFailure(time.Now())
		return nil
	}
	//logger.Debug("AURP Peer: Data receiver is connected!")
	p.setRState(ReceiverConnected)

	// Send an RI-Req
	p.sendRetries.Store(0)
	if _, err := p.send(p.Transport.NewRIReqPacket()); err != nil {
		logger.Error("AURP Peer: Couldn't send RI-Req packet", "error", err)
		return err
	}
	p.setRState(ReceiverWaitForRIRsp)
	p.Transport.ResetRemoteSeq()

	return nil
}

func (p *AURPPeer) handleRIReq(logger *slog.Logger, pkt *aurp.RIReqPacket) error {
	// We are: sender
	// They are: receiver

	if err := p.checkRemoteConnID(logger, &pkt.TrHeader); err != nil {
		if err == errDropPacket {
			return nil
		}
		return err
	}

	if sstate := p.SenderState(); sstate != SenderConnected {
		logger.Warn("AURP Peer: Received RI-Req but was not expecting one")
	}
	p.setSUIFlags(pkt.Flags)

	// TODO: Load ExtraAdvertisedZones and HiddenZones. The base exported route
	// set is deliberately built in one place so initial RI-Rsp and incremental
	// RI-Upd split-horizon policy cannot drift apart.
	routes := p.aurpExportedRoutes()
	nets := make(aurp.NetworkTuples, 0, len(routes))
	for _, r := range routes {
		nets = append(nets, aurp.NetworkTuple{
			Extended:   r.Extended,
			RangeStart: r.NetStart,
			RangeEnd:   r.NetEnd,
			Distance:   r.Distance,
		})
	}

	p.Transport.ResetLocalSeq()
	payloadBudget, err := aurpRoutingPayloadBudget(
		p.Transport.NewRIRspPacket(0, nil),
	)
	if err != nil {
		return err
	}
	chunks, err := chunkAURPNetworkTuples(nets, payloadBudget)
	if err != nil {
		return err
	}
	p.pendingRIRsp = chunks

	if err := p.sendNextRIRsp(false); err != nil {
		logger.Error("AURP Peer: Couldn't send RI-Rsp packet", "error", err)
		return err
	}
	return nil
}

func (p *AURPPeer) applyRIRspNetworkTuple(nt aurp.NetworkTuple) (bool, error) {
	if err := validateAURPRouteTuple(nt.Extended, nt.RangeStart, nt.RangeEnd); err != nil {
		return false, err
	}
	if nt.Distance >= maxRouteDistance {
		return false, nil
	}
	_, err := p.RouteTable.UpsertRoute(
		p,
		nt.Extended,
		nt.RangeStart,
		nt.RangeEnd,
		nt.Distance+1,
	)
	return err == nil, err
}

func (p *AURPPeer) handleRIRsp(logger *slog.Logger, pkt *aurp.RIRspPacket) error {
	// We are: receiver
	// They are: sender

	if err := p.checkLocalConnID(logger, &pkt.TrHeader); err != nil {
		if err == errDropPacket {
			return nil
		}
		return err
	}
	p.markReceiverAlive()

	if err := p.checkRemoteSeq(logger, &pkt.TrHeader, "RI-Rsp"); err != nil {
		if err == errDropPacket {
			return nil
		}
		return err
	}
	if rstate := p.ReceiverState(); rstate != ReceiverWaitForRIRsp {
		// A delayed final chunk or a peer-initiated refresh can arrive after
		// the state has returned to connected. It is still valid when its
		// connection ID and sequence number pass the normal checks above.
		logger.Info(
			"AURP Peer: processing RI-Rsp outside initial synchronization",
			"receiver-state", rstate,
			"reason", "late-or-refresh-response",
		)
	}

	logger.Debug("AURP Peer: Learned about these networks", "networks", pkt.Networks)

	for _, nt := range pkt.Networks {
		logger := logger.With(
			"extended", nt.Extended,
			"net-start", nt.RangeStart,
			"net-end", nt.RangeEnd,
			"distance", nt.Distance,
		)

		accepted, err := p.applyRIRspNetworkTuple(nt)
		if err != nil {
			logger.Warn("AURP Peer: RI-Rsp: ignored invalid route tuple", "error", err, "action", "no route installed")
			continue
		}
		if !accepted {
			logger.Info(
				"AURP Peer: RI-Rsp: ignored unreachable route tuple",
				"reason", "distance-15-or-higher",
				"action", "no route installed",
			)
			continue
		}
		p.markZoneInfoPending(nt.RangeStart)
	}

	// TODO: track which networks we don't have zone info for, and
	// only set SZI for those ?
	if _, err := p.send(p.Transport.NewRIAckPacket(pkt.ConnectionID, pkt.Sequence, aurp.RoutingFlagSendZoneInfo)); err != nil {
		logger.Error("AURP Peer: Couldn't send RI-Ack packet", "error", err)
		return err
	}
	if pkt.Flags&aurp.RoutingFlagLast != 0 {
		// No longer waiting for an RI-Rsp. A complete routing-information
		// exchange proves the receiver connection is healthy, so reconnect
		// pacing returns to its initial state.
		p.setRState(ReceiverConnected)
		p.lastSuccess.Store(time.Now())
		p.resetReconnectBackoff()
	}
	p.Transport.IncRemoteSeq()
	return nil
}

func (p *AURPPeer) handleRIAck(logger *slog.Logger, pkt *aurp.RIAckPacket) error {
	// We are: sender
	// They are: receiver

	if err := p.checkRemoteConnID(logger, &pkt.TrHeader); err != nil {
		if err == errDropPacket {
			return nil
		}
		return err
	}

	if got, want := pkt.Sequence, p.Transport.LocalSeq(); got != want {
		logger.Warn("AURP Peer: RI-Ack out of sequence, discarding packet", "want-seq", want)
		return nil
	}

	sstate := p.SenderState()
	switch sstate {
	case SenderWaitForRIRspAck:
		// We sent an RI-Rsp, this is the RI-Ack we expected.
	case SenderWaitForRIUpdAck:
		// We sent an RI-Upd, this is the RI-Ack we expected.
		if p.restartProbe {
			p.sendRetries.Store(0)
			p.restartProbe = false
			p.lastRISent = nil
			p.setSState(SenderConnected)

			logger.Info(
				"AURP Peer: restarted-peer probe acknowledged; keeping existing sender connection",
				"remote-conn-id", p.Transport.RemoteConnID(),
			)
			return nil
		}
	case SenderWaitForRDAck:
		p.sendRetries.Store(0)
		p.RouteTable.RemoveObserver(p)
		p.disconnectSender()
		logger.Info("AURP Peer: Router Down acknowledged; sender connection closed")
		return nil
	default:
		logger.Warn("AURP Peer: Received RI-Ack but was not waiting for one")
	}

	// If SZI is set, return zone information for the networks in the packet
	// that was just acknowledged, before advancing a multi-packet sequence.
	if pkt.Flags&aurp.RoutingFlagSendZoneInfo != 0 {
		var nets []ddp.Network
		switch last := p.lastRISent.(type) {
		case *aurp.RIRspPacket:
			for _, nt := range last.Networks {
				nets = append(nets, nt.RangeStart)
			}
		case *aurp.RIUpdPacket:
			for _, et := range last.Events {
				if et.EventCode == aurp.EventCodeNA {
					nets = append(nets, et.RangeStart)
				}
			}
		}
		zones := p.RouteTable.ZonesForNetworks(nets)
		if err := p.sendZIRspPackets(zones); err != nil {
			logger.Error("AURP Peer: Couldn't send ZI-Rsp packet", "error", err)
		}
	}

	p.sendRetries.Store(0)

	// AURP-Tr permits one outstanding routing-information packet. Only advance
	// to the next chunk after the current chunk has been acknowledged.
	switch sstate {
	case SenderWaitForRIRspAck:
		if len(p.pendingRIRsp) > 0 {
			if err := p.sendNextRIRsp(true); err != nil {
				logger.Error("AURP Peer: Couldn't send next RI-Rsp packet", "error", err)
				return err
			}
			return nil
		}
	case SenderWaitForRIUpdAck:
		if len(p.pendingRIUpd) > 0 {
			if err := p.sendNextRIUpd(true); err != nil {
				logger.Error("AURP Peer: Couldn't send next RI-Upd packet", "error", err)
				return err
			}
			return nil
		}
	}

	p.setSState(SenderConnected)
	p.RouteTable.AddObserver(p)

	if p.ReceiverState() == ReceiverUnconnected {
		p.sendRetries.Store(0)
		if _, err := p.send(p.Transport.NewOpenReqPacket(nil)); err != nil {
			logger.Error("AURP Peer: Couldn't send Open-Req packet", "error", err)
			return err
		}
		p.setRState(ReceiverWaitForOpenRsp)
	}
	return nil
}

func (p *AURPPeer) applyRIUpdEvent(et aurp.EventTuple) (bool, error) {
	// ND/NRC identify the route by RangeStart only; their range end is not
	// consumed and older peers/tests may leave it at zero. Validate the
	// range shape for events that actually carry a replacement route.
	if et.EventCode == aurp.EventCodeNA || et.EventCode == aurp.EventCodeNDC {
		if err := validateAURPRouteTuple(et.Extended, et.RangeStart, et.RangeEnd); err != nil {
			return false, err
		}
	}
	switch et.EventCode {
	case aurp.EventCodeNull:
		return false, nil

	case aurp.EventCodeZC:
		// RFC 1504 reserves ZC for future use and does not define the event
		// tuple semantics. Accept the surrounding RI-Upd transaction without
		// mutating routing or zone state.
		return false, nil

	case aurp.EventCodeNA:
		if et.Distance >= maxRouteDistance {
			return false, nil
		}
		_, err := p.RouteTable.UpsertRoute(
			p, et.Extended, et.RangeStart, et.RangeEnd, et.Distance+1,
		)
		return err == nil, err

	case aurp.EventCodeND, aurp.EventCodeNRC:
		// RFC 1504 says an ND or NRC for an unknown network is ignored.
		if p.RouteTable.find(p, et.RangeStart).Zero() {
			return false, nil
		}
		return false, p.RouteTable.DeleteRoute(p, et.RangeStart)

	case aurp.EventCodeNDC:
		existing := p.RouteTable.find(p, et.RangeStart)

		// A distance of 15 is processed as a deletion.
		if et.Distance >= maxRouteDistance {
			if existing.Zero() {
				return false, nil
			}
			return false, p.RouteTable.DeleteRoute(p, et.RangeStart)
		}

		// RFC 1504 says an NDC for an unknown network is processed as an NA.
		if existing.Zero() {
			_, err := p.RouteTable.UpsertRoute(
				p, et.Extended, et.RangeStart, et.RangeEnd, et.Distance+1,
			)
			return err == nil, err
		}

		// The tuple carries the network-range shape as well as the metric.
		// Preserve range changes from a peer instead of updating only Distance
		// and leaving stale forwarding entries beyond the new range.
		if existing.Extended != et.Extended ||
			existing.NetEnd != et.RangeEnd {
			_, err := p.RouteTable.UpsertRoute(
				p, et.Extended, et.RangeStart, et.RangeEnd, et.Distance+1,
			)
			return false, err
		}

		return false, p.RouteTable.UpdateDistance(
			p, et.RangeStart, et.Distance+1,
		)
	}
	return false, nil
}

func validateAURPRouteTuple(extended bool, start, end ddp.Network) error {
	if start > end {
		return fmt.Errorf("network range is reversed (%d-%d)", start, end)
	}
	if !extended && start != end {
		return fmt.Errorf("non-extended tuple has range %d-%d", start, end)
	}
	return nil
}

func aurpRIUpdAction(et aurp.EventTuple) string {
	switch et.EventCode {
	case aurp.EventCodeNull:
		return "probe-only"
	case aurp.EventCodeNA:
		if et.Distance >= maxRouteDistance {
			return "ignore-unreachable"
		}
		return "install-or-refresh"
	case aurp.EventCodeND, aurp.EventCodeNRC:
		return "remove-if-present"
	case aurp.EventCodeNDC:
		if et.Distance >= maxRouteDistance {
			return "remove-if-present-distance-15"
		}
		return "update-distance"
	case aurp.EventCodeZC:
		return "reserved-no-op"
	default:
		return "unknown-no-op"
	}
}

func (p *AURPPeer) handleRIUpd(logger *slog.Logger, pkt *aurp.RIUpdPacket) error {
	// We are: receiver
	// They are: sender

	if err := p.checkLocalConnID(logger, &pkt.TrHeader); err != nil {
		if err == errDropPacket {
			return nil
		}
		return err
	}
	p.markReceiverAlive()

	switch rstate := p.ReceiverState(); rstate {
	case ReceiverConnected:
		// Business as usual.

	case ReceiverWaitForTickleAck:
		// Any valid routing update proves the sender is alive. Do not wait
		// for a separate Tickle-Ack before resuming normal processing.
		p.setRState(ReceiverConnected)

	case ReceiverUnconnected, ReceiverWaitForOpenRsp:
		p.earlyRIUpdates.Add(1)
		logger.Warn("AURP Peer: RI-Upd arrived before receiver connection was ready")
		// Re-synchronize from a complete routing snapshot. Do not apply this
		// update because there is no known-good baseline yet.
		if _, err := p.send(p.Transport.NewRIReqPacket()); err != nil {
			logger.Error("AURP Peer: Couldn't send RI-Req", "error", err)
		}
		p.setRState(ReceiverWaitForRIRsp)
		p.Transport.ResetRemoteSeq()
		p.sendRetries.Store(0)
		p.lastSend.Store(time.Now())
		return nil

	case ReceiverWaitForRIRsp:
		p.earlyRIUpdates.Add(1)
		// Some deployed peers emit an RI-Upd while an RI-Rsp refresh is in
		// progress. RFC 1504 still requires valid sequenced data to be
		// acknowledged. Advance the sequence but do not mutate the partial
		// routing baseline; request the complete table again.
		if err := p.checkRemoteSeq(logger, &pkt.TrHeader, "RI-Upd"); err != nil {
			if err == errDropPacket {
				return nil
			}
			return err
		}
		if _, err := p.send(
			p.Transport.NewRIAckPacket(pkt.ConnectionID, pkt.Sequence, 0),
		); err != nil {
			logger.Error("AURP Peer: Couldn't acknowledge RI-Upd during RI-Rsp sync", "error", err)
			return err
		}
		p.earlyRIUpdateAcks.Add(1)
		p.Transport.IncRemoteSeq()
		p.sendRetries.Store(0)
		if _, err := p.send(p.Transport.NewRIReqPacket()); err != nil {
			logger.Error("AURP Peer: Couldn't re-request RI-Rsp after early RI-Upd", "error", err)
			return err
		}
		p.lastSend.Store(time.Now())
		logger.Info("AURP Peer: acknowledged early RI-Upd and re-requested complete RI-Rsp")
		return nil
	}

	if err := p.checkRemoteSeq(logger, &pkt.TrHeader, "RI-Upd"); err != nil {
		if err == errDropPacket {
			return nil
		}
		return err
	}

	var ackFlag aurp.RoutingFlag

	for _, et := range pkt.Events {
		logger := logger.With(
			"event-code", et.EventCode,
			"extended", et.Extended,
			"net-start", et.RangeStart,
			"net-end", et.RangeEnd,
			"distance", et.Distance,
		)
		logger.Debug("AURP Peer: RI-Upd event", "action", aurpRIUpdAction(et))

		needZoneInfo, err := p.applyRIUpdEvent(et)
		if err != nil {
			logger.Warn(
				"AURP Peer: RI-Upd event ignored",
				"error", err,
				"action", "no route mutation",
			)
			continue
		}
		if et.EventCode == aurp.EventCodeNA && et.Distance >= maxRouteDistance {
			logger.Info(
				"AURP Peer: RI-Upd NA ignored unreachable route tuple",
				"reason", "distance-15-or-higher",
				"action", "no route installed",
			)
		}
		if et.EventCode == aurp.EventCodeNDC && et.Distance >= maxRouteDistance {
			logger.Info(
				"AURP Peer: RI-Upd NDC treated as route deletion",
				"reason", "distance-15-or-higher",
				"action", "route removed if present",
			)
		}
		if needZoneInfo {
			p.markZoneInfoPending(et.RangeStart)
			ackFlag = aurp.RoutingFlagSendZoneInfo
		}
	}

	if _, err := p.send(p.Transport.NewRIAckPacket(pkt.ConnectionID, pkt.Sequence, ackFlag)); err != nil {
		logger.Error("AURP Peer: Couldn't send RI-Ack", "error", err)
		return err
	}
	p.Transport.IncRemoteSeq()

	return nil
}

func (p *AURPPeer) handleRD(logger *slog.Logger, pkt *aurp.RDPacket) error {
	// RFC 1504 permits RD from either side of a one-way connection:
	// data-sender RD is sequenced; data-receiver RD uses sequence 0.
	if pkt.Sequence == 0 {
		p.receiverRouterDowns.Add(1)
		// They are the data receiver; we are the data sender.
		if err := p.checkRemoteConnID(logger, &pkt.TrHeader); err != nil {
			if err == errDropPacket {
				return nil
			}
			return err
		}
		logger.Info(
			"AURP Peer: receiver-originated Router Down",
			"code", int(pkt.ErrorCode),
			"code-str", pkt.ErrorCode,
		)
		p.RouteTable.RemoveObserver(p)
		p.disconnectSender()
		return nil
	}

	// They are the data sender; we are the data receiver.
	p.senderRouterDowns.Add(1)
	if err := p.checkLocalConnID(logger, &pkt.TrHeader); err != nil {
		if err == errDropPacket {
			return nil
		}
		return err
	}
	if err := p.checkRemoteSeqWithAckFlags(logger, &pkt.TrHeader, "RD", 0); err != nil {
		if err == errDropPacket {
			return nil
		}
		return err
	}
	p.markReceiverAlive()

	logger.Info(
		"AURP Peer: sender-originated Router Down",
		"code", int(pkt.ErrorCode),
		"code-str", pkt.ErrorCode,
	)

	if _, err := p.send(
		p.Transport.NewRIAckPacket(pkt.ConnectionID, pkt.Sequence, 0),
	); err != nil {
		logger.Error("AURP Peer: Couldn't send RI-Ack", "error", err)
		return err
	}
	p.routerDownAcks.Add(1)
	p.Transport.IncRemoteSeq()
	p.disconnectReceiver()
	return nil
}

func (p *AURPPeer) handleZIReq(logger *slog.Logger, pkt *aurp.ZIReqPacket) error {
	// We are: sender
	// They are: receiver

	if err := p.checkRemoteConnID(logger, &pkt.TrHeader); err != nil {
		if err == errDropPacket {
			return nil
		}
		return err
	}

	visibleNetworks := make([]ddp.Network, 0, len(pkt.Networks))
	for _, network := range pkt.Networks {
		if !p.timing.networkHidden(network) {
			visibleNetworks = append(visibleNetworks, network)
		}
	}
	zones := p.RouteTable.ZonesForNetworks(visibleNetworks)
	if err := p.sendZIRspPackets(zones); err != nil {
		logger.Error("AURP Peer: Couldn't send ZI-Rsp packet", "error", err)
		return err
	}
	return nil
}

func (p *AURPPeer) handleZIRsp(logger *slog.Logger, pkt *aurp.ZIRspPacket) error {
	// We are: receiver
	// They are: sender

	if err := p.checkLocalConnID(logger, &pkt.TrHeader); err != nil {
		if err == errDropPacket {
			return nil
		}
		return err
	}
	p.markReceiverAlive()

	logger.Debug(
		"AURP Peer: Learned about these zones",
		"subcode", pkt.Subcode,
		"total-tuples", pkt.TotalTuples,
		"zones", pkt.Zones,
	)

	switch pkt.Subcode {
	case aurp.SubcodeZoneInfoNonExt:
		accepted, ignored := p.applyNonExtendedZIRsp(pkt)
		p.zoneTuplesAccepted.Add(uint64(accepted))
		p.zoneTuplesIgnored.Add(uint64(ignored))
		if ignored > 0 {
			logger.Warn(
				"AURP Peer: ignored zone tuples for networks not learned from this peer",
				"ignored", ignored,
				"accepted", accepted,
			)
		}

	case aurp.SubcodeZoneInfoExt:
		p.extendedZIFragments.Add(1)
		complete, network, err := p.applyExtendedZIRsp(pkt)
		if err != nil {
			logger.Warn(
				"AURP Peer: invalid extended ZI-Rsp",
				"network", network,
				"error", err,
			)
			return nil
		}
		if complete {
			p.extendedZICompleted.Add(1)
			logger.Debug(
				"AURP Peer: completed extended zone list",
				"network", network,
			)
		}

	default:
		logger.Warn(
			"AURP Peer: unknown ZI-Rsp subcode",
			"subcode", pkt.Subcode,
		)
	}
	return nil
}

func (p *AURPPeer) applyZIRspZone(zt aurp.ZoneTuple) bool {
	// Zone information is meaningful only for a network currently learned
	// from this receiver-side AURP connection. Without this ownership check,
	// a delayed or reflected ZI-Rsp can attach a peer's zone name to a route
	// currently owned by another peer.
	if p.RouteTable == nil || p.RouteTable.find(p, zt.Network).Zero() {
		return false
	}
	if err := p.RouteTable.AddZonesToNetwork(zt.Network, zt.Name); err != nil {
		return false
	}
	return true
}

func (p *AURPPeer) handleGDZLReq(logger *slog.Logger, pkt *aurp.GDZLReqPacket) error {
	// We are: sender
	// They are: receiver

	if err := p.checkRemoteConnID(logger, &pkt.TrHeader); err != nil {
		if err == errDropPacket {
			return nil
		}
		return err
	}

	if _, err := p.send(p.Transport.NewGDZLRspPacket(-1, nil)); err != nil {
		logger.Error("AURP Peer: Couldn't send GDZL-Rsp packet", "error", err)
		return err
	}
	return nil
}

func (p *AURPPeer) handleGDZLRsp(logger *slog.Logger, pkt *aurp.GDZLRspPacket) error {
	// We are: receiver
	// They are: sender

	if err := p.checkLocalConnID(logger, &pkt.TrHeader); err != nil {
		if err == errDropPacket {
			return nil
		}
		return err
	}

	logger.Warn("AURP Peer: Received a GDZL-Rsp, but I wouldn't have sent a GDZL-Req - so that's weird")
	return nil
}

func (p *AURPPeer) handleGZNReq(logger *slog.Logger, pkt *aurp.GZNReqPacket) error {
	// We are: sender
	// They are: receiver

	if err := p.checkRemoteConnID(logger, &pkt.TrHeader); err != nil {
		if err == errDropPacket {
			return nil
		}
		return err
	}

	if _, err := p.send(p.Transport.NewGZNRspPacket(pkt.ZoneName, false, nil)); err != nil {
		logger.Error("AURP Peer: Couldn't send GZN-Rsp packet", "error", err)
		return err
	}
	return nil
}

func (p *AURPPeer) handleGZNRsp(logger *slog.Logger, pkt *aurp.GZNRspPacket) error {
	// We are: receiver
	// They are: sender

	if err := p.checkLocalConnID(logger, &pkt.TrHeader); err != nil {
		if err == errDropPacket {
			return nil
		}
		return err
	}

	logger.Warn("AURP Peer: Received a GZN-Rsp, but I wouldn't have sent a GZN-Req - so that's weird")
	return nil
}

func (p *AURPPeer) handleTickle(logger *slog.Logger, pkt *aurp.TicklePacket) error {
	// We are: sender
	// They are: receiver

	if err := p.checkRemoteConnID(logger, &pkt.TrHeader); err != nil {
		if err == errDropPacket {
			return nil
		}
		return err
	}

	// Immediately respond with Tickle-Ack
	if _, err := p.send(p.Transport.NewTickleAckPacket()); err != nil {
		logger.Error("AURP Peer: Couldn't send Tickle-Ack", "error", err)
		return err
	}
	return nil
}

func (p *AURPPeer) handleTickleAck(logger *slog.Logger, pkt *aurp.TickleAckPacket) error {
	// We are: receiver
	// They are: sender

	if err := p.checkLocalConnID(logger, &pkt.TrHeader); err != nil {
		if err == errDropPacket {
			return nil
		}
		return err
	}

	switch rstate := p.ReceiverState(); rstate {
	case ReceiverWaitForTickleAck:
		p.markReceiverAlive()
		p.sendRetries.Store(0)
		p.setRState(ReceiverConnected)

	case ReceiverConnected:
		// A delayed or duplicated Tickle-Ack on the active connection still
		// demonstrates receiver-side liveness, but must not change state.
		p.lateTickleAcks.Add(1)
		p.markReceiverAlive()
		logger.Debug("AURP Peer: late or duplicate Tickle-Ack ignored")

	default:
		// Never let a stray Tickle-Ack bypass Open/RI synchronization.
		p.lateTickleAcks.Add(1)
		logger.Debug(
			"AURP Peer: stray Tickle-Ack ignored without changing receiver state",
			"receiver-state", rstate,
		)
	}
	return nil
}

// checkRemoteSeq checks the sequence number in the packet against the expected
// sequence number from the transport.
func (p *AURPPeer) checkRemoteSeq(logger *slog.Logger, trheader *aurp.TrHeader, packetName string) error {
	return p.checkRemoteSeqWithAckFlags(
		logger,
		trheader,
		packetName,
		aurp.RoutingFlagSendZoneInfo,
	)
}

func (p *AURPPeer) checkRemoteSeqWithAckFlags(
	logger *slog.Logger,
	trheader *aurp.TrHeader,
	packetName string,
	duplicateAckFlags aurp.RoutingFlag,
) error {
	switch got, want := trheader.Sequence, p.Transport.RemoteSeq(); got {
	case aurp.Pred(want):
		p.duplicateRoutingPackets.Add(1)
		// "If the data receiver expects sequence number n and
		// receives a packet with the sequence number n-1, that
		// packet was delayed and is a duplicate of another packet
		// already received. The data receiver must retransmit an
		// RI-Ack packet, because the data sender may not have
		// received the RI-Ack packet previously sent; that is, the
		// RI-Ack may have been lost."
		logger.Debug(
			"AURP Peer: duplicate sequenced routing packet; retransmitting RI-Ack",
			"packet", packetName,
			"packet-seq", got,
			"expected-seq", want,
			"action", "re-ack-and-drop",
		)
		if _, err := p.send(p.Transport.NewRIAckPacket(trheader.ConnectionID, trheader.Sequence, duplicateAckFlags)); err != nil {
			logger.Error("AURP Peer: Couldn't send RI-Ack packet", "error", err)
			return err
		}
		p.reacksSent.Add(1)
		return errDropPacket

	case want:
		// "Whenever the data receiver receives an RI-Rsp, RI-Upd,
		// or RD packet that has the expected sequence number and
		// connection ID..."
		// As expected. Continue.
		return nil

	case aurp.Succ(want):
		p.futureRoutingPackets.Add(1)
		// "If the data receiver expects sequence number n and
		// receives a packet with the sequence number n+1, it should
		// discard the packet and terminate the one-way connection
		// on which it is the data receiver. Because AURP-Tr
		// supports only one outstanding transaction at a time, the
		// receipt of such a packet indicates that the connection is
		// out of sync."

		logger.Warn(
			"AURP Peer: future sequenced routing packet; resetting receiver connection",
			"packet", packetName,
			"packet-seq", got,
			"expected-seq", want,
			"action", "reset-receiver",
		)
		p.disconnectReceiver()
		return errDropPacket

	default:
		p.staleRoutingPackets.Add(1)
		// "If the data receiver expects sequence number n and
		// receives a packet with a sequence number other than n-1,
		// n, or n+1, the packet was delayed and is a duplicate of
		// another packet already received. The data receiver need
		// not send an RI-Ack, because the data sender must have
		// received an RI-Ack for that sequence number prior to
		// sending a packet with the sequence number n-1. The data
		// receiver should discard the packet."
		logger.Debug(
			"AURP Peer: stale sequenced routing packet; discarding",
			"packet", packetName,
			"packet-seq", got,
			"expected-seq", want,
			"action", "drop-without-ack",
		)
		return errDropPacket
	}
}

// checkLocalConnID checks that the ConnectionID in the header matches the
// transport's LocalConnID.
func (p *AURPPeer) checkLocalConnID(logger *slog.Logger, trheader *aurp.TrHeader) error {
	got, want := trheader.ConnectionID, p.Transport.LocalConnID()
	// LocalConnID should always be set to something
	if got != want {
		p.connectionIDMismatches.Add(1)
		// "If the packet contains a connection ID that does not
		// match that expected for the connection, the exterior
		// outer discards the packet."
		logger.Warn("AURP Peer: connection ID mismatch, dropping packet", "want-conn-id", want)
		return errDropPacket
	}
	return nil
}

// checkRemoteConnID checks that the ConnectionID in the header matches the
// transport's RemoteConnID.
func (p *AURPPeer) checkRemoteConnID(logger *slog.Logger, trheader *aurp.TrHeader) error {
	got, want := trheader.ConnectionID, p.Transport.RemoteConnID()
	if want == 0 {
		// Connection not established yet, so it can be anything
		return nil
	}
	if got != want {
		p.connectionIDMismatches.Add(1)
		// "If the packet contains a connection ID that does not
		// match that expected for the connection, the exterior
		// outer discards the packet."
		logger.Warn("AURP Peer: connection ID mismatch, dropping packet", "want-conn-id", want)
		return errDropPacket
	}
	return nil
}

func (p *AURPPeer) setRState(rstate ReceiverState) { p.rstate.Store(int32(rstate)) }
func (p *AURPPeer) setSState(sstate SenderState)   { p.sstate.Store(int32(sstate)) }

func (p *AURPPeer) disconnectReceiver() {
	// Routes learned on the receiver-side one-way connection are candidates
	// owned by that connection. Once the receiver connection is lost, none of
	// them may remain eligible for forwarding; a later RI-Rsp rebuilds the
	// peer's route set from scratch.
	if p.RouteTable != nil {
		p.RouteTable.DeleteTarget(p)
	}

	// "When establishing a one-way connection with a given data sender, a
	// data receiver using AURP-Tr must send an Open-Req that has a
	// different connection ID from that used in its last connection with
	// the data sender." Hence, IncLocalConnID.
	p.Transport.ResetRemoteSeq()
	p.Transport.IncLocalConnID()
	p.pendingZoneInfo = nil
	p.setRState(ReceiverUnconnected)
}

func (p *AURPPeer) disconnectSender() {
	p.Transport.ResetLocalSeq()
	p.Transport.SetRemoteConnID(0)
	p.pendingRIRsp = nil
	p.pendingRIUpd = nil
	p.lastRISent = nil
	p.restartProbe = false
	p.pendingEventsMu.Lock()
	p.pendingEvents = nil
	p.pendingEventsMu.Unlock()
	p.setSState(SenderUnconnected)
}

func (p *AURPPeer) disconnect() {
	p.disconnectReceiver()
	p.disconnectSender()
}

// send encodes and sends pkt to the remote host.
func (p *AURPPeer) send(pkt aurp.Packet) (int, error) {
	// Record routing-type packets into the chatlog
	if rpkt, ok := pkt.(aurp.RoutingPacket); ok {
		p.addToChatLog(rpkt, true /* sent */)
	}

	var b bytes.Buffer
	if _, err := pkt.WriteTo(&b); err != nil {
		return 0, err
	}

	promLabels := prometheus.Labels{"peer": p.RemoteAddrString()}
	aurpPacketsOutCounter.With(promLabels).Inc()
	aurpBytesOutCounter.With(promLabels).Add(float64(b.Len()))

	p.logger.Debug("AURP Peer: Sending", "pkt-type", reflect.TypeOf(pkt), "length", b.Len())
	p.lastSend.Store(time.Now())
	return p.UDPConn.WriteToUDP(b.Bytes(), &net.UDPAddr{IP: p.RemoteAddr(), Port: 387})
}

func (p *AURPPeer) addToChatLog(pkt aurp.RoutingPacket, sent bool) {
	now := time.Now()
	p.chatLogMu.Lock()
	defer p.chatLogMu.Unlock()
	p.chatLog = append(p.chatLog, ChatLogEntry{
		Packet:    pkt,
		Sent:      sent,
		Timestamp: now,
	})
	p.chatLog = p.chatLog[max(0, len(p.chatLog)-chatLogLimit):]
}

type ReceiverState int32

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

type SenderState int32

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
