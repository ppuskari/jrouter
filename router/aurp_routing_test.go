package router

import (
	"bytes"
	"net"
	"testing"

	"drjosh.dev/jrouter/aurp"
	"github.com/sfiera/multitalk/pkg/ddp"
)

func testAURPRoute(target fakeTarget, networkNumber ddp.Network, distance uint8) Route {
	return Route{
		RouteKey: RouteKey{
			TargetKey: target.RouteTargetKey(),
			NetStart:  networkNumber,
		},
		Extended: true,
		NetEnd:   networkNumber,
		Target:   target,
		Distance: distance,
		network:  &network{},
	}
}

func TestAURPEventForBestTransition(t *testing.T) {
	localA := fakeTarget{key: "local-a", class: TargetClassAppleTalkPeer}
	localB := fakeTarget{key: "local-b", class: TargetClassAppleTalkPeer}
	tunnelA := fakeTarget{key: "aurp-a", class: TargetClassAURPPeer}
	tunnelB := fakeTarget{key: "aurp-b", class: TargetClassAURPPeer}

	tests := []struct {
		name      string
		before    Route
		after     Route
		want      aurp.EventCode
		wantDist  uint8
		wantEvent bool
	}{
		{"new local", Route{}, testAURPRoute(localA, 100, 2), aurp.EventCodeNA, 2, true},
		{"new tunnel hidden", Route{}, testAURPRoute(tunnelA, 100, 2), 0, 0, false},
		{"tunnel to local", testAURPRoute(tunnelA, 100, 2), testAURPRoute(localA, 100, 3), aurp.EventCodeNA, 3, true},
		{"local to tunnel", testAURPRoute(localA, 100, 2), testAURPRoute(tunnelA, 100, 2), aurp.EventCodeNRC, 0, true},
		{"local deleted", testAURPRoute(localA, 100, 2), Route{}, aurp.EventCodeND, 0, true},
		{"local distance changed", testAURPRoute(localA, 100, 2), testAURPRoute(localA, 100, 4), aurp.EventCodeNDC, 4, true},
		{"local becomes distance 15", testAURPRoute(localA, 100, 14), testAURPRoute(localA, 100, 15), aurp.EventCodeNDC, 15, true},
		{"distance 15 becomes usable", testAURPRoute(localA, 100, 15), testAURPRoute(localA, 100, 14), aurp.EventCodeNA, 14, true},
		{"local same metric new next hop", testAURPRoute(localA, 100, 2), testAURPRoute(localB, 100, 2), 0, 0, false},
		{"tunnel to tunnel hidden", testAURPRoute(tunnelA, 100, 2), testAURPRoute(tunnelB, 100, 2), 0, 0, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := aurpEventForBestTransition(tc.before, tc.after)
			if ok != tc.wantEvent {
				t.Fatalf("event present = %v, want %v; event=%v", ok, tc.wantEvent, got)
			}
			if !ok {
				return
			}
			if got.EventCode != tc.want || got.Distance != tc.wantDist {
				t.Fatalf("event = (%v, distance %d), want (%v, distance %d)",
					got.EventCode, got.Distance, tc.want, tc.wantDist)
			}
		})
	}
}

func TestAURPPendingChangesCoalesce(t *testing.T) {
	local := fakeTarget{key: "local", class: TargetClassAppleTalkPeer}
	tunnel := fakeTarget{key: "aurp", class: TargetClassAURPPeer}

	p := &AURPPeer{}
	p.setSState(SenderConnected)

	local2 := testAURPRoute(local, 200, 2)
	tunnel2 := testAURPRoute(tunnel, 200, 2)

	// A local route that leaves through the tunnel and returns before the
	// update interval should produce no packet-level event at all.
	p.queueBestNetworkTransition(local2, tunnel2)
	p.queueBestNetworkTransition(tunnel2, local2)
	if got := p.takePendingEvents(); len(got) != 0 {
		t.Fatalf("cancelled transition produced %v", got)
	}

	// If the peer started with no exportable route, an intermediate local
	// metric change must still coalesce to one NA containing the final metric.
	local3 := testAURPRoute(local, 300, 3)
	local5 := testAURPRoute(local, 300, 5)
	tunnel3 := testAURPRoute(tunnel, 300, 1)
	p.queueBestNetworkTransition(tunnel3, local3)
	p.queueBestNetworkTransition(local3, local5)
	got := p.takePendingEvents()
	if len(got) != 1 || got[0].EventCode != aurp.EventCodeNA || got[0].Distance != 5 {
		t.Fatalf("coalesced events = %v, want one NA distance 5", got)
	}
}

