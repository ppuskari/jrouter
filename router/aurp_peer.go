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
	"log/slog"
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
	lastHeardFromTimer = 90 * time.Second
	tickleRetryLimit   = 10
	sendRetryTimer     = 10 * time.Second
	sendRetryLimit     = 5
	reconnectTimer     = 10 * time.Minute
	updateTimer        = 10 * time.Second

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

	// The resolved address of the peer.
	// NOTE: The UDP port is always assumed to be 387.
	RemoteAddr net.IP

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
	pendingRIRsp []aurp.NetworkTuples
	pendingRIUpd []aurp.EventTuples

	// The logger.
	logger *slog.Logger

	// The internal states below are only set within the Handle loop, but can
	// be read concurrently from outside (e.g. status, metrics).
	running       atomic.Bool
	rstate        atomic.Int32 // ReceiverState
	sstate        atomic.Int32 // SenderState
	lastReconnect atomic.Value // time.Time
	lastHeardFrom atomic.Value // time.Time
	lastSend      atomic.Value // time.Time // TODO: clarify use of lastSend / sendRetries
	lastUpdate    atomic.Value // time.Time
	sendRetries   atomic.Int32

	// Used for debugging AURP conversations.
	chatLogMu sync.RWMutex
	chatLog   []ChatLogEntry

	// Other bits of internal state
	lastRISent aurp.RoutingPacket
}

