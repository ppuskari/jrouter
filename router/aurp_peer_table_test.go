package router

import (
	"context"
	"io"
	"log/slog"
	"net"
	"testing"
	"time"

	"drjosh.dev/jrouter/aurp"
)

func newTestAURPPeerTable() *AURPPeerTable {
	return &AURPPeerTable{
		logger:            slog.New(slog.NewTextHandler(io.Discard, nil)),
		peersByIP:         make(map[[4]byte]*AURPPeer),
		peersByConfigured: make(map[string]*AURPPeer),
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
