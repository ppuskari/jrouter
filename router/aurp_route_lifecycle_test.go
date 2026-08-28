package router

import (
	"net"
	"slices"
	"testing"

	"drjosh.dev/jrouter/aurp"
	"github.com/sfiera/multitalk/pkg/ddp"
)

func TestSet7DeleteInferiorAndBestRouteCandidates(t *testing.T) {
	rt := NewRouteTable(t.Context())
	obs := &fakeObserver{}
	rt.AddObserver(obs)

	best := fakeTarget{key: "best", class: TargetClassAppleTalkPeer}
	inferior := fakeTarget{key: "inferior", class: TargetClassAppleTalkPeer}

	bestRoute, err := rt.UpsertRoute(best, true, 100, 100, 1)
	if err != nil {
		t.Fatal(err)
	}
	inferiorRoute, err := rt.UpsertRoute(inferior, true, 100, 100, 3)
	if err != nil {
		t.Fatal(err)
	}
	if err := rt.AddZonesToNetwork(100, "Lifecycle"); err != nil {
		t.Fatal(err)
	}
	obs.events = nil

	// Removing a non-best candidate must not disturb the active route or emit
	// a best-route transition.
	rt.DeleteTarget(inferior)
	if got := rt.Lookup(100); got.TargetKey != bestRoute.TargetKey ||
		got.Distance != bestRoute.Distance {
		t.Fatalf("best route changed after inferior deletion: %v", got)
	}
	if len(obs.events) != 0 {
		t.Fatalf("inferior deletion emitted observer events: %+v", obs.events)
	}

	// Reinstall the inferior candidate, then remove the best. The fallback
	// must become active and observers must see one best-path change.
	inferiorRoute, err = rt.UpsertRoute(inferior, true, 100, 100, 3)
	if err != nil {
		t.Fatal(err)
	}
	obs.events = nil
	rt.DeleteTarget(best)

	got := rt.Lookup(100)
	if got.TargetKey != inferiorRoute.TargetKey ||
		got.Distance != inferiorRoute.Distance {
		t.Fatalf("fallback route = %v, want %v", got, inferiorRoute)
	}
	if len(obs.events) != 1 ||
		obs.events[0].Event != "changed" ||
		obs.events[0].From.TargetKey != bestRoute.TargetKey ||
		obs.events[0].To.TargetKey != inferiorRoute.TargetKey {
		t.Fatalf("best deletion observer events = %+v", obs.events)
	}
}

