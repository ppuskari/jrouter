package router

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"testing"
	"time"

	"drjosh.dev/jrouter/aurp"
	"github.com/prometheus/client_golang/prometheus"
)

func newTestAURPPeerTable() *AURPPeerTable {
	return &AURPPeerTable{
		logger:            slog.New(slog.NewTextHandler(io.Discard, nil)),
		peersByIP:         make(map[[4]byte]*AURPPeer),
		peersByConfigured: make(map[string]*AURPPeer),
		dnsByConfigured:   make(map[string]configuredDNSState),
		nextConnID:        1,
	}
}

func testPeerIPs() (net.IP, net.IP, net.IP) {
	return net.IPv4(104, 21, 23, 127),
		net.IPv4(172, 67, 211, 24),
		net.IPv4(203, 0, 113, 7)
}

func TestAURPConfiguredPeerSharesDNSCandidates(t *testing.T) {
	table := newTestAURPPeerTable()
	logger := table.logger
	localDI := aurp.IPDomainIdentifier(net.IPv4(192, 0, 2, 1))
	ip1, ip2, _ := testPeerIPs()

	p1, err := table.LookupOrCreate(
		context.Background(), logger, nil, nil,
		"peer.example", ip1, localDI, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	p2, err := table.LookupOrCreate(
		context.Background(), logger, nil, nil,
		"peer.example", ip2, localDI, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if p1 != p2 {
		t.Fatal("multi-A configured hostname created two logical peers")
	}

	for _, ip := range []net.IP{ip1, ip2} {
		got, err := table.Lookup(ip)
		if err != nil {
			t.Fatal(err)
		}
		if got != p1 {
			t.Fatalf("candidate %v mapped to %p, want %p", ip, got, p1)
		}
	}

	table.mu.RLock()
	unique := table.uniquePeersLocked()
	table.mu.RUnlock()
	if len(unique) != 1 {
		t.Fatalf("unique peer count = %d, want 1", len(unique))
	}
}

func TestAURPConfiguredPeerDNSReorderKeepsActiveEndpoint(t *testing.T) {
	table := newTestAURPPeerTable()
	localDI := aurp.IPDomainIdentifier(net.IPv4(192, 0, 2, 1))
	ip1, ip2, _ := testPeerIPs()

	peer, err := table.LookupOrCreate(
		context.Background(), table.logger, nil, nil,
		"peer.example", ip1, localDI, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := table.LookupOrCreate(
		context.Background(), table.logger, nil, nil,
		"peer.example", ip2, localDI, nil,
	); err != nil {
		t.Fatal(err)
	}

	switched, err := table.setConfiguredCandidates(peer, []net.IP{ip2, ip1})
	if err != nil {
		t.Fatal(err)
	}
	if switched {
		t.Fatal("DNS reorder switched an active endpoint that is still valid")
	}
	if !peer.RemoteAddr().Equal(ip1) {
		t.Fatalf("active endpoint = %v, want %v", peer.RemoteAddr(), ip1)
	}
}

func TestAURPConfiguredPeerSwitchesWhenActiveCandidateDisappears(t *testing.T) {
	table := newTestAURPPeerTable()
	localDI := aurp.IPDomainIdentifier(net.IPv4(192, 0, 2, 1))
	ip1, ip2, _ := testPeerIPs()

	peer, err := table.LookupOrCreate(
		context.Background(), table.logger, nil, nil,
		"peer.example", ip1, localDI, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := table.LookupOrCreate(
		context.Background(), table.logger, nil, nil,
		"peer.example", ip2, localDI, nil,
	); err != nil {
		t.Fatal(err)
	}

	transport := peer.Transport
	switched, err := table.setConfiguredCandidates(peer, []net.IP{ip2})
	if err != nil {
		t.Fatal(err)
	}
	if !switched {
		t.Fatal("active endpoint disappearance did not switch candidate")
	}
	if peer.Transport != transport {
		t.Fatal("endpoint switch replaced AURP transport identity")
	}
	if !peer.RemoteAddr().Equal(ip2) {
		t.Fatalf("active endpoint = %v, want %v", peer.RemoteAddr(), ip2)
	}

	oldPeer, err := table.Lookup(ip1)
	if err != nil {
		t.Fatal(err)
	}
	if oldPeer != nil {
		t.Fatalf("stale candidate %v still mapped", ip1)
	}
	newPeer, err := table.Lookup(ip2)
	if err != nil {
		t.Fatal(err)
	}
	if newPeer != peer {
		t.Fatalf("replacement candidate mapped to %p, want %p", newPeer, peer)
	}
}

func TestAURPConfiguredPeerAddsCandidateWithoutSwitch(t *testing.T) {
	table := newTestAURPPeerTable()
	localDI := aurp.IPDomainIdentifier(net.IPv4(192, 0, 2, 1))
	ip1, ip2, ip3 := testPeerIPs()

	peer, err := table.LookupOrCreate(
		context.Background(), table.logger, nil, nil,
		"peer.example", ip1, localDI, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	switched, err := table.setConfiguredCandidates(peer, []net.IP{ip1, ip2, ip3})
	if err != nil {
		t.Fatal(err)
	}
	if switched {
		t.Fatal("adding DNS candidates unexpectedly changed active endpoint")
	}

	for _, ip := range []net.IP{ip1, ip2, ip3} {
		got, err := table.Lookup(ip)
		if err != nil {
			t.Fatal(err)
		}
		if got != peer {
			t.Fatalf("candidate %v mapped to %p, want %p", ip, got, peer)
		}
	}
}

func TestAURPConfiguredPeerRejectsCandidateOwnedByAnotherPeer(t *testing.T) {
	table := newTestAURPPeerTable()
	localDI := aurp.IPDomainIdentifier(net.IPv4(192, 0, 2, 1))
	ip1, _, _ := testPeerIPs()

	if _, err := table.LookupOrCreate(
		context.Background(), table.logger, nil, nil,
		"one.example", ip1, localDI, nil,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := table.LookupOrCreate(
		context.Background(), table.logger, nil, nil,
		"two.example", ip1, localDI, nil,
	); err == nil {
		t.Fatal("two configured peers silently shared one candidate IP")
	}
}

func TestAURPReconnectBackoffProgression(t *testing.T) {
	want := []time.Duration{
		10 * time.Minute,
		20 * time.Minute,
		40 * time.Minute,
		80 * time.Minute,
		120 * time.Minute,
		120 * time.Minute,
	}
	for i, wantDelay := range want {
		failures := i + 1
		if got := reconnectBackoff(failures); got != wantDelay {
			t.Fatalf(
				"reconnectBackoff(%d) = %v, want %v",
				failures, got, wantDelay,
			)
		}
	}
	if got := reconnectBackoff(0); got != 0 {
		t.Fatalf("reconnectBackoff(0) = %v, want 0", got)
	}
}

func TestAURPReconnectBackoffJitterBounds(t *testing.T) {
	base := 40 * time.Minute
	lower := base - base/10
	upper := base + base/10
	for i := 0; i < 200; i++ {
		got := jitterReconnectBackoff(base)
		if got < lower || got > upper {
			t.Fatalf(
				"jittered delay %v outside [%v,%v]",
				got, lower, upper,
			)
		}
	}
}

func TestAURPReconnectFailureSchedulesAndReset(t *testing.T) {
	peer := &AURPPeer{
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	now := time.Unix(1_800_000_000, 0)

	peer.noteReconnectFailure(now)
	if got := peer.ReconnectFailures(); got != 1 {
		t.Fatalf("failure count = %d, want 1", got)
	}
	first := peer.NextReconnect().Sub(now)
	if first < 9*time.Minute || first > 11*time.Minute {
		t.Fatalf("first reconnect delay = %v, want 10m +/-10%%", first)
	}
	if peer.reconnectReady(now.Add(8 * time.Minute)) {
		t.Fatal("peer became reconnect-ready before backoff expired")
	}
	if !peer.reconnectReady(peer.NextReconnect()) {
		t.Fatal("peer not reconnect-ready at scheduled time")
	}

	peer.noteReconnectFailure(now)
	if got := peer.ReconnectFailures(); got != 2 {
		t.Fatalf("failure count = %d, want 2", got)
	}
	second := peer.NextReconnect().Sub(now)
	if second < 18*time.Minute || second > 22*time.Minute {
		t.Fatalf("second reconnect delay = %v, want 20m +/-10%%", second)
	}

	peer.resetReconnectBackoff()
	if got := peer.ReconnectFailures(); got != 0 {
		t.Fatalf("failure count after reset = %d, want 0", got)
	}
	if !peer.NextReconnect().IsZero() {
		t.Fatalf("next reconnect after reset = %v, want zero", peer.NextReconnect())
	}
	if !peer.reconnectReady(now) {
		t.Fatal("reset peer is not immediately reconnect-ready")
	}
}

func TestAURPNewDNSCandidateBypassesCurrentBackoff(t *testing.T) {
	table := newTestAURPPeerTable()
	localDI := aurp.IPDomainIdentifier(net.IPv4(192, 0, 2, 1))
	ip1, ip2, _ := testPeerIPs()

	peer, err := table.LookupOrCreate(
		context.Background(), table.logger, nil, nil,
		"peer.example", ip1, localDI, nil,
	)
	if err != nil {
		t.Fatal(err)
	}

	peer.reconnectFailures.Store(3)
	peer.nextReconnect.Store(time.Now().Add(time.Hour))

	if _, err := table.LookupOrCreate(
		context.Background(), table.logger, nil, nil,
		"peer.example", ip2, localDI, nil,
	); err != nil {
		t.Fatal(err)
	}
	switched, err := table.setConfiguredCandidates(peer, []net.IP{ip2})
	if err != nil {
		t.Fatal(err)
	}
	if !switched {
		t.Fatal("active endpoint did not switch")
	}

	if !peer.reconnectReady(time.Now()) {
		t.Fatal("new DNS endpoint remained blocked by old reconnect deadline")
	}
	if got := peer.ReconnectFailures(); got != 3 {
		t.Fatalf("DNS endpoint switch reset failures = %d, want 3", got)
	}
}

func TestAURPDNSBackoffProgression(t *testing.T) {
	want := []time.Duration{
		30 * time.Second,
		1 * time.Minute,
		2 * time.Minute,
		5 * time.Minute,
		10 * time.Minute,
		30 * time.Minute,
		30 * time.Minute,
	}
	for i, wantDelay := range want {
		failures := i + 1
		if got := dnsBackoff(failures); got != wantDelay {
			t.Fatalf(
				"dnsBackoff(%d) = %v, want %v",
				failures, got, wantDelay,
			)
		}
	}
	if got := dnsBackoff(0); got != 0 {
		t.Fatalf("dnsBackoff(0) = %v, want 0", got)
	}
}

func TestAURPDNSBackoffJitterBounds(t *testing.T) {
	base := 5 * time.Minute
	lower := base - base/10
	upper := base + base/10
	for i := 0; i < 200; i++ {
		got := jitterDNSBackoff(base)
		if got < lower || got > upper {
			t.Fatalf(
				"jittered DNS delay %v outside [%v,%v]",
				got, lower, upper,
			)
		}
	}
}

func TestAURPDNSErrorKinds(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{
			name: "not found",
			err:  &net.DNSError{Err: "no such host", IsNotFound: true},
			want: "not-found",
		},
		{
			name: "timeout",
			err:  &net.DNSError{Err: "timeout", IsTimeout: true},
			want: "timeout",
		},
		{
			name: "temporary",
			err:  &net.DNSError{Err: "temporary", IsTemporary: true},
			want: "temporary",
		},
		{
			name: "generic dns",
			err:  &net.DNSError{Err: "resolver error"},
			want: "dns-error",
		},
		{
			name: "generic error",
			err:  errors.New("boom"),
			want: "error",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := dnsErrorKind(tc.err); got != tc.want {
				t.Fatalf("dnsErrorKind(%v) = %q, want %q", tc.err, got, tc.want)
			}
		})
	}
}

func TestAURPTracksUnresolvedConfiguredPeer(t *testing.T) {
	table := newTestAURPPeerTable()
	table.TrackConfiguredAddress("missing.example")

	if !table.dnsReady("missing.example", time.Now()) {
		t.Fatal("new configured peer should be immediately DNS-ready")
	}
	if got := table.configuredPeer("missing.example"); got != nil {
		t.Fatalf("unresolved configured peer unexpectedly has peer object %p", got)
	}

	now := time.Unix(1_800_000_000, 0)
	err := &net.DNSError{Err: "no such host", IsNotFound: true}
	table.noteDNSFailure("missing.example", now, err)

	if table.dnsReady("missing.example", now.Add(20*time.Second)) {
		t.Fatal("DNS retry became ready before first backoff expired")
	}

	table.mu.RLock()
	state := table.dnsByConfigured["missing.example"]
	table.mu.RUnlock()
	if state.failures != 1 {
		t.Fatalf("DNS failures = %d, want 1", state.failures)
	}
	if state.kind != "not-found" {
		t.Fatalf("DNS error kind = %q, want not-found", state.kind)
	}
	firstDelay := state.next.Sub(now)
	if firstDelay < 27*time.Second || firstDelay > 33*time.Second {
		t.Fatalf("first DNS retry delay = %v, want 30s +/-10%%", firstDelay)
	}

	table.resetDNSBackoff("missing.example")
	if !table.dnsReady("missing.example", now) {
		t.Fatal("DNS success reset did not make peer immediately ready")
	}
	table.mu.RLock()
	state = table.dnsByConfigured["missing.example"]
	table.mu.RUnlock()
	if state.failures != 0 || state.kind != "" || !state.next.IsZero() {
		t.Fatalf("DNS state after reset = %+v, want zero state", state)
	}
}

func TestAURPStatusIncludesConfiguredDNSAndPeerState(t *testing.T) {
	table := newTestAURPPeerTable()
	localDI := aurp.IPDomainIdentifier(net.IPv4(192, 0, 2, 1))
	ip1, ip2, _ := testPeerIPs()

	peer, err := table.LookupOrCreate(
		context.Background(), table.logger, nil, nil,
		"peer.example", ip1, localDI, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := table.LookupOrCreate(
		context.Background(), table.logger, nil, nil,
		"peer.example", ip2, localDI, nil,
	); err != nil {
		t.Fatal(err)
	}

	peer.Transport.SetRemoteConnID(77)
	peer.Transport.IncLocalSeq()
	peer.Transport.IncRemoteSeq()
	peer.lastSuccess.Store(time.Unix(1_800_000_000, 0))
	peer.reconnectFailures.Store(2)
	peer.duplicateRoutingPackets.Store(3)
	peer.reacksSent.Store(3)
	peer.staleRoutingPackets.Store(4)
	peer.futureRoutingPackets.Store(5)
	peer.connectionIDMismatches.Store(6)

	table.TrackConfiguredAddress("missing.example")
	table.noteDNSFailure(
		"missing.example",
		time.Unix(1_800_000_000, 0),
		&net.DNSError{Err: "no such host", IsNotFound: true},
	)

	got, err := table.status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	rows, ok := got.([]aurpPeerStatusRow)
	if !ok {
		t.Fatalf("status type = %T, want []aurpPeerStatusRow", got)
	}

	var peerRow, missingRow *aurpPeerStatusRow
	for i := range rows {
		switch rows[i].ConfiguredAddr {
		case "peer.example":
			peerRow = &rows[i]
		case "missing.example":
			missingRow = &rows[i]
		}
	}
	if peerRow == nil {
		t.Fatal("peer.example missing from status")
	}
	if missingRow == nil {
		t.Fatal("unresolved missing.example missing from status")
	}

	if peerRow.CandidateAddrs != "104.21.23.127, 172.67.211.24" {
		t.Fatalf("candidates = %q", peerRow.CandidateAddrs)
	}
	if peerRow.RemoteAddr != "104.21.23.127" {
		t.Fatalf("active endpoint = %q", peerRow.RemoteAddr)
	}
	if peerRow.RemoteDI != "104.21.23.127 (0x6815177f)" {
		t.Fatalf("remote DI = %q", peerRow.RemoteDI)
	}
	if peerRow.LocalConnID == 0 || peerRow.RemoteConnID != 77 {
		t.Fatalf(
			"conn IDs = %d/%d, want nonzero/77",
			peerRow.LocalConnID, peerRow.RemoteConnID,
		)
	}
	if peerRow.TxSeq != 2 || peerRow.RxSeq != 2 {
		t.Fatalf("seq tx/rx = %d/%d, want 2/2", peerRow.TxSeq, peerRow.RxSeq)
	}
	if peerRow.ReconnectFailures != 2 {
		t.Fatalf("reconnect failures = %d, want 2", peerRow.ReconnectFailures)
	}
	if peerRow.DuplicateRoutingPackets != 3 ||
		peerRow.ReacksSent != 3 ||
		peerRow.StaleRoutingPackets != 4 ||
		peerRow.FutureRoutingPackets != 5 ||
		peerRow.ConnectionIDMismatches != 6 {
		t.Fatalf("unexpected status counters: %+v", *peerRow)
	}

	if missingRow.HasPeer {
		t.Fatal("unresolved configured peer unexpectedly has an active peer")
	}
	if missingRow.ReceiverState != "unresolved" ||
		missingRow.SenderState != "unresolved" {
		t.Fatalf(
			"unresolved states = %q/%q",
			missingRow.ReceiverState, missingRow.SenderState,
		)
	}
	if missingRow.DNSFailures != 1 || missingRow.DNSErrorKind != "not-found" {
		t.Fatalf(
			"DNS state = failures %d kind %q",
			missingRow.DNSFailures, missingRow.DNSErrorKind,
		)
	}
}

func TestAURPMetricsDoNotDuplicateMultiACandidatePeer(t *testing.T) {
	table := newTestAURPPeerTable()
	localDI := aurp.IPDomainIdentifier(net.IPv4(192, 0, 2, 1))
	ip1, ip2, _ := testPeerIPs()

	if _, err := table.LookupOrCreate(
		context.Background(), table.logger, nil, nil,
		"peer.example", ip1, localDI, nil,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := table.LookupOrCreate(
		context.Background(), table.logger, nil, nil,
		"peer.example", ip2, localDI, nil,
	); err != nil {
		t.Fatal(err)
	}

	registry := prometheus.NewRegistry()
	registry.MustRegister(table)
	if _, err := registry.Gather(); err != nil {
		t.Fatalf("gathering multi-A peer metrics: %v", err)
	}
}

func TestSet8ConfiguredTunnelIdentitySurvivesEndpointSwitch(t *testing.T) {
	table := newTestAURPPeerTable()
	localDI := aurp.IPDomainIdentifier(net.IPv4(192, 0, 2, 1))
	ip1, ip2, _ := testPeerIPs()

	peer, err := table.LookupOrCreate(
		context.Background(), table.logger, nil, nil,
		"Peer.Example", ip1, localDI, nil,
	)
	if err != nil {
		t.Fatal(err)
	}

	wantTunnelID := "cfg:peer.example"
	if got := peer.TunnelID(); got != wantTunnelID {
		t.Fatalf("tunnel ID = %q, want %q", got, wantTunnelID)
	}
	wantKey := peer.RouteTargetKey()

	if _, err := table.LookupOrCreate(
		context.Background(), table.logger, nil, nil,
		"Peer.Example", ip2, localDI, nil,
	); err != nil {
		t.Fatal(err)
	}
	switched, err := table.setConfiguredCandidates(peer, []net.IP{ip2})
	if err != nil {
		t.Fatal(err)
	}
	if !switched {
		t.Fatal("active endpoint did not switch")
	}
	if got := peer.RouteTargetKey(); got != wantKey {
		t.Fatalf("route target key changed across DNS failover: %q -> %q", wantKey, got)
	}
	if got := peer.TunnelID(); got != wantTunnelID {
		t.Fatalf("tunnel ID changed across DNS failover: %q", got)
	}
}

func TestSet8OpenPeerTunnelIdentityUsesDomainIdentifier(t *testing.T) {
	table := newTestAURPPeerTable()
	localDI := aurp.IPDomainIdentifier(net.IPv4(192, 0, 2, 1))
	remoteDI := aurp.IPDomainIdentifier(net.IPv4(198, 51, 100, 9))

	peer, err := table.LookupOrCreate(
		context.Background(), table.logger, nil, nil,
		"", net.IPv4(203, 0, 113, 9), localDI, remoteDI,
	)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := peer.TunnelID(), "di:198.51.100.9"; got != want {
		t.Fatalf("open peer tunnel ID = %q, want %q", got, want)
	}

	oldKey := peer.RouteTargetKey()
	peer.setRemoteAddr(net.IPv4(203, 0, 113, 99))
	if got := peer.RouteTargetKey(); got != oldKey {
		t.Fatalf("open peer route target key changed with endpoint: %q -> %q", oldKey, got)
	}
}

func TestSet8CandidateConflictGetsBackoffClassification(t *testing.T) {
	table := newTestAURPPeerTable()
	localDI := aurp.IPDomainIdentifier(net.IPv4(192, 0, 2, 1))
	ip1, _, _ := testPeerIPs()

	if _, err := table.LookupOrCreate(
		context.Background(), table.logger, nil, nil,
		"one.example", ip1, localDI, nil,
	); err != nil {
		t.Fatal(err)
	}

	_, err := table.LookupOrCreate(
		context.Background(), table.logger, nil, nil,
		"two.example", ip1, localDI, nil,
	)
	if err == nil {
		t.Fatal("expected configured peer identity conflict")
	}
	if got := dnsErrorKind(err); got != "identity-conflict" {
		t.Fatalf("conflict kind = %q, want identity-conflict", got)
	}

	now := time.Unix(1_800_000_000, 0)
	table.noteDNSFailure("two.example", now, err)
	if table.dnsReady("two.example", now.Add(20*time.Second)) {
		t.Fatal("identity conflict did not enter configured-peer backoff")
	}
	table.mu.RLock()
	state := table.dnsByConfigured["two.example"]
	table.mu.RUnlock()
	if state.kind != "identity-conflict" || state.failures != 1 {
		t.Fatalf("identity conflict state = %+v", state)
	}
}

func TestSet8IdentityConflictBackoffProgressesWithoutReset(t *testing.T) {
	table := newTestAURPPeerTable()
	now := time.Unix(1_800_000_000, 0)
	err := fmt.Errorf("%w: duplicate configured identity", errPeerCandidateConflict)

	table.noteDNSFailure("dup.example", now, err)
	table.mu.RLock()
	first := table.dnsByConfigured["dup.example"]
	table.mu.RUnlock()
	if first.failures != 1 || first.kind != "identity-conflict" {
		t.Fatalf("first conflict state = %+v", first)
	}

	table.noteDNSFailure("dup.example", first.next, err)
	table.mu.RLock()
	second := table.dnsByConfigured["dup.example"]
	table.mu.RUnlock()
	if second.failures != 2 || second.kind != "identity-conflict" {
		t.Fatalf("second conflict state = %+v", second)
	}
	delay := second.next.Sub(first.next)
	if delay < 54*time.Second || delay > 66*time.Second {
		t.Fatalf("second identity-conflict delay = %v, want 1m +/-10%%", delay)
	}
}

func TestSet8CandidateRefreshConflictIsClassified(t *testing.T) {
	table := newTestAURPPeerTable()
	localDI := aurp.IPDomainIdentifier(net.IPv4(192, 0, 2, 1))
	ip1, ip2, _ := testPeerIPs()

	peer1, err := table.LookupOrCreate(
		context.Background(), table.logger, nil, nil,
		"one.example", ip1, localDI, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := table.LookupOrCreate(
		context.Background(), table.logger, nil, nil,
		"two.example", ip2, localDI, nil,
	); err != nil {
		t.Fatal(err)
	}

	_, err = table.setConfiguredCandidates(peer1, []net.IP{ip1, ip2})
	if err == nil {
		t.Fatal("expected candidate refresh identity conflict")
	}
	if got := dnsErrorKind(err); got != "identity-conflict" {
		t.Fatalf("refresh conflict kind = %q, want identity-conflict", got)
	}
}

func TestSet26MetricPeerLabelSurvivesDNSEndpointSwitch(t *testing.T) {
	table := newTestAURPPeerTable()
	localDI := aurp.IPDomainIdentifier(net.IPv4(192, 0, 2, 1))
	ip1, ip2, _ := testPeerIPs()

	peer, err := table.LookupOrCreate(
		context.Background(), table.logger, nil, nil,
		"Peer.Example", ip1, localDI, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	want := "cfg:peer.example"
	if got := peer.metricPeerLabel(); got != want {
		t.Fatalf("metric peer label = %q, want %q", got, want)
	}

	switched, err := table.setConfiguredCandidates(peer, []net.IP{ip2})
	if err != nil {
		t.Fatal(err)
	}
	if !switched {
		t.Fatal("expected active endpoint switch")
	}
	if got := peer.metricPeerLabel(); got != want {
		t.Fatalf("metric peer label changed after DNS failover: got %q want %q", got, want)
	}
}