func TestAURPExportedRoutesUseBestLocalView(t *testing.T) {
	rt := NewRouteTable(t.Context())
	direct := fakeTarget{key: "direct", class: TargetClassDirect}
	local := fakeTarget{key: "local", class: TargetClassAppleTalkPeer}
	tunnel := fakeTarget{key: "aurp", class: TargetClassAURPPeer}

	if _, err := rt.UpsertRoute(direct, true, 100, 100, 0); err != nil {
		t.Fatal(err)
	}
	if err := rt.AddZonesToNetwork(100, "Direct"); err != nil {
		t.Fatal(err)
	}

	if _, err := rt.UpsertRoute(local, true, 200, 200, 3); err != nil {
		t.Fatal(err)
	}
	if _, err := rt.UpsertRoute(tunnel, true, 200, 200, 1); err != nil {
		t.Fatal(err)
	}
	if err := rt.AddZonesToNetwork(200, "Hidden by tunnel"); err != nil {
		t.Fatal(err)
	}

	if _, err := rt.UpsertRoute(local, true, 300, 300, 15); err != nil {
		t.Fatal(err)
	}
	if err := rt.AddZonesToNetwork(300, "Distance fifteen"); err != nil {
		t.Fatal(err)
	}

	if _, err := rt.UpsertRoute(local, true, 400, 400, 2); err != nil {
		t.Fatal(err)
	}
	if err := rt.AddZonesToNetwork(400, "Local peer"); err != nil {
		t.Fatal(err)
	}

	got := rt.aurpExportedRoutes()
	if len(got) != 2 {
		t.Fatalf("exported route count = %d, want 2; routes=%v", len(got), got)
	}
	if got[0].NetStart != 100 || got[1].NetStart != 400 {
		t.Fatalf("exported starts = %v, want [100 400]",
			[]ddp.Network{got[0].NetStart, got[1].NetStart})
	}
}

func TestAURPRoutingChunksFitBudget(t *testing.T) {
	tr := aurp.NewTransport(
		aurp.IPDomainIdentifier(net.IPv4(10, 0, 0, 1)),
		aurp.IPDomainIdentifier(net.IPv4(10, 0, 0, 2)),
		1, 2,
	)

	var nets aurp.NetworkTuples
	var events aurp.EventTuples
	for i := 0; i < 600; i++ {
		n := ddp.Network(1000 + i)
		nets = append(nets, aurp.NetworkTuple{
			Extended: true, RangeStart: n, RangeEnd: n, Distance: 2,
		})
		events = append(events, aurp.EventTuple{
			EventCode: aurp.EventCodeNDC,
			Extended:  true, RangeStart: n, RangeEnd: n, Distance: 3,
		})
	}

	rspBudget, err := aurpRoutingPayloadBudget(tr.NewRIRspPacket(0, nil))
	if err != nil {
		t.Fatal(err)
	}
	rspChunks, err := chunkAURPNetworkTuples(nets, rspBudget)
	if err != nil {
		t.Fatal(err)
	}
	if len(rspChunks) < 2 {
		t.Fatalf("RI-Rsp did not split: %d chunk(s)", len(rspChunks))
	}
	for i, chunk := range rspChunks {
		var b bytes.Buffer
		if _, err := tr.NewRIRspPacket(0, chunk).WriteTo(&b); err != nil {
			t.Fatal(err)
		}
		if b.Len() > aurpRoutingDatagramBudget {
			t.Fatalf("RI-Rsp chunk %d size = %d > %d", i, b.Len(), aurpRoutingDatagramBudget)
		}
	}

	updBudget, err := aurpRoutingPayloadBudget(tr.NewRIUpdPacket(nil))
	if err != nil {
		t.Fatal(err)
	}
	updChunks, err := chunkAURPEventTuples(events, updBudget)
	if err != nil {
		t.Fatal(err)
	}
	if len(updChunks) < 2 {
		t.Fatalf("RI-Upd did not split: %d chunk(s)", len(updChunks))
	}
	for i, chunk := range updChunks {
		var b bytes.Buffer
		if _, err := tr.NewRIUpdPacket(chunk).WriteTo(&b); err != nil {
			t.Fatal(err)
		}
		if b.Len() > aurpRoutingDatagramBudget {
			t.Fatalf("RI-Upd chunk %d size = %d > %d", i, b.Len(), aurpRoutingDatagramBudget)
		}
	}
}

