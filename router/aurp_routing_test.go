package router

import (
	"bytes"
	"context"
	"net"
	"testing"

	"drjosh.dev/jrouter/atalk/nbp"
	"drjosh.dev/jrouter/atalk/zip"
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
	p.setSUIFlags(aurp.RoutingFlagAllSUI)

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
		RouteTable: rt,
	}
	p.setRemoteAddr(net.IPv4(10, 1, 2, 3))

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

func TestAURPRouteTupleSafeguardsRejectInvalidRanges(t *testing.T) {
	rt := NewRouteTable(t.Context())
	peer := &AURPPeer{RouteTable: rt}

	if accepted, err := peer.applyRIRspNetworkTuple(aurp.NetworkTuple{
		RangeStart: 900, RangeEnd: 901,
	}); err == nil || accepted {
		t.Fatalf("non-extended RI-Rsp tuple accepted=%v err=%v", accepted, err)
	}
	if accepted, err := peer.applyRIRspNetworkTuple(aurp.NetworkTuple{
		Extended: true, RangeStart: 901, RangeEnd: 900,
	}); err == nil || accepted {
		t.Fatalf("reversed RI-Rsp tuple accepted=%v err=%v", accepted, err)
	}
	if needZoneInfo, err := peer.applyRIUpdEvent(aurp.EventTuple{
		EventCode: aurp.EventCodeNA, RangeStart: 902, RangeEnd: 903,
	}); err == nil || needZoneInfo {
		t.Fatalf("invalid RI-Upd tuple needZoneInfo=%v err=%v", needZoneInfo, err)
	}
}

func TestSet26HiddenNetworksAreNotExportedOrUpdated(t *testing.T) {
	rt := NewRouteTable(t.Context())
	local := fakeTarget{key: "local-hidden-test", class: TargetClassAppleTalkPeer}
	for _, network := range []ddp.Network{3000, 3001} {
		if _, err := rt.UpsertRoute(local, true, network, network, 2); err != nil {
			t.Fatal(err)
		}
		if err := rt.AddZonesToNetwork(network, "Local Zone"); err != nil {
			t.Fatal(err)
		}
	}

	peer := &AURPPeer{
		RouteTable: rt,
		timing: AURPConfig{HiddenNetworks: []AURPNetworkRange{
			{Start: 3001, End: 3001},
		}},
	}
	routes := peer.aurpExportedRoutes()
	if len(routes) != 1 || routes[0].NetStart != 3000 {
		t.Fatalf("visible exported routes = %v, want only network 3000", routes)
	}

	peer.setSState(SenderConnected)
	peer.setSUIFlags(aurp.RoutingFlagAllSUI)
	peer.queueBestNetworkTransition(Route{}, testAURPRoute(local, 3001, 2))
	if got := peer.takePendingEvents(); len(got) != 0 {
		t.Fatalf("hidden network generated RI-Upd events: %v", got)
	}
}

func TestSet26OutputFromAURPDropsHiddenDestination(t *testing.T) {
	rtr := &Router{
		Config: &Config{AURP: AURPConfig{HiddenNetworks: []AURPNetworkRange{
			{Start: 4000, End: 4009},
		}}},
		RouteTable: NewRouteTable(t.Context()),
	}
	ingress := &AURPPeer{tunnelID: "cfg:ingress.example"}
	pkt := new(ddp.ExtPacket)
	pkt.DstNet = 4005
	if err := rtr.OutputFromAURP(context.Background(), ingress, pkt); err == nil {
		t.Fatal("AURP packet to hidden local network was accepted")
	}
}

func TestSet26HopCountReductionAdjustsOnlyWhenNecessary(t *testing.T) {
	route := Route{Distance: 6}
	pkt := new(ddp.ExtPacket)
	setDDPHopCount(pkt, 12)

	changed, err := reduceAURPHopCount(pkt, route)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("hop-count reduction did not adjust an over-limit path")
	}
	if got, want := ddpHopCount(pkt), uint16(9); got != want {
		t.Fatalf("reduced hop count = %d, want %d", got, want)
	}

	setDDPHopCount(pkt, 4)
	changed, err = reduceAURPHopCount(pkt, route)
	if err != nil {
		t.Fatal(err)
	}
	if changed || ddpHopCount(pkt) != 4 {
		t.Fatalf("safe path was unnecessarily reduced: changed=%v hop=%d", changed, ddpHopCount(pkt))
	}
}

