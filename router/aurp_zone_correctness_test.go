package router

import (
	"testing"

	"drjosh.dev/jrouter/aurp"
)

func TestSet9ZIRspZoneRequiresOwningPeerRoute(t *testing.T) {
	rt := NewRouteTable(t.Context())
	owner := &AURPPeer{
		tunnelID:   "cfg:owner.example",
		RouteTable: rt,
	}
	other := &AURPPeer{
		tunnelID:   "cfg:other.example",
		RouteTable: rt,
	}

	if _, err := rt.UpsertRoute(owner, true, 1700, 1700, 2); err != nil {
		t.Fatal(err)
	}
	if _, err := rt.UpsertRoute(other, true, 1800, 1800, 2); err != nil {
		t.Fatal(err)
	}

	if owner.applyZIRspZone(aurp.ZoneTuple{
		Network: 1800,
		Name:    "Wrong Peer Zone",
	}) {
		t.Fatal("accepted zone information for a route owned by another peer")
	}
	if got := rt.Lookup(1800); !got.Zero() {
		t.Fatalf("foreign route became valid from wrong peer zone info: %v", got)
	}

	if !owner.applyZIRspZone(aurp.ZoneTuple{
		Network: 1700,
		Name:    "Owner Zone",
	}) {
		t.Fatal("rejected zone information for route owned by this peer")
	}
	if got := rt.Lookup(1700); got.Zero() {
		t.Fatal("owned route did not become valid after zone information")
	}
}

func TestSet9ZIRspZoneRejectsUnknownNetwork(t *testing.T) {
	rt := NewRouteTable(t.Context())
	peer := &AURPPeer{
		tunnelID:   "cfg:peer.example",
		RouteTable: rt,
	}

	if peer.applyZIRspZone(aurp.ZoneTuple{
		Network: 1900,
		Name:    "Unknown Zone",
	}) {
		t.Fatal("accepted zone information for an unknown network")
	}
	if got := rt.Lookup(1900); !got.Zero() {
		t.Fatalf("unknown network became routable: %v", got)
	}
}