func TestAURPRoutingPacketSequences(t *testing.T) {
	tr := aurp.NewTransport(
		aurp.IPDomainIdentifier(net.IPv4(10, 0, 0, 1)),
		aurp.IPDomainIdentifier(net.IPv4(10, 0, 0, 2)),
		1, 2,
	)
	p := &AURPPeer{Transport: tr}

	p.pendingRIRsp = []aurp.NetworkTuples{
		{{RangeStart: 100, RangeEnd: 100, Distance: 1}},
		{{RangeStart: 200, RangeEnd: 200, Distance: 1}},
	}
	tr.ResetLocalSeq()
	firstRsp, err := p.nextRIRspPacket(false)
	if err != nil {
		t.Fatal(err)
	}
	secondRsp, err := p.nextRIRspPacket(true)
	if err != nil {
		t.Fatal(err)
	}
	if firstRsp.Sequence != 1 || firstRsp.Flags&aurp.RoutingFlagLast != 0 {
		t.Fatalf("first RI-Rsp seq/flags = %d/%04x", firstRsp.Sequence, firstRsp.Flags)
	}
	if secondRsp.Sequence != 2 || secondRsp.Flags&aurp.RoutingFlagLast == 0 {
		t.Fatalf("second RI-Rsp seq/flags = %d/%04x", secondRsp.Sequence, secondRsp.Flags)
	}

	p.pendingRIUpd = []aurp.EventTuples{
		{{EventCode: aurp.EventCodeNA, RangeStart: 300, RangeEnd: 300, Distance: 1}},
		{{EventCode: aurp.EventCodeNDC, RangeStart: 300, RangeEnd: 300, Distance: 2}},
	}
	tr.ResetLocalSeq()
	firstUpd, err := p.nextRIUpdPacket(true)
	if err != nil {
		t.Fatal(err)
	}
	secondUpd, err := p.nextRIUpdPacket(true)
	if err != nil {
		t.Fatal(err)
	}
	if firstUpd.Sequence != 2 || secondUpd.Sequence != 3 {
		t.Fatalf("RI-Upd sequences = %d,%d; want 2,3", firstUpd.Sequence, secondUpd.Sequence)
	}
}

func TestAURPInconsistentUpdateHandling(t *testing.T) {
	rt := NewRouteTable(t.Context())
	p := &AURPPeer{
		RemoteAddr: net.IPv4(10, 1, 2, 3),
		RouteTable: rt,
	}

	needZI, err := p.applyRIUpdEvent(aurp.EventTuple{
		EventCode:  aurp.EventCodeNDC,
		Extended:   true,
		RangeStart: 500,
		RangeEnd:   500,
		Distance:   3,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !needZI {
		t.Fatal("NDC for unknown network did not request zone information as NA")
	}
	r := rt.find(p, 500)
	if r.Zero() || r.Distance != 4 {
		t.Fatalf("NDC-for-unknown route = %v; want stored distance 4", r)
	}

	// Unknown ND/NRC are explicitly ignorable.
	for _, code := range []aurp.EventCode{aurp.EventCodeND, aurp.EventCodeNRC} {
		if _, err := p.applyRIUpdEvent(aurp.EventTuple{
			EventCode: code, RangeStart: 600,
		}); err != nil {
			t.Fatalf("%v for unknown network: %v", code, err)
		}
	}

	// Distance 15 behaves as deletion.
	if _, err := p.applyRIUpdEvent(aurp.EventTuple{
		EventCode:  aurp.EventCodeNDC,
		Extended:   true,
		RangeStart: 500,
		RangeEnd:   500,
		Distance:   15,
	}); err != nil {
		t.Fatal(err)
	}
	if r := rt.find(p, 500); !r.Zero() {
		t.Fatalf("distance-15 NDC left route behind: %v", r)
	}

	// A poison tuple in an RI-Rsp is skipped without preventing the following
	// usable tuple from being processed.
	accepted, err := p.applyRIRspNetworkTuple(aurp.NetworkTuple{
		Extended: true, RangeStart: 700, RangeEnd: 700, Distance: 15,
	})
	if err != nil || accepted {
		t.Fatalf("poison RI-Rsp tuple accepted=%v err=%v", accepted, err)
	}
	accepted, err = p.applyRIRspNetworkTuple(aurp.NetworkTuple{
		Extended: true, RangeStart: 701, RangeEnd: 701, Distance: 2,
	})
	if err != nil || !accepted {
		t.Fatalf("valid RI-Rsp tuple accepted=%v err=%v", accepted, err)
	}
	if r := rt.find(p, 701); r.Zero() || r.Distance != 3 {
		t.Fatalf("valid tuple after poison not installed: %v", r)
	}
}