func TestSet26HopCountReductionAdvertisesAURPRouteAsOneHopToRTMP(t *testing.T) {
	aurpTarget := fakeTarget{key: "aurp-hcr", class: TargetClassAURPPeer}
	localTarget := fakeTarget{key: "local-hcr", class: TargetClassAppleTalkPeer}
	if got := rtmpAdvertisedDistance(testAURPRoute(aurpTarget, 3100, 8), true); got != 1 {
		t.Fatalf("AURP route advertised distance = %d, want 1", got)
	}
	if got := rtmpAdvertisedDistance(testAURPRoute(localTarget, 3101, 8), true); got != 8 {
		t.Fatalf("local route advertised distance = %d, want 8", got)
	}
}

func TestSet26HopCountWeightAppliesToImportedAURPDistance(t *testing.T) {
	rt := NewRouteTable(t.Context())
	peer := &AURPPeer{
		tunnelID:   "cfg:weighted.example",
		RouteTable: rt,
		timing:     AURPConfig{HopCountWeight: 2},
	}

	accepted, err := peer.applyRIRspNetworkTuple(aurp.NetworkTuple{
		Extended: true, RangeStart: 3200, RangeEnd: 3200, Distance: 3,
	})
	if err != nil || !accepted {
		t.Fatalf("weighted route accepted=%v err=%v", accepted, err)
	}
	route := rt.find(peer, 3200)
	if got, want := route.Distance, uint8(6); got != want {
		t.Fatalf("stored weighted route distance = %d, want %d", got, want)
	}
}

func TestSet26HopCountWeightAppliesToOutboundDDPWithoutMutatingInput(t *testing.T) {
	peer := &AURPPeer{timing: AURPConfig{HopCountWeight: 2}}
	pkt := new(ddp.ExtPacket)
	setDDPHopCount(pkt, 4)

	weighted, err := peer.applyAURPHopWeight(pkt)
	if err != nil {
		t.Fatal(err)
	}
	if got := ddpHopCount(weighted); got != 6 {
		t.Fatalf("weighted hop count = %d, want 6", got)
	}
	if got := ddpHopCount(pkt); got != 4 {
		t.Fatalf("input packet was mutated: hop count = %d, want 4", got)
	}
}

