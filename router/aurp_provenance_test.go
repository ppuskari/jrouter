package router

import (
	"net"
	"testing"

	"drjosh.dev/jrouter/aurp"
	"github.com/sfiera/multitalk/pkg/ddp"
)

func TestSet82RouteOriginUsesStableAURPTunnelIdentity(t *testing.T) {
	rt := NewRouteTable(t.Context())
	peer := &AURPPeer{
		tunnelID:  "cfg:peer.example",
		RouteTable: rt,
	}
	peer.setRemoteAddr(net.IPv4(198, 51, 100, 10))

	route, err := rt.UpsertRoute(peer, true, 1200, 1200, 2)
	if err != nil {
		t.Fatal(err)
	}
	if err := rt.AddZonesToNetwork(1200, "Provenance"); err != nil {
		t.Fatal(err)
	}

	origin := route.RouteOrigin()
	if origin.Kind != RouteOriginAURP || origin.ID != "cfg:peer.example" {
		t.Fatalf("origin = %+v", origin)
	}

	peer.setRemoteAddr(net.IPv4(203, 0, 113, 20))
	stored := rt.find(peer, 1200)
	origin = stored.RouteOrigin()
	if origin.Kind != RouteOriginAURP || origin.ID != "cfg:peer.example" {
		t.Fatalf("origin changed after endpoint switch: %+v", origin)
	}
}

func TestSet82AURPIngressDoesNotReflectToSamePeer(t *testing.T) {
	rt := NewRouteTable(t.Context())
	peer := &AURPPeer{
		tunnelID:  "cfg:peer.example",
		RouteTable: rt,
	}
	peer.setRemoteAddr(net.IPv4(198, 51, 100, 10))

	if _, err := rt.UpsertRoute(peer, true, 1300, 1300, 2); err != nil {
		t.Fatal(err)
	}
	if err := rt.AddZonesToNetwork(1300, "Reflection"); err != nil {
		t.Fatal(err)
	}

	rtr := &Router{RouteTable: rt}
	pkt := &ddp.ExtPacket{DstNet: 1300}

	if _, err := rtr.outputRoute(pkt, peer.TunnelID()); err == nil {
		t.Fatal("same-peer AURP reflection was not rejected")
	}
}

func TestSet82AURPIngressMayUseDifferentPeer(t *testing.T) {
	rt := NewRouteTable(t.Context())
	ingress := &AURPPeer{
		tunnelID: "cfg:ingress.example",
	}
	egress := &AURPPeer{
		tunnelID:  "cfg:egress.example",
		RouteTable: rt,
	}
	egress.setRemoteAddr(net.IPv4(203, 0, 113, 30))

	if _, err := rt.UpsertRoute(egress, true, 1400, 1400, 2); err != nil {
		t.Fatal(err)
	}
	if err := rt.AddZonesToNetwork(1400, "Transit"); err != nil {
		t.Fatal(err)
	}

	rtr := &Router{RouteTable: rt}
	pkt := &ddp.ExtPacket{DstNet: 1400}

	route, err := rtr.outputRoute(pkt, ingress.TunnelID())
	if err != nil {
		t.Fatal(err)
	}
	if route.RouteOrigin().ID != egress.TunnelID() {
		t.Fatalf("selected origin = %+v", route.RouteOrigin())
	}
}

func TestSet82AURPSplitHorizonUsesProvenance(t *testing.T) {
	target := fakeTarget{key: "legacy-aurp", class: TargetClassAURPPeer}
	r := testAURPRoute(target, 1500, 2)
	r.Origin = RouteOrigin{
		Kind: RouteOriginAURP,
		ID:   "cfg:peer.example",
	}

	if aurpRouteIsAdvertisable(r) {
		t.Fatal("AURP-originated route became exportable")
	}

	local := testAURPRoute(
		fakeTarget{key: "local", class: TargetClassAppleTalkPeer},
		1501,
		2,
	)
	local.Origin = RouteOrigin{
		Kind: RouteOriginAppleTalk,
		ID:   "EtherTalkPeer|en0|1000.2",
	}
	if !aurpRouteIsAdvertisable(local) {
		t.Fatal("local AppleTalk route was incorrectly hidden")
	}
}

func TestSet82OpenPeerOriginUsesDomainIdentity(t *testing.T) {
	rt := NewRouteTable(t.Context())
	peer := &AURPPeer{
		tunnelID: "di:198.51.100.42",
		Transport: aurp.NewTransport(
			aurp.IPDomainIdentifier(net.IPv4(192, 0, 2, 1)),
			aurp.IPDomainIdentifier(net.IPv4(198, 51, 100, 42)),
			1,
			2,
		),
		RouteTable: rt,
	}
	peer.setRemoteAddr(net.IPv4(203, 0, 113, 42))

	route, err := rt.UpsertRoute(peer, true, 1600, 1600, 2)
	if err != nil {
		t.Fatal(err)
	}
	if got := route.RouteOrigin(); got.Kind != RouteOriginAURP ||
		got.ID != "di:198.51.100.42" {
		t.Fatalf("open-peer origin = %+v", got)
	}
}