func TestSet7DeleteRouteRemovesOnlyExactRouteKey(t *testing.T) {
	rt := NewRouteTable(t.Context())
	target := fakeTarget{key: "same-target", class: TargetClassAppleTalkPeer}

	if _, err := rt.UpsertRoute(target, true, 200, 210, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := rt.UpsertRoute(target, true, 205, 215, 2); err != nil {
		t.Fatal(err)
	}
	if err := rt.AddZonesToNetwork(200, "Range A"); err != nil {
		t.Fatal(err)
	}
	if err := rt.AddZonesToNetwork(205, "Range B"); err != nil {
		t.Fatal(err)
	}

	if err := rt.DeleteRoute(target, 200); err != nil {
		t.Fatal(err)
	}

	if r := rt.find(target, 200); !r.Zero() {
		t.Fatalf("deleted route remains stored: %v", r)
	}
	if r := rt.find(target, 205); r.Zero() {
		t.Fatal("overlapping route with different RouteKey was also deleted")
	}
	if got := rt.Lookup(205); got.Zero() || got.NetStart != 205 {
		t.Fatalf("overlapping route no longer forwards: %v", got)
	}
}

func TestSet7UpsertRangeReplacementRemovesOldTail(t *testing.T) {
	rt := NewRouteTable(t.Context())
	target := fakeTarget{key: "range-target", class: TargetClassAppleTalkPeer}

	if _, err := rt.UpsertRoute(target, true, 300, 310, 1); err != nil {
		t.Fatal(err)
	}
	if err := rt.AddZonesToNetwork(300, "Range"); err != nil {
		t.Fatal(err)
	}
	if got := rt.Lookup(309); got.Zero() {
		t.Fatal("initial extended route does not cover old tail")
	}

	if _, err := rt.UpsertRoute(target, true, 300, 305, 1); err != nil {
		t.Fatal(err)
	}

	for n := ddp.Network(306); n <= 310; n++ {
		if got := rt.Lookup(n); !got.Zero() {
			t.Fatalf("stale old-range route remains at %d: %v", n, got)
		}
	}
	for n := ddp.Network(300); n <= 305; n++ {
		if got := rt.Lookup(n); got.Zero() {
			t.Fatalf("replacement route missing at %d", n)
		}
	}
}

func TestSet7UpdateDistancePersistsStoredRouteAndNDC15(t *testing.T) {
	rt := NewRouteTable(t.Context())
	local := fakeTarget{key: "local", class: TargetClassAppleTalkPeer}
	peerObserver := &AURPPeer{}
	peerObserver.setSState(SenderConnected)
	rt.AddObserver(peerObserver)

	if _, err := rt.UpsertRoute(local, true, 400, 400, 14); err != nil {
		t.Fatal(err)
	}
	if err := rt.AddZonesToNetwork(400, "Metric"); err != nil {
		t.Fatal(err)
	}
	_ = peerObserver.takePendingEvents() // discard initial NA

	if err := rt.UpdateDistance(local, 400, 15); err != nil {
		t.Fatal(err)
	}

	stored := rt.find(local, 400)
	if stored.Zero() || stored.Distance != 15 {
		t.Fatalf("stored route distance = %v, want 15", stored)
	}

	var classRoute Route
	for r := range rt.ValidRoutesForClass(TargetClassAppleTalkPeer) {
		if r.NetStart == 400 {
			classRoute = r
			break
		}
	}
	if classRoute.Zero() || classRoute.Distance != 15 {
		t.Fatalf("by-class route distance = %v, want 15", classRoute)
	}

	events := peerObserver.takePendingEvents()
	if len(events) != 1 ||
		events[0].EventCode != aurp.EventCodeNDC ||
		events[0].Distance != 15 {
		t.Fatalf("distance-15 transition events = %v, want one NDC/15", events)
	}
}

func TestSet7DeleteLastRouteClearsStaleZones(t *testing.T) {
	rt := NewRouteTable(t.Context())
	target := fakeTarget{key: "zone-target", class: TargetClassAppleTalkPeer}

	if _, err := rt.UpsertRoute(target, true, 500, 500, 1); err != nil {
		t.Fatal(err)
	}
	if err := rt.AddZonesToNetwork(500, "Transient Zone"); err != nil {
		t.Fatal(err)
	}
	// Repeated learning must not duplicate reverse-index entries.
	if err := rt.AddZonesToNetwork(500, "Transient Zone"); err != nil {
		t.Fatal(err)
	}
	if got := rt.networksForZone("Transient Zone"); len(got) != 1 {
		t.Fatalf("reverse zone index = %v, want one network", got)
	}

	rt.DeleteTarget(target)

	if got := rt.AllZoneNames(); slices.Contains(got, "Transient Zone") {
		t.Fatalf("stale zone remains globally visible: %v", got)
	}
	if got := rt.RoutesForZone("Transient Zone"); len(got) != 0 {
		t.Fatalf("stale zone still has routes: %v", got)
	}

	// Reintroducing the network must not inherit the retired zone.
	if _, err := rt.UpsertRoute(target, true, 500, 500, 1); err != nil {
		t.Fatal(err)
	}
	if got := rt.Lookup(500); !got.Zero() {
		t.Fatalf("reintroduced route became valid from stale zone: %v", got)
	}
	if err := rt.AddZonesToNetwork(500, "Fresh Zone"); err != nil {
		t.Fatal(err)
	}
	if got := rt.Lookup(500); got.Zero() {
		t.Fatal("route did not become valid after fresh zone information")
	}
}

func TestSet7AURPReceiverDisconnectFlushesAndRebuilds(t *testing.T) {
	rt := NewRouteTable(t.Context())
	localDI := aurp.IPDomainIdentifier(net.IPv4(192, 0, 2, 10))
	remoteDI := aurp.IPDomainIdentifier(net.IPv4(198, 51, 100, 20))
	peer := &AURPPeer{
		RouteTable: rt,
		Transport:  aurp.NewTransport(localDI, remoteDI, 100, 200),
	}
	peer.setRemoteAddr(net.IPv4(198, 51, 100, 20))
	peer.setRState(ReceiverConnected)

	nt := aurp.NetworkTuple{
		Extended:   true,
		RangeStart: 600,
		RangeEnd:   600,
		Distance:   2,
	}
	accepted, err := peer.applyRIRspNetworkTuple(nt)
	if err != nil || !accepted {
		t.Fatalf("initial RI-Rsp route accepted=%v err=%v", accepted, err)
	}
	if err := rt.AddZonesToNetwork(600, "Remote Zone"); err != nil {
		t.Fatal(err)
	}
	if got := rt.Lookup(600); got.Zero() {
		t.Fatal("initial AURP route not valid")
	}

	oldConnID := peer.Transport.LocalConnID()
	peer.disconnectReceiver()

	if got := rt.find(peer, 600); !got.Zero() {
		t.Fatalf("AURP route survived receiver disconnect: %v", got)
	}
	if got := rt.Lookup(600); !got.Zero() {
		t.Fatalf("AURP route still forwards after disconnect: %v", got)
	}
	if slices.Contains(rt.AllZoneNames(), "Remote Zone") {
		t.Fatal("AURP disconnect left stale zone metadata")
	}
	if peer.Transport.LocalConnID() == oldConnID {
		t.Fatal("receiver disconnect did not advance local connection ID")
	}

	// A fresh RI-Rsp reconstructs the route candidate, but it must remain
	// unroutable until fresh zone information arrives.
	accepted, err = peer.applyRIRspNetworkTuple(nt)
	if err != nil || !accepted {
		t.Fatalf("rebuild RI-Rsp route accepted=%v err=%v", accepted, err)
	}
	if got := rt.find(peer, 600); got.Zero() {
		t.Fatal("rebuild did not store the AURP route")
	}
	if got := rt.Lookup(600); !got.Zero() {
		t.Fatalf("rebuilt route inherited stale zone: %v", got)
	}
	if err := rt.AddZonesToNetwork(600, "Remote Zone"); err != nil {
		t.Fatal(err)
	}
	if got := rt.Lookup(600); got.Zero() {
		t.Fatal("rebuilt AURP route did not become valid after fresh zone info")
	}
}