// ChatLogEntry is a record of a packet either sent or received and a timestamp.
// It's used for logging AURP conversations for diagnosis.
type ChatLogEntry struct {
	Packet    aurp.RoutingPacket
	Sent      bool // as opposed to Received
	Timestamp time.Time
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
	// same exported view it started with, discard it entirely.
	if _, advertise := aurpEventForBestTransition(change.before, change.after); !advertise {
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

// RouteTargetKey returns "AURPPeer|peer's IP address".
func (p *AURPPeer) RouteTargetKey() string {
	return "AURPPeer|" + p.RemoteAddr.String()
}

// Class returns TargetClassAURPPeer.
func (p *AURPPeer) Class() TargetClass { return TargetClassAURPPeer }

func (p *AURPPeer) String() string {
	return p.RemoteAddr.String()
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
		p.logger.Debug("AURP: handle loop for peer already running", "raddr", p.RemoteAddr)
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
			if p.SenderState() == SenderUnconnected {
				// Return immediately
				return
			}
			// Send a best-effort Router Down before returning
			p.lastRISent = p.Transport.NewRDPacket(aurp.ErrCodeNormalClose)
			if _, err := p.send(p.lastRISent); err != nil {
				p.logger.Error("Couldn't send RD packet", "error", err)
			}
			return

		case <-p.reconnectCh:
			if p.ReceiverState() != ReceiverUnconnected || time.Since(p.LastReconnect()) <= reconnectTimer {
				continue
			}
			now := time.Now()
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

func (p *AURPPeer) rtickerTasks() error {
	switch p.ReceiverState() {
	case ReceiverWaitForOpenRsp:
		if time.Since(p.LastSend()) <= sendRetryTimer {
			break
		}
		if p.SendRetries() >= sendRetryLimit {
			p.logger.Warn("AURP Peer: Send retry limit reached while waiting for Open-Rsp, closing connection")
			p.disconnectReceiver()
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
		if time.Since(p.LastHeardFrom()) <= lastHeardFromTimer {
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
		if time.Since(p.LastSend()) <= sendRetryTimer {
			break
		}
		if p.SendRetries() >= tickleRetryLimit {
			p.logger.Warn("AURP Peer: Send retry limit reached while waiting for Tickle-Ack, closing connection")
			p.disconnectReceiver()
			p.RouteTable.DeleteTarget(p)
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
		if time.Since(p.LastSend()) <= sendRetryTimer {
			break
		}
		if p.SendRetries() >= sendRetryLimit {
			p.logger.Warn("AURP Peer: Send retry limit reached while waiting for RI-Rsp, closing connection")
			p.disconnectReceiver()
			p.RouteTable.DeleteTarget(p)
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
		if p.SenderState() == SenderConnected && time.Since(p.LastSend()) > sendRetryTimer {
			if p.SendRetries() >= sendRetryLimit {
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
		if time.Since(p.LastSend()) <= sendRetryTimer {
			break
		}
		if p.lastRISent == nil {
			p.logger.Error("AURP Peer: sender retry: lastRISent = nil?")
			break
		}
		if p.SendRetries() >= sendRetryLimit {
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
		if time.Since(p.LastSend()) <= sendRetryTimer {
			break
		}
		p.setSState(SenderUnconnected)
		p.RouteTable.RemoveObserver(p)
	}

	return nil
}

func (p *AURPPeer) handlePacket(pkt aurp.RoutingPacket) error {
	now := time.Now()
	p.lastHeardFrom.Store(now)

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

	if sstate := p.SenderState(); sstate != SenderUnconnected {
		logger.Warn("AURP Peer: Open-Req received but sender state is not unconnected")
	}

	// TODO: implement the following
	//
	// "If a data sender receives an Open-Req from an exterior router with which
	// it already has a connection and the connection ID does not match that for
	// the connection already established, it should not discard the packet
	// without verifying whether the connection is still active. The receipt of
	// such a packet may indicate that the data receiver on the connection has
	// been restarted and has opened a new one-way connection, without first
	// terminating its original connection. The exterior router acting as the
	// data sender should send a null RI-Upd over the connection to determine
	// whether it is still active. If the data sender receives an RI-Ack in
	// response to the null RI-Upd, it discards the Open-Req and the original
	// connection remains active. If the data sender receives no RI-Ack after
	// retransmitting the null RI-Upd, it closes the original connection, then
	// sends an Open-Rsp to the next Open-Req received."

	// The peer tells us their connection ID in Open-Req.
	p.Transport.SetRemoteConnID(pkt.ConnectionID)

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

	// TODO: Load ExtraAdvertisedZones and HiddenZones. The base exported route
	// set is deliberately built in one place so initial RI-Rsp and incremental
	// RI-Upd split-horizon policy cannot drift apart.
	routes := p.RouteTable.aurpExportedRoutes()
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

	if p.ReceiverState() != ReceiverWaitForRIRsp {
		logger.Warn("Received RI-Rsp but was not waiting for one")
	}

	if err := p.checkRemoteSeq(logger, &pkt.TrHeader); err != nil {
		if err == errDropPacket {
			return nil
		}
		return err
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
			logger.Error("AURP Peer: RI-Rsp: couldn't upsert a route", "error", err)
			continue
		}
		if !accepted {
			logger.Info("AURP Peer: RI-Rsp: skipping route because distance is too high")
			continue
		}
	}

	// TODO: track which networks we don't have zone info for, and
	// only set SZI for those ?
	if _, err := p.send(p.Transport.NewRIAckPacket(pkt.ConnectionID, pkt.Sequence, aurp.RoutingFlagSendZoneInfo)); err != nil {
		logger.Error("AURP Peer: Couldn't send RI-Ack packet", "error", err)
		return err
	}
	if pkt.Flags&aurp.RoutingFlagLast != 0 {
		// No longer waiting for an RI-Rsp
		p.setRState(ReceiverConnected)
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
	case SenderWaitForRDAck:
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
		if _, err := p.send(p.Transport.NewZIRspPacket(zones)); err != nil {
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
	switch et.EventCode {
	case aurp.EventCodeNull, aurp.EventCodeZC:
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

		return false, p.RouteTable.UpdateDistance(
			p, et.RangeStart, et.Distance+1,
		)
	}
	return false, nil
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

	switch rstate := p.ReceiverState(); rstate {
	case ReceiverConnected:
		// Business as usual.

	case ReceiverUnconnected, ReceiverWaitForOpenRsp:
		logger.Error("AURP Peer: Got an RI-Upd while not in Connected state")
		// Remote thinks we are connected, but we are not, or we are starting
		// from the beginning. Try an RI-Req, jump to WaitForRIRsp state, and
		// don't ack or use the RI-Upd.
		if _, err := p.send(p.Transport.NewRIReqPacket()); err != nil {
			logger.Error("AURP Peer: Couldn't send RI-Req", "error", err)
		}
		p.setRState(ReceiverWaitForRIRsp)
		// restart the receiving sequence
		p.Transport.ResetRemoteSeq()
		return nil

	case ReceiverWaitForRIRsp, ReceiverWaitForTickleAck:
		logger.Error("AURP Peer: Got an RI-Upd while not in Connected state")
		return nil
	}

	if err := p.checkRemoteSeq(logger, &pkt.TrHeader); err != nil {
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
		logger.Debug("AURP Peer: RI-Upd event")

		needZoneInfo, err := p.applyRIUpdEvent(et)
		if err != nil {
			logger.Error("AURP Peer: RI-Upd event couldn't be applied", "error", err)
			continue
		}
		if needZoneInfo {
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
	// We are: sender
	// They are: receiver

	if err := p.checkRemoteConnID(logger, &pkt.TrHeader); err != nil {
		if err == errDropPacket {
			return nil
		}
		return err
	}

	if rstate := p.ReceiverState(); rstate == ReceiverUnconnected || rstate == ReceiverWaitForOpenRsp {
		logger.Error("AURP Peer: Received RD but was not expecting one")
	}

	// TODO: check sequence number
	// "Whenever the data receiver receives an RI-Rsp, RI-Upd, or RD packet
	// that has the expected sequence number and connection ID..."

	logger.Info("AURP Peer: Router Down", "code", int(pkt.ErrorCode), "code-str", pkt.ErrorCode)
	p.RouteTable.DeleteTarget(p)

	// Respond with RI-Ack
	if _, err := p.send(p.Transport.NewRIAckPacket(pkt.ConnectionID, pkt.Sequence, 0)); err != nil {
		logger.Error("AURP Peer: Couldn't send RI-Ack", "error", err)
		return err
	}
	// Connections closed
	p.disconnect()
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

	// TODO: split ZI-Rsp packets similarly to ZIP Replies
	zones := p.RouteTable.ZonesForNetworks(pkt.Networks)
	if _, err := p.send(p.Transport.NewZIRspPacket(zones)); err != nil {
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

	logger.Debug("AURP Peer: Learned about these zones", "zones", pkt.Zones)
	for _, zt := range pkt.Zones {
		// Filter out our own networks, because we manage those.
		// (A peer that is reflecting routes is probably reflecting zones too.)
		route := p.RouteTable.Lookup(zt.Network)
		if route.Target != nil && route.Target.Class() == TargetClassDirect {
			continue
		}
		p.RouteTable.AddZonesToNetwork(zt.Network, zt.Name)
	}
	return nil
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

	if rstate := p.ReceiverState(); rstate != ReceiverWaitForTickleAck {
		logger.Warn("AURP Peer: Received Tickle-Ack but was not waiting for one")
	}
	p.setRState(ReceiverConnected)
	return nil
}

// checkRemoteSeq checks the sequence number in the packet against the expected
// sequence number from the transport.
func (p *AURPPeer) checkRemoteSeq(logger *slog.Logger, trheader *aurp.TrHeader) error {
	switch got, want := trheader.Sequence, p.Transport.RemoteSeq(); got {
	case aurp.Pred(want):
		// "If the data receiver expects sequence number n and
		// receives a packet with the sequence number n–1, that
		// packet was delayed and is a duplicate of another packet
		// already received. The data receiver must retransmit an
		// RI-Ack packet, because the data sender may not have
		// received the RI-Ack packet previously sent—that is, the
		// RI-Ack may have been lost."
		logger.Warn("AURP Peer: repeated routing information packet", "want-seq", want)
		if _, err := p.send(p.Transport.NewRIAckPacket(trheader.ConnectionID, trheader.Sequence, aurp.RoutingFlagSendZoneInfo)); err != nil {
			logger.Error("AURP Peer: Couldn't send RI-Ack packet", "error", err)
			return err
		}
		return errDropPacket

	case want:
		// "Whenever the data receiver receives an RI-Rsp, RI-Upd,
		// or RD packet that has the expected sequence number and
		// connection ID..."
		// As expected. Continue.
		return nil

	case aurp.Succ(want):
		// "If the data receiver expects sequence number n and
		// receives a packet with the sequence number n+1, it should
		// discard the packet and terminate the one-way connection
		// on which it is the data receiver. Because AURP-Tr
		// supports only one outstanding transaction at a time, the
		// receipt of such a packet indicates that the connection is
		// out of sync."

		logger.Warn("AURP Peer: routing information packet out of sequence, resetting connection", "want-seq", want)
		p.disconnectReceiver()
		return errDropPacket

	default:
		// "If the data receiver expects sequence number n and
		// receives a packet with a sequence number other than n–1,
		// n, or n+1, the packet was delayed and is a duplicate of
		// another packet already received. The data receiver need
		// not send an RI-Ack, because the data sender must have
		// received an RI-Ack for that sequence number prior to
		// sending a packet with the sequence number n–1. The data
		// receiver should discard the packet."
		logger.Warn("AURP Peer: routing information packet out of sequence, discarding packet", "want-seq", want)
		return errDropPacket
	}
}

// checkLocalConnID checks that the ConnectionID in the header matches the
// transport's LocalConnID.
func (p *AURPPeer) checkLocalConnID(logger *slog.Logger, trheader *aurp.TrHeader) error {
	got, want := trheader.ConnectionID, p.Transport.LocalConnID()
	// LocalConnID should always be set to something
	if got != want {
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
	// "When establishing a one-way connection with a given data sender, a
	// data receiver using AURP-Tr must send an Open-Req that has a
	// different connection ID from that used in its last connection with
	// the data sender." Hence, IncLocalConnID.
	p.Transport.ResetRemoteSeq()
	p.Transport.IncLocalConnID()
	p.setRState(ReceiverUnconnected)
}

func (p *AURPPeer) disconnectSender() {
	p.Transport.ResetLocalSeq()
	p.Transport.SetRemoteConnID(0)
	p.pendingRIRsp = nil
	p.pendingRIUpd = nil
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

	promLabels := prometheus.Labels{"peer": p.RemoteAddr.String()}
	aurpPacketsOutCounter.With(promLabels).Inc()
	aurpBytesOutCounter.With(promLabels).Add(float64(b.Len()))

	p.logger.Debug("AURP Peer: Sending", "pkt-type", reflect.TypeOf(pkt), "length", b.Len())
	p.lastSend.Store(time.Now())
	return p.UDPConn.WriteToUDP(b.Bytes(), &net.UDPAddr{IP: p.RemoteAddr, Port: 387})
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
