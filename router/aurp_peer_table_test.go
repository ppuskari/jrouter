package router

import (
	"context"
	"io"
	"log/slog"
	"net"
	"testing"

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