func TestSet26AlternativePathAvoidsIngressAURPTunnel(t *testing.T) {
	rt := NewRouteTable(t.Context())
	ingress := &AURPPeer{tunnelID: "cfg:ingress.example", RouteTable: rt}
	alternate := fakeTarget{key: "alternate-local", class: TargetClassAppleTalkPeer}

	if _, err := rt.UpsertRoute(ingress, true, 3300, 3300, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := rt.UpsertRoute(alternate, true, 3300, 3300, 2); err != nil {
		t.Fatal(err)
	}
	if err := rt.ReplaceZonesForNetwork(3300, "Alt Zone"); err != nil {
		t.Fatal(err)
	}

	if best := rt.Lookup(3300); best.Target != ingress {
		t.Fatalf("ordinary best route target = %v, want ingress AURP peer", best.Target)
	}
	got := rt.LookupAvoidingAURPTunnel(3300, ingress.TunnelID())
	if got.Zero() || got.Target.RouteTargetKey() != alternate.RouteTargetKey() {
		t.Fatalf("alternative route = %v, want %v", got, alternate)
	}

	rtr := &Router{RouteTable: rt}
	pkt := new(ddp.ExtPacket)
	pkt.DstNet = 3300
	route, err := rtr.outputRoute(pkt, ingress.TunnelID())
	if err != nil {
		t.Fatal(err)
	}
	if route.Target.RouteTargetKey() != alternate.RouteTargetKey() {
		t.Fatalf("output route = %v, want alternate", route)
	}
}

func TestSet26EqualDistanceRoutePreferenceIsDeterministic(t *testing.T) {
	rt := NewRouteTable(t.Context())
	aurpTarget := fakeTarget{key: "z-aurp", class: TargetClassAURPPeer}
	localTarget := fakeTarget{key: "y-local", class: TargetClassAppleTalkPeer}
	directTarget := fakeTarget{key: "x-direct", class: TargetClassDirect}

	for _, target := range []fakeTarget{aurpTarget, localTarget, directTarget} {
		if _, err := rt.UpsertRoute(target, true, 3310, 3310, 2); err != nil {
			t.Fatal(err)
		}
	}
	if err := rt.ReplaceZonesForNetwork(3310, "Tie Zone"); err != nil {
		t.Fatal(err)
	}
	if got := rt.Lookup(3310); got.Target.RouteTargetKey() != directTarget.RouteTargetKey() {
		t.Fatalf("equal-distance best route = %v, want direct target", got)
	}
}

func TestSet26StaticRemapMapsImportedRouteAndOutboundDestination(t *testing.T) {
	rt := NewRouteTable(t.Context())
	peer := &AURPPeer{
		tunnelID:   "cfg:remote.example",
		RouteTable: rt,
		timing: AURPConfig{RemapRules: []AURPRemapRule{{
			Peer:        "cfg:remote.example",
			RemoteStart: 100,
			RemoteEnd:   109,
			LocalStart:  5000,
			LocalEnd:    5009,
		}}},
	}

	accepted, err := peer.applyRIRspNetworkTuple(aurp.NetworkTuple{
		Extended:   true,
		RangeStart: 100,
		RangeEnd:   109,
		Distance:   1,
	})
	if err != nil || !accepted {
		t.Fatalf("remapped route accepted=%v err=%v", accepted, err)
	}
	if got := rt.find(peer, 5000); got.Zero() || got.NetEnd != 5009 {
		t.Fatalf("remapped route = %v, want 5000-5009", got)
	}

	pkt := new(ddp.ExtPacket)
	pkt.DstNet = 5004
	mapped, err := peer.remapOutboundDDP(pkt)
	if err != nil {
		t.Fatal(err)
	}
	if mapped.DstNet != 104 {
		t.Fatalf("outbound remapped destination = %d, want 104", mapped.DstNet)
	}
	if pkt.DstNet != 5004 {
		t.Fatal("outbound remap mutated caller packet")
	}
}

func TestSet26StaticRemapMapsInboundDDPAndNBPTuple(t *testing.T) {
	peer := &AURPPeer{
		tunnelID: "cfg:remote.example",
		timing: AURPConfig{RemapRules: []AURPRemapRule{{
			Peer:        "cfg:remote.example",
			RemoteStart: 100,
			RemoteEnd:   109,
			LocalStart:  5000,
			LocalEnd:    5009,
		}}},
	}
	nbpRaw, err := (&nbp.Packet{
		Function: nbp.FunctionLkUpReply,
		NBPID:    1,
		Tuples: []nbp.Tuple{{
			Network: 103,
			Node:    42,
			Socket:  2,
			Object:  "Printer",
			Type:    "LaserWriter",
			Zone:    "Remote",
		}},
	}).Marshal()
	if err != nil {
		t.Fatal(err)
	}
	pkt := &ddp.ExtPacket{
		ExtHeader: ddp.ExtHeader{
			SrcNet: 102,
			Proto:  ddp.ProtoNBP,
			Cksum:  0,
		},
		Data: nbpRaw,
	}
	if err := peer.remapInboundDDP(pkt); err != nil {
		t.Fatal(err)
	}
	if pkt.SrcNet != 5002 || pkt.Cksum != 0 {
		t.Fatalf("inbound DDP source/checksum = %d/%d, want 5002/0", pkt.SrcNet, pkt.Cksum)
	}
	parsed, err := nbp.Unmarshal(pkt.Data)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Tuples[0].Network != 5003 {
		t.Fatalf("remapped NBP tuple network = %d, want 5003", parsed.Tuples[0].Network)
	}
}

func TestSet26DeviceHidingFiltersOnlyMatchingNBPTuples(t *testing.T) {
	peer := &AURPPeer{
		tunnelID: "cfg:remote.example",
		timing: AURPConfig{HiddenDevices: []AURPDeviceHideRule{{
			Peer:      "cfg:remote.example",
			Type:      "LaserWriter",
			Direction: "import",
		}}},
	}
	raw, err := (&nbp.Packet{
		Function: nbp.FunctionLkUpReply,
		NBPID:    9,
		Tuples: []nbp.Tuple{
			{Network: 1, Node: 1, Socket: 2, Object: "Printer", Type: "LaserWriter", Zone: "Z"},
			{Network: 2, Node: 2, Socket: 2, Object: "Server", Type: "AFPServer", Zone: "Z"},
		},
	}).Marshal()
	if err != nil {
		t.Fatal(err)
	}
	pkt := &ddp.ExtPacket{
		ExtHeader: ddp.ExtHeader{Proto: ddp.ProtoNBP},
		Data:      raw,
	}
	filtered, drop, err := peer.filterDeviceNBP(pkt, "import")
	if err != nil {
		t.Fatal(err)
	}
	if drop {
		t.Fatal("mixed visible/hidden reply was dropped")
	}
	parsed, err := nbp.Unmarshal(filtered.Data)
	if err != nil {
		t.Fatal(err)
	}
	if len(parsed.Tuples) != 1 || parsed.Tuples[0].Type != "AFPServer" {
		t.Fatalf("filtered tuples = %v, want only AFPServer", parsed.Tuples)
	}
}

func TestSet26DeviceHidingDropsAllHiddenReply(t *testing.T) {
	peer := &AURPPeer{
		tunnelID: "cfg:remote.example",
		timing: AURPConfig{HiddenDevices: []AURPDeviceHideRule{{
			Type:      "*",
			Direction: "both",
		}}},
	}
	raw, err := (&nbp.Packet{
		Function: nbp.FunctionLkUpReply,
		NBPID:    1,
		Tuples: []nbp.Tuple{{
			Network: 1,
			Node:    1,
			Socket:  2,
			Object:  "Anything",
			Type:    "AnyType",
			Zone:    "Z",
		}},
	}).Marshal()
	if err != nil {
		t.Fatal(err)
	}
	_, drop, err := peer.filterDeviceNBP(&ddp.ExtPacket{
		ExtHeader: ddp.ExtHeader{Proto: ddp.ProtoNBP},
		Data:      raw,
	}, "export")
	if err != nil {
		t.Fatal(err)
	}
	if !drop {
		t.Fatal("all-hidden NBP reply was not dropped")
	}
}

func TestSet26ClusterCollapsesRemappedRoutesForRTMP(t *testing.T) {
	a := fakeTarget{key: "cluster-a", class: TargetClassAURPPeer}
	b := fakeTarget{key: "cluster-b", class: TargetClassAURPPeer}
	routes := []Route{
		testAURPRoute(a, 5000, 3),
		testAURPRoute(b, 5001, 2),
	}
	tuples := buildRTMPTuplesWithClusters(
		routes,
		AURPConfig{Clusters: []AURPClusterRule{{
			Start: 5000,
			End:   5009,
		}}},
		false,
	)
	if len(tuples) != 1 {
		t.Fatalf("clustered RTMP tuples = %v, want one tuple", tuples)
	}
	if tuples[0].RangeStart != 5000 ||
		tuples[0].RangeEnd != 5009 ||
		tuples[0].Distance != 2 {
		t.Fatalf("clustered tuple = %+v, want 5000-5009 distance 2", tuples[0])
	}
}

func TestSet26BackupPeerPenaltyRetainsButDeprioritizesRoute(t *testing.T) {
	rt := NewRouteTable(t.Context())
	primary := &AURPPeer{
		tunnelID:   "cfg:primary.example",
		RouteTable: rt,
	}
	backup := &AURPPeer{
		tunnelID:   "cfg:backup.example",
		RouteTable: rt,
		timing: AURPConfig{BackupPeers: []AURPBackupPeerRule{{
			Peer:    "cfg:backup.example",
			Penalty: 6,
		}}},
	}

	if ok, err := primary.applyRIRspNetworkTuple(aurp.NetworkTuple{
		Extended:   true,
		RangeStart: 3600,
		RangeEnd:   3600,
		Distance:   1,
	}); err != nil || !ok {
		t.Fatalf("primary route accepted=%v err=%v", ok, err)
	}
	if ok, err := backup.applyRIRspNetworkTuple(aurp.NetworkTuple{
		Extended:   true,
		RangeStart: 3600,
		RangeEnd:   3600,
		Distance:   1,
	}); err != nil || !ok {
		t.Fatalf("backup route accepted=%v err=%v", ok, err)
	}
	if err := rt.ReplaceZonesForNetwork(3600, "Backup Zone"); err != nil {
		t.Fatal(err)
	}

	best := rt.Lookup(3600)
	if best.Target != primary {
		t.Fatalf("best target with primary present = %v, want primary", best.Target)
	}
	if err := rt.DeleteRoute(primary, 3600); err != nil {
		t.Fatal(err)
	}
	best = rt.Lookup(3600)
	if best.Target != backup {
		t.Fatalf("best target after primary removal = %v, want backup", best.Target)
	}
	if best.Distance != 8 {
		t.Fatalf("backup stored distance = %d, want 8", best.Distance)
	}
}

func TestSet27BackupRouteReturnsToPrimaryWhenPrimaryRecovers(t *testing.T) {
	rt := NewRouteTable(t.Context())
	primary := &AURPPeer{
		tunnelID:   "cfg:primary-return.example",
		RouteTable: rt,
	}
	backup := &AURPPeer{
		tunnelID:   "cfg:backup-return.example",
		RouteTable: rt,
		timing: AURPConfig{BackupPeers: []AURPBackupPeerRule{{
			Peer:    "cfg:backup-return.example",
			Penalty: 6,
		}}},
	}
	tuple := aurp.NetworkTuple{
		Extended:   true,
		RangeStart: 3610,
		RangeEnd:   3610,
		Distance:   1,
	}

	if ok, err := primary.applyRIRspNetworkTuple(tuple); err != nil || !ok {
		t.Fatalf("initial primary accepted=%v err=%v", ok, err)
	}
	if ok, err := backup.applyRIRspNetworkTuple(tuple); err != nil || !ok {
		t.Fatalf("backup accepted=%v err=%v", ok, err)
	}
	if err := rt.ReplaceZonesForNetwork(3610, "Return Zone"); err != nil {
		t.Fatal(err)
	}
	if best := rt.Lookup(3610); best.Target != primary || best.Distance != 2 {
		t.Fatalf("initial best = %v distance %d, want primary distance 2", best.Target, best.Distance)
	}

	rt.DeleteTarget(primary)
	if best := rt.Lookup(3610); best.Target != backup || best.Distance != 8 {
		t.Fatalf("failover best = %v distance %d, want backup distance 8", best.Target, best.Distance)
	}

	if ok, err := primary.applyRIRspNetworkTuple(tuple); err != nil || !ok {
		t.Fatalf("recovered primary accepted=%v err=%v", ok, err)
	}
	if best := rt.Lookup(3610); best.Target != primary || best.Distance != 2 {
		t.Fatalf("recovery best = %v distance %d, want primary distance 2", best.Target, best.Distance)
	}
	if retained := rt.find(backup, 3610); retained.Zero() || retained.Distance != 8 {
		t.Fatalf("backup route was not retained after primary recovery: %v", retained)
	}
}

func TestSet26ZIPQueryChunkingHandlesLargeRouteUpdates(t *testing.T) {
	var networks []ddp.Network
	for n := ddp.Network(1); n <= 600; n++ {
		networks = append(networks, n)
	}
	packets, err := buildZIPQueryPackets(networks)
	if err != nil {
		t.Fatal(err)
	}
	if len(packets) != 3 {
		t.Fatalf("ZIP query packets = %d, want 3", len(packets))
	}
	counts := []int{255, 255, 90}
	for i, data := range packets {
		query, err := zip.UnmarshalQueryPacket(data)
		if err != nil {
			t.Fatal(err)
		}
		if len(query.Networks) != counts[i] {
			t.Fatalf("ZIP query %d networks = %d, want %d", i, len(query.Networks), counts[i])
		}
	}
}

func TestSet26ImportHidingSuppressesOnlyMatchingPeerRoutes(t *testing.T) {
	rt := NewRouteTable(t.Context())
	hiddenPeer := &AURPPeer{
		tunnelID:   "cfg:hidden.example",
		RouteTable: rt,
		timing: AURPConfig{HiddenImportNetworks: []AURPImportHideRule{{
			Peer:  "cfg:hidden.example",
			Start: 200,
			End:   209,
		}}},
	}
	otherPeer := &AURPPeer{
		tunnelID:   "cfg:other.example",
		RouteTable: rt,
		timing:     hiddenPeer.timing,
	}

	accepted, err := hiddenPeer.applyRIRspNetworkTuple(aurp.NetworkTuple{
		Extended:   true,
		RangeStart: 200,
		RangeEnd:   209,
		Distance:   1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if accepted || !rt.find(hiddenPeer, 200).Zero() {
		t.Fatal("peer-scoped hidden import route was installed")
	}

	accepted, err = otherPeer.applyRIRspNetworkTuple(aurp.NetworkTuple{
		Extended:   true,
		RangeStart: 200,
		RangeEnd:   209,
		Distance:   1,
	})
	if err != nil || !accepted {
		t.Fatalf("nonmatching peer route accepted=%v err=%v", accepted, err)
	}
	if rt.find(otherPeer, 200).Zero() {
		t.Fatal("nonmatching peer route was incorrectly hidden")
	}
}

func TestSet26DDPChecksumVerificationBeforeRemap(t *testing.T) {
	peer := &AURPPeer{
		tunnelID: "cfg:checksum.example",
		timing: AURPConfig{RemapRules: []AURPRemapRule{{
			Peer:        "cfg:checksum.example",
			RemoteStart: 100,
			RemoteEnd:   109,
			LocalStart:  5000,
			LocalEnd:    5009,
		}}},
	}
	pkt := &ddp.ExtPacket{
		ExtHeader: ddp.ExtHeader{
			Size:      13 + 3,
			SrcNet:    102,
			DstNet:    900,
			SrcNode:   1,
			DstNode:   2,
			SrcSocket: 4,
			DstSocket: 4,
			Proto:     ddp.ProtoAEP,
		},
		Data: []byte{1, 2, 3},
	}
	checksum, err := computeDDPChecksum(pkt)
	if err != nil {
		t.Fatal(err)
	}
	pkt.Cksum = checksum
	if err := peer.remapInboundDDP(pkt); err != nil {
		t.Fatal(err)
	}
	if pkt.SrcNet != 5002 || pkt.Cksum != 0 {
		t.Fatalf("valid remap source/checksum = %d/0x%04x, want 5002/0", pkt.SrcNet, pkt.Cksum)
	}

	bad := &ddp.ExtPacket{
		ExtHeader: ddp.ExtHeader{
			Size:      13 + 3,
			SrcNet:    103,
			DstNet:    900,
			SrcNode:   1,
			DstNode:   2,
			SrcSocket: 4,
			DstSocket: 4,
			Proto:     ddp.ProtoAEP,
		},
		Data: []byte{4, 5, 6},
	}
	checksum, err = computeDDPChecksum(bad)
	if err != nil {
		t.Fatal(err)
	}
	bad.Cksum = checksum
	bad.Data[0] ^= 0xff
	if err := peer.remapInboundDDP(bad); err == nil {
		t.Fatal("remapping accepted packet with corrupt original checksum")
	}
	if bad.SrcNet != 103 {
		t.Fatalf("failed checksum remap mutated source network to %d", bad.SrcNet)
	}
}

func TestSet27RemapNoPolicyPreservesPayloadBacking(t *testing.T) {
	data := []byte{1, 2, 3, 4}
	packet := &ddp.ExtPacket{
		ExtHeader: ddp.ExtHeader{
			SrcNet: 101,
			DstNet: 202,
			Proto:  ddp.ProtoAEP,
		},
		Data: data,
	}
	before := &packet.Data[0]
	peer := &AURPPeer{}
	if err := peer.remapInboundDDP(packet); err != nil {
		t.Fatal(err)
	}
	if &packet.Data[0] != before {
		t.Fatal("no-policy inbound remap copied payload data")
	}
}

func TestSet27HeaderOnlyRemapPreservesPayloadBacking(t *testing.T) {
	data := []byte{5, 6, 7, 8}
	packet := &ddp.ExtPacket{
		ExtHeader: ddp.ExtHeader{
			SrcNet: 102,
			DstNet: 900,
			Proto:  ddp.ProtoAEP,
		},
		Data: data,
	}
	before := &packet.Data[0]
	peer := &AURPPeer{
		tunnelID: "cfg:fast-remap.example",
		timing: AURPConfig{RemapRules: []AURPRemapRule{{
			Peer:        "cfg:fast-remap.example",
			RemoteStart: 100,
			RemoteEnd:   109,
			LocalStart:  5000,
			LocalEnd:    5009,
		}}},
	}
	if err := peer.remapInboundDDP(packet); err != nil {
		t.Fatal(err)
	}
	if packet.SrcNet != 5002 {
		t.Fatalf("remapped source = %d, want 5002", packet.SrcNet)
	}
	if &packet.Data[0] != before {
		t.Fatal("header-only inbound remap copied payload data")
	}

	outbound := &ddp.ExtPacket{
		ExtHeader: ddp.ExtHeader{
			DstNet: 5004,
			Proto:  ddp.ProtoAEP,
		},
		Data: data,
	}
	outBefore := &outbound.Data[0]
	mapped, err := peer.remapOutboundDDP(outbound)
	if err != nil {
		t.Fatal(err)
	}
	if mapped.DstNet != 104 {
		t.Fatalf("outbound remapped destination = %d, want 104", mapped.DstNet)
	}
	if &mapped.Data[0] != outBefore {
		t.Fatal("header-only outbound remap copied payload data")
	}
}
