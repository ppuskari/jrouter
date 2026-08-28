/*
   Copyright 2025 Josh Deprez

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
	"cmp"
	"context"
	_ "embed"
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net"
	"net/http"
	"slices"
	"sync"
	"text/template"
	"time"

	"drjosh.dev/jrouter/aurp"
	"drjosh.dev/jrouter/status"
	"github.com/prometheus/client_golang/prometheus"
)

const (
	dnsBackoffBase = 30 * time.Second
	dnsBackoffCap  = 30 * time.Minute
	dnsJitterPct   = 10
)

var errDNSBackoff = errors.New("DNS lookup is in backoff")

type configuredDNSState struct {
	failures int
	next     time.Time
	kind     string
}

func dnsBackoff(failures int) time.Duration {
	if failures <= 0 {
		return 0
	}
	steps := []time.Duration{
		30 * time.Second,
		1 * time.Minute,
		2 * time.Minute,
		5 * time.Minute,
		10 * time.Minute,
		30 * time.Minute,
	}
	idx := min(failures-1, len(steps)-1)
	return min(steps[idx], dnsBackoffCap)
}

func jitterDNSBackoff(d time.Duration) time.Duration {
	if d <= 0 {
		return 0
	}
	span := int64(d) * dnsJitterPct / 100
	if span <= 0 {
		return d
	}
	return d + time.Duration(rand.Int64N(2*span+1)-span)
}

func dnsErrorKind(err error) string {
	var dnsErr *net.DNSError
	if !errors.As(err, &dnsErr) {
		return "error"
	}
	switch {
	case dnsErr.IsNotFound:
		return "not-found"
	case dnsErr.IsTimeout:
		return "timeout"
	case dnsErr.IsTemporary:
		return "temporary"
	default:
		return "dns-error"
	}
}

// AURPPeerTable tracks connections to AURP peers.
type AURPPeerTable struct {
	logger *slog.Logger

	mu                sync.RWMutex
	peersByIP         map[[4]byte]*AURPPeer // candidate IP -> logical peer
	peersByConfigured map[string]*AURPPeer  // configured name -> logical peer
	dnsByConfigured   map[string]configuredDNSState
	nextConnID        uint16
}

// NewAURPPeerTable creates a new AURP peer table.
func NewAURPPeerTable(ctx context.Context, logger *slog.Logger) *AURPPeerTable {
	t := &AURPPeerTable{
		logger:            logger,
		peersByIP:         make(map[[4]byte]*AURPPeer),
		peersByConfigured: make(map[string]*AURPPeer),
		dnsByConfigured:   make(map[string]configuredDNSState),
	}
	for t.nextConnID == 0 {
		t.nextConnID = uint16(rand.UintN(0x10000))
	}
	status.AddItem(ctx, "AURP Peers", peerTableTemplate, t.status)
	prometheus.MustRegister(t)
	return t
}

// TrackConfiguredAddress records a configured peer even if its DNS lookup
// has not succeeded yet, so startup DNS failures remain eligible for retry.
func (t *AURPPeerTable) TrackConfiguredAddress(peerAddr string) {
	if peerAddr == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, ok := t.dnsByConfigured[peerAddr]; !ok {
		t.dnsByConfigured[peerAddr] = configuredDNSState{}
	}
}

func (t *AURPPeerTable) dnsReady(peerAddr string, now time.Time) bool {
	t.mu.RLock()
	defer t.mu.RUnlock()
	state := t.dnsByConfigured[peerAddr]
	return state.next.IsZero() || !now.Before(state.next)
}

func (t *AURPPeerTable) noteDNSFailure(peerAddr string, now time.Time, err error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	state := t.dnsByConfigured[peerAddr]
	state.failures++
	state.kind = dnsErrorKind(err)
	delay := jitterDNSBackoff(dnsBackoff(state.failures))
	state.next = now.Add(delay)
	t.dnsByConfigured[peerAddr] = state
	t.logger.Info(
		"AURP Peer: DNS retry backoff scheduled",
		"configured-addr", peerAddr,
		"kind", state.kind,
		"failures", state.failures,
		"delay", delay,
		"next-dns", state.next,
	)
}

func (t *AURPPeerTable) resetDNSBackoff(peerAddr string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	state := t.dnsByConfigured[peerAddr]
	state.failures = 0
	state.next = time.Time{}
	state.kind = ""
	t.dnsByConfigured[peerAddr] = state
}

func (t *AURPPeerTable) configuredAddresses() []string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	out := make([]string, 0, len(t.dnsByConfigured))
	for peerAddr := range t.dnsByConfigured {
		out = append(out, peerAddr)
	}
	return out
}

func (t *AURPPeerTable) configuredPeer(peerAddr string) *AURPPeer {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.peersByConfigured[peerAddr]
}

// RunAll runs all peer handlers in goroutines.
func (t *AURPPeerTable) RunAll(ctx context.Context, wg *sync.WaitGroup) {
	t.mu.RLock()
	peers := t.uniquePeersLocked()
	t.mu.RUnlock()
	for _, peer := range peers {
		wg.Go(func() { peer.Handle(ctx) })
	}
}

func (t *AURPPeerTable) uniquePeersLocked() []*AURPPeer {
	seen := make(map[*AURPPeer]struct{}, len(t.peersByIP))
	peers := make([]*AURPPeer, 0, len(t.peersByIP))
	for _, peer := range t.peersByIP {
		if _, ok := seen[peer]; ok {
			continue
		}
		seen[peer] = struct{}{}
		peers = append(peers, peer)
	}
	return peers
}

// LookupOrCreate looks up a peer by raddr, or creates a peer if it is not
// found. Configured hostnames have one logical AURPPeer even when DNS returns
// multiple IPv4 candidate addresses. It returns an error if raddr is not IPv4.
func (t *AURPPeerTable) LookupOrCreate(
	ctx context.Context,
	logger *slog.Logger,
	routes *RouteTable,
	udpConn *net.UDPConn,
	peerAddr string,
	raddr net.IP,
	localDI, remoteDI aurp.DomainIdentifier,
) (*AURPPeer, error) {
	raddr4 := raddr.To4()
	if len(raddr4) != 4 {
		return nil, fmt.Errorf("remote addr %v is not an IPv4 address", raddr)
	}
	key := [4]byte(raddr4)

	t.mu.Lock()
	defer t.mu.Unlock()

	if peerAddr != "" {
		if _, ok := t.dnsByConfigured[peerAddr]; !ok {
			t.dnsByConfigured[peerAddr] = configuredDNSState{}
		}
		if peer := t.peersByConfigured[peerAddr]; peer != nil {
			if other := t.peersByIP[key]; other != nil && other != peer {
				return nil, fmt.Errorf(
					"configured peer %q candidate %v already belongs to %q",
					peerAddr, raddr4, other.ConfiguredAddr,
				)
			}
			t.peersByIP[key] = peer
			return peer, nil
		}
	}

	if peer := t.peersByIP[key]; peer != nil {
		if peerAddr == "" {
			return peer, nil
		}
		if peer.ConfiguredAddr != "" && peer.ConfiguredAddr != peerAddr {
			return nil, fmt.Errorf(
				"configured peer %q candidate %v already belongs to %q",
				peerAddr, raddr4, peer.ConfiguredAddr,
			)
		}
		peer.ConfiguredAddr = peerAddr
		t.peersByConfigured[peerAddr] = peer
		return peer, nil
	}

	if remoteDI == nil {
		remoteDI = aurp.IPDomainIdentifier(raddr4)
	}
	peer := &AURPPeer{
		Transport:      aurp.NewTransport(localDI, remoteDI, t.nextConnID, 0),
		UDPConn:        udpConn,
		ConfiguredAddr: peerAddr,
		ReceiveCh:      make(chan aurp.RoutingPacket, 1024),
		RouteTable:     routes,

		logger:      logger.With("raddr", raddr4, "remote-di", remoteDI),
		reconnectCh: make(chan struct{}, 1),
	}
	peer.setRemoteAddr(raddr4)

	t.peersByIP[key] = peer
	if peerAddr != "" {
		t.peersByConfigured[peerAddr] = peer
	}
	t.nextConnID = aurp.Succ(t.nextConnID)

	return peer, nil
}

// Lookup looks up the peer associated with this IP address. It returns an error
// if the address is not an IPv4 address.
func (t *AURPPeerTable) Lookup(raddr net.IP) (*AURPPeer, error) {
	raddr4 := raddr.To4()
	if len(raddr4) != 4 {
		return nil, fmt.Errorf("remote addr %v is not an IPv4 address", raddr)
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.peersByIP[[4]byte(raddr4)], nil
}

// ServeHTTP serves diagnostic pages for AURP peers, such as the chatlog.
func (t *AURPPeerTable) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Only the chat log so far
	ipStr := r.PathValue("ip")
	peer, err := t.Lookup(net.ParseIP(ipStr))
	if err != nil {
		http.Error(w, fmt.Sprintf("invalid address %q: %v", ipStr, err), http.StatusNotFound)
		return
	}
	if peer == nil {
		http.Error(w, fmt.Sprintf("peer %q not found", ipStr), http.StatusNotFound)
		return
	}

	if err := chatLogTmpl.Execute(w, peer); err != nil {
		t.logger.Error("Executing chatlog template", "error", err)
	}
}

func (t *AURPPeerTable) status(ctx context.Context) (any, error) {
	var peerInfo []*AURPPeer
	func() {
		t.mu.RLock()
		defer t.mu.RUnlock()
		peerInfo = t.uniquePeersLocked()
	}()
	slices.SortFunc(peerInfo, func(pa, pb *AURPPeer) int {
		return cmp.Or(
			-cmp.Compare(
				bool2Int(pa.ReceiverState() == ReceiverConnected),
				bool2Int(pb.ReceiverState() == ReceiverConnected),
			),
			-cmp.Compare(
				bool2Int(pa.SenderState() == SenderConnected),
				bool2Int(pb.SenderState() == SenderConnected),
			),
			cmp.Compare(pa.ConfiguredAddr, pb.ConfiguredAddr),
			cmp.Compare(
				binary.BigEndian.Uint32(pa.RemoteAddr()),
				binary.BigEndian.Uint32(pb.RemoteAddr()),
			),
		)
	})
	return peerInfo, nil
}

func bool2Int(b bool) int {
	if b {
		return 1
	}
	return 0
}

// ResolveConfiguredPeer resolves one configured hostname using DNS backoff and
// creates or refreshes its logical AURP peer.
func (t *AURPPeerTable) ResolveConfiguredPeer(
	ctx context.Context,
	logger *slog.Logger,
	routes *RouteTable,
	udpConn *net.UDPConn,
	peerAddr string,
	localDI aurp.DomainIdentifier,
) (*AURPPeer, error) {
	if !t.dnsReady(peerAddr, time.Now()) {
		return nil, errDNSBackoff
	}

	resolved, err := net.LookupIP(peerAddr)
	if err != nil {
		t.noteDNSFailure(peerAddr, time.Now(), err)
		return nil, err
	}
	candidates := normalizeIPv4Candidates(resolved)
	if len(candidates) == 0 {
		err := fmt.Errorf("configured peer %q has no IPv4 candidates", peerAddr)
		t.noteDNSFailure(peerAddr, time.Now(), err)
		return nil, err
	}
	t.resetDNSBackoff(peerAddr)

	var peer *AURPPeer
	for _, raddr4 := range candidates {
		if net.IP(localDI).Equal(raddr4) {
			continue
		}
		p, err := t.LookupOrCreate(
			ctx, logger, routes, udpConn, peerAddr, raddr4, localDI, nil,
		)
		if err != nil {
			return nil, err
		}
		if peer == nil {
			peer = p
		}
	}
	if peer == nil {
		return nil, fmt.Errorf("configured peer %q resolved only to local address", peerAddr)
	}
	return peer, nil
}

// PeriodicallyAttemptConnections scans the peer table every 10 seconds looking
// for configured peers that are disconnected, and attempts to connect them.
func (t *AURPPeerTable) PeriodicallyAttemptConnections(ctx context.Context, logger *slog.Logger, wg *sync.WaitGroup, routes *RouteTable, udpConn *net.UDPConn, localDI aurp.DomainIdentifier) {
	ctx, setStatus, _ := status.AddSimpleItem(ctx, "Periodically Attempt Connections")
	setStatus("Running")
	defer setStatus("Stopped!")

	scanTicker := time.Tick(10 * time.Second)

	for {
		select {
		case <-ctx.Done():
			return

		case <-scanTicker:
			// continue below
		}

		for _, peerAddr := range t.configuredAddresses() {
			peer := t.configuredPeer(peerAddr)
			if peer == nil {
				if !t.dnsReady(peerAddr, time.Now()) {
					continue
				}
				resolvedPeer, err := t.ResolveConfiguredPeer(
					ctx, logger, routes, udpConn, peerAddr, localDI,
				)
				if err != nil {
					if !errors.Is(err, errDNSBackoff) {
						logger.Warn("AURP Peer: DNS resolution retry failed", "configured-addr", peerAddr, "error", err)
					}
					continue
				}
				if resolvedPeer != nil && !resolvedPeer.Running() {
					wg.Go(func() { resolvedPeer.Handle(ctx) })
				}
				continue
			}

			if peer.ReceiverState() == ReceiverUnconnected {
				t.reconnectPeer(ctx, logger, wg, peer)
			}
		}
	}
}

func normalizeIPv4Candidates(addrs []net.IP) []net.IP {
	seen := make(map[[4]byte]struct{}, len(addrs))
	out := make([]net.IP, 0, len(addrs))
	for _, addr := range addrs {
		addr4 := addr.To4()
		if addr4 == nil {
			continue
		}
		key := [4]byte(addr4)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, append(net.IP(nil), addr4...))
	}
	return out
}

// setConfiguredCandidates refreshes the IP aliases for one configured logical
// peer. DNS reordering leaves the active endpoint untouched. If the active
// endpoint disappears, another candidate becomes active without creating a
// second AURPPeer or changing its AURP transport identity.
func (t *AURPPeerTable) setConfiguredCandidates(
	peer *AURPPeer,
	addrs []net.IP,
) (bool, error) {
	candidates := normalizeIPv4Candidates(addrs)
	if peer.ConfiguredAddr == "" {
		return false, fmt.Errorf("peer has no configured address")
	}
	if len(candidates) == 0 {
		return false, fmt.Errorf("configured peer %q has no IPv4 candidates", peer.ConfiguredAddr)
	}

	desired := make(map[[4]byte]struct{}, len(candidates))
	for _, addr := range candidates {
		desired[[4]byte(addr)] = struct{}{}
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	if configured := t.peersByConfigured[peer.ConfiguredAddr]; configured != peer {
		return false, fmt.Errorf("configured peer mapping changed for %q", peer.ConfiguredAddr)
	}

	for key := range desired {
		if other := t.peersByIP[key]; other != nil && other != peer {
			return false, fmt.Errorf(
				"configured peer %q candidate %v already belongs to %q",
				peer.ConfiguredAddr, net.IP(key[:]), other.ConfiguredAddr,
			)
		}
	}

	for key, mapped := range t.peersByIP {
		if mapped != peer {
			continue
		}
		if _, keep := desired[key]; !keep {
			delete(t.peersByIP, key)
		}
	}
	for key := range desired {
		t.peersByIP[key] = peer
	}

	active := peer.RemoteAddr()
	if active4 := active.To4(); active4 != nil {
		if _, keep := desired[[4]byte(active4)]; keep {
			return false, nil
		}
	}

	if peer.RouteTable != nil {
		peer.RouteTable.DeleteTarget(peer)
	}
	peer.setRemoteAddr(candidates[0])
	// A newly selected DNS endpoint deserves an immediate attempt even if
	// the previous endpoint was in reconnect backoff. Preserve the failure
	// count so another failure continues the progressive schedule.
	peer.nextReconnect.Store(time.Time{})
	return true, nil
}

func signalReconnect(peer *AURPPeer) {
	select {
	case peer.reconnectCh <- struct{}{}:
	default:
	}
}

func (t *AURPPeerTable) reconnectPeer(ctx context.Context, logger *slog.Logger, wg *sync.WaitGroup, peer *AURPPeer) error {
	if peer.ConfiguredAddr == "" {
		return nil
	}

	if !t.dnsReady(peer.ConfiguredAddr, time.Now()) {
		return nil
	}
	resolved, err := net.LookupIP(peer.ConfiguredAddr)
	if err != nil {
		t.noteDNSFailure(peer.ConfiguredAddr, time.Now(), err)
		logger.Warn("Couldn't resolve UDP address, skipping", "configured-addr", peer.ConfiguredAddr, "error", err)
		return nil
	}
	candidates := normalizeIPv4Candidates(resolved)
	if len(candidates) == 0 {
		err := fmt.Errorf("resolved peer has no IPv4 addresses")
		t.noteDNSFailure(peer.ConfiguredAddr, time.Now(), err)
		logger.Warn("Resolved peer has no IPv4 addresses, skipping", "configured-addr", peer.ConfiguredAddr)
		return nil
	}
	t.resetDNSBackoff(peer.ConfiguredAddr)

	switched, err := t.setConfiguredCandidates(peer, candidates)
	if err != nil {
		logger.Warn("AURP Peer: candidate refresh", "configured-addr", peer.ConfiguredAddr, "error", err)
		return nil
	}
	if switched {
		logger.Info(
			"AURP Peer: active DNS endpoint changed",
			"configured-addr", peer.ConfiguredAddr,
			"raddr", peer.RemoteAddr(),
		)
	}

	if peer.Running() {
		signalReconnect(peer)
		return nil
	}

	// Not running. The handle loop sends an Open-Req on startup.
	wg.Go(func() { peer.Handle(ctx) })
	return nil
}

//go:embed chatlog.html.tmpl
var chatLogTmplSrc string

var chatLogTmpl = template.Must(template.New("chatlog").Funcs(status.FuncMap()).Parse(chatLogTmplSrc))

const peerTableTemplate = `
<table>
	<thead><tr>
		<th>Configured addr</th>
		<th>Remote addr</th>
		<th>Running?</th>
		<th>Receiver state</th>
		<th>Sender state</th>
		<th>RecvCh len</th>
		<th>Last heard from</th>
		<th>Last reconnect</th>
		<th>Last update</th>
		<th>Last send</th>
		<th>Send retries</th>
	</tr></thead>
	<tbody>
{{range $peer := . }}
	<tr>
		<td>{{$peer.ConfiguredAddr}}</td>
		<td><a href="/chatlog/{{$peer.RemoteAddrString}}">{{$peer.RemoteAddrString}}</a></td>
		<td class="{{if $peer.Running}}green{{else}}red{{end}}">{{if $peer.Running}}running{{else}}stopped{{end}}</td>
		<td class="{{if $peer.ReceiverConnected}}green{{else}}red{{end}}">{{$peer.ReceiverState}}</td>
		<td class="{{if $peer.SenderConnected}}green{{else}}red{{end}}">{{$peer.SenderState}}</td>
		<td>{{$peer.ReceiveChLen}}</td>
		<td>{{$peer.LastHeardFrom | ago}}</td>
		<td>{{$peer.LastReconnect | ago}}</td>
		<td>{{$peer.LastUpdate | ago}}</td>
		<td>{{$peer.LastSend | ago}}</td>
		<td>{{$peer.SendRetries}}</td>
	</tr>
{{end}}
	</tbody>
</table>
`
