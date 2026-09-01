package router

import (
	"fmt"
	"slices"
	"testing"
	"time"

	"drjosh.dev/jrouter/aurp"
	"github.com/sfiera/multitalk/pkg/ddp"
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

func TestSet9ZCReservedDoesNotMutateState(t *testing.T) {
	rt := NewRouteTable(t.Context())
	peer := &AURPPeer{
		tunnelID:   "cfg:zc.example",
		RouteTable: rt,
	}
	if _, err := rt.UpsertRoute(peer, true, 2000, 2000, 3); err != nil {
		t.Fatal(err)
	}
	if err := rt.ReplaceZonesForNetwork(2000, "Original Zone"); err != nil {
		t.Fatal(err)
	}

	before := rt.find(peer, 2000)
	needZone, err := peer.applyRIUpdEvent(aurp.EventTuple{
		EventCode:  aurp.EventCodeZC,
		Extended:   true,
		RangeStart: 2000,
		RangeEnd:   2000,
		Distance:   1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if needZone {
		t.Fatal("reserved ZC unexpectedly requested zone information")
	}
	after := rt.find(peer, 2000)
	if before.Distance != after.Distance ||
		before.NetEnd != after.NetEnd ||
		!slices.Equal(before.ZoneNames(), after.ZoneNames()) {
		t.Fatalf("reserved ZC mutated route or zone state: before=%v after=%v", before, after)
	}
}

func TestSet9ReplaceZonesRemovesStaleReverseIndex(t *testing.T) {
	rt := NewRouteTable(t.Context())
	peer := &AURPPeer{
		tunnelID:   "cfg:zones.example",
		RouteTable: rt,
	}
	if _, err := rt.UpsertRoute(peer, true, 2100, 2100, 2); err != nil {
		t.Fatal(err)
	}
	if err := rt.AddZonesToNetwork(2100, "Old Zone", "Shared Zone"); err != nil {
		t.Fatal(err)
	}
	if err := rt.ReplaceZonesForNetwork(2100, "Shared Zone", "New Zone", "New Zone"); err != nil {
		t.Fatal(err)
	}

	got := rt.find(peer, 2100).ZoneNames()
	slices.Sort(got)
	want := []string{"New Zone", "Shared Zone"}
	if !slices.Equal(got, want) {
		t.Fatalf("zones = %v, want %v", got, want)
	}
	if routes := rt.RoutesForZone("Old Zone"); len(routes) != 0 {
		t.Fatalf("stale reverse index retained Old Zone: %v", routes)
	}
	if routes := rt.RoutesForZone("New Zone"); len(routes) != 1 {
		t.Fatalf("new reverse index routes = %d, want 1", len(routes))
	}
}

func TestSet9NonExtendedZIRspReplacesAndDeduplicates(t *testing.T) {
	rt := NewRouteTable(t.Context())
	peer := &AURPPeer{
		tunnelID:   "cfg:nonext.example",
		RouteTable: rt,
	}
	if _, err := rt.UpsertRoute(peer, true, 2200, 2200, 2); err != nil {
		t.Fatal(err)
	}
	if err := rt.ReplaceZonesForNetwork(2200, "Stale Zone"); err != nil {
		t.Fatal(err)
	}

	accepted, ignored := peer.applyNonExtendedZIRsp(&aurp.ZIRspPacket{
		Subcode: aurp.SubcodeZoneInfoNonExt,
		Zones: aurp.ZoneTuples{
			{Network: 2200, Name: "Zone A"},
			{Network: 2200, Name: "Zone B"},
			{Network: 2200, Name: "Zone A"},
			{Network: 2299, Name: "Foreign"},
		},
	})
	if accepted != 3 || ignored != 1 {
		t.Fatalf("accepted/ignored = %d/%d, want 3/1", accepted, ignored)
	}

	got := rt.find(peer, 2200).ZoneNames()
	slices.Sort(got)
	want := []string{"Zone A", "Zone B"}
	if !slices.Equal(got, want) {
		t.Fatalf("zones = %v, want %v", got, want)
	}
	if len(rt.RoutesForZone("Stale Zone")) != 0 {
		t.Fatal("stale zone survived complete nonextended replacement")
	}
}

func TestSet9ExtendedZIRspPublishesOnlyWhenComplete(t *testing.T) {
	rt := NewRouteTable(t.Context())
	peer := &AURPPeer{
		tunnelID:   "cfg:extended.example",
		RouteTable: rt,
	}
	if _, err := rt.UpsertRoute(peer, true, 2300, 2300, 2); err != nil {
		t.Fatal(err)
	}
	if err := rt.ReplaceZonesForNetwork(2300, "Old Complete Zone"); err != nil {
		t.Fatal(err)
	}

	complete, _, err := peer.applyExtendedZIRsp(&aurp.ZIRspPacket{
		Subcode:     aurp.SubcodeZoneInfoExt,
		TotalTuples: 3,
		Zones: aurp.ZoneTuples{
			{Network: 2300, Name: "Zone A"},
			{Network: 2300, Name: "Zone B"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if complete {
		t.Fatal("partial extended zone list published as complete")
	}
	if got := rt.find(peer, 2300).ZoneNames(); !slices.Equal(got, []string{"Old Complete Zone"}) {
		t.Fatalf("partial response changed published zones: %v", got)
	}

	complete, _, err = peer.applyExtendedZIRsp(&aurp.ZIRspPacket{
		Subcode:     aurp.SubcodeZoneInfoExt,
		TotalTuples: 3,
		Zones: aurp.ZoneTuples{
			{Network: 2300, Name: "Zone C"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !complete {
		t.Fatal("complete extended zone list was not published")
	}
	got := rt.find(peer, 2300).ZoneNames()
	slices.Sort(got)
	want := []string{"Zone A", "Zone B", "Zone C"}
	if !slices.Equal(got, want) {
		t.Fatalf("zones = %v, want %v", got, want)
	}
	if len(rt.RoutesForZone("Old Complete Zone")) != 0 {
		t.Fatal("old zone survived completed extended replacement")
	}
}

func TestSet9ExtendedAssemblyClearedOnReceiverReset(t *testing.T) {
	peer := newRestartTestPeer(t)
	if _, err := peer.RouteTable.UpsertRoute(peer, true, 2400, 2400, 2); err != nil {
		t.Fatal(err)
	}
	if _, _, err := peer.applyExtendedZIRsp(&aurp.ZIRspPacket{
		Subcode:     aurp.SubcodeZoneInfoExt,
		TotalTuples: 2,
		Zones: aurp.ZoneTuples{
			{Network: 2400, Name: "First"},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if peer.pendingZoneInfo == nil {
		t.Fatal("extended zone assembly was not retained")
	}

	oldConnID := peer.Transport.LocalConnID()
	peer.disconnectReceiver()
	if peer.pendingZoneInfo != nil {
		t.Fatal("receiver reset retained partial extended zone assembly")
	}

	late := &aurp.ZIRspPacket{
		Header: aurp.Header{
			TrHeader: aurp.TrHeader{
				ConnectionID: oldConnID,
				Sequence:     0,
			},
			CommandCode: aurp.CmdCodeZoneRsp,
		},
		Subcode:     aurp.SubcodeZoneInfoExt,
		TotalTuples: 2,
		Zones: aurp.ZoneTuples{
			{Network: 2400, Name: "Late"},
		},
	}
	if err := peer.handleZIRsp(peer.logger, late); err != nil {
		t.Fatal(err)
	}
	if peer.pendingZoneInfo != nil {
		t.Fatal("late old-connection ZI-Rsp recreated pending assembly")
	}
}

func TestSet9ZIRspSenderUsesExtendedPacketsWhenNeeded(t *testing.T) {
	peer := newRestartTestPeer(t)

	var zones []string
	for i := 0; i < 40; i++ {
		zones = append(zones, fmt.Sprintf(
			"Set9 very long test zone %02d %s",
			i,
			"abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789",
		))
	}
	packets, err := buildZIRspPackets(
		peer.Transport,
		map[ddp.Network][]string{2500: zones},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(packets) < 2 {
		t.Fatalf("large zone list produced %d packet(s), want multiple", len(packets))
	}
	for i, pkt := range packets {
		if pkt.Subcode != aurp.SubcodeZoneInfoExt {
			t.Fatalf("packet %d subcode = %d, want extended", i, pkt.Subcode)
		}
		if int(pkt.TotalTuples) != len(zones) {
			t.Fatalf("packet %d total = %d, want %d", i, pkt.TotalTuples, len(zones))
		}
		size, err := aurpPacketSize(pkt)
		if err != nil {
			t.Fatal(err)
		}
		if size > aurpRoutingDatagramBudget {
			t.Fatalf("packet %d size = %d, budget = %d", i, size, aurpRoutingDatagramBudget)
		}
		for _, zt := range pkt.Zones {
			if zt.Network != 2500 {
				t.Fatalf("packet %d contains network %d, want 2500", i, zt.Network)
			}
		}
	}
}

func TestSet26MissingZIResponseIsRetried(t *testing.T) {
	peer := newRestartTestPeer(t)
	if _, err := peer.RouteTable.UpsertRoute(peer, true, 2600, 2600, 2); err != nil {
		t.Fatal(err)
	}
	peer.markZoneInfoPending(2600)
	peer.pendingZoneInfo[2600].lastActivity = time.Now().Add(-2 * aurpZoneInfoRetryTimer)

	before := len(peer.DumpChatLog())
	if err := peer.retryIncompleteZoneInfo(time.Now()); err != nil {
		t.Fatal(err)
	}
	entries := peer.DumpChatLog()
	if len(entries) != before+1 {
		t.Fatalf("chat log entries = %d, want %d", len(entries), before+1)
	}
	req, ok := entries[len(entries)-1].Packet.(*aurp.ZIReqPacket)
	if !ok {
		t.Fatalf("retry packet = %T, want *aurp.ZIReqPacket", entries[len(entries)-1].Packet)
	}
	if !slices.Equal(req.Networks, []ddp.Network{2600}) {
		t.Fatalf("ZI-Req networks = %v, want [2600]", req.Networks)
	}
	if req.ConnectionID != peer.Transport.LocalConnID() || req.Sequence != 0 {
		t.Fatalf("ZI-Req transport = conn %d seq %d, want conn %d seq 0", req.ConnectionID, req.Sequence, peer.Transport.LocalConnID())
	}
}

func TestSet26PartialExtendedZIIsRetriedUntilComplete(t *testing.T) {
	peer := newRestartTestPeer(t)
	if _, err := peer.RouteTable.UpsertRoute(peer, true, 2700, 2700, 2); err != nil {
		t.Fatal(err)
	}
	peer.markZoneInfoPending(2700)

	complete, _, err := peer.applyExtendedZIRsp(&aurp.ZIRspPacket{
		Subcode:     aurp.SubcodeZoneInfoExt,
		TotalTuples: 2,
		Zones: aurp.ZoneTuples{
			{Network: 2700, Name: "Zone A"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if complete {
		t.Fatal("partial extended response reported complete")
	}
	peer.pendingZoneInfo[2700].lastActivity = time.Now().Add(-2 * aurpZoneInfoRetryTimer)

	before := len(peer.DumpChatLog())
	if err := peer.retryIncompleteZoneInfo(time.Now()); err != nil {
		t.Fatal(err)
	}
	entries := peer.DumpChatLog()
	if len(entries) != before+1 {
		t.Fatalf("partial extended response did not trigger one retry")
	}
	if _, ok := entries[len(entries)-1].Packet.(*aurp.ZIReqPacket); !ok {
		t.Fatalf("retry packet = %T, want *aurp.ZIReqPacket", entries[len(entries)-1].Packet)
	}

	complete, _, err = peer.applyExtendedZIRsp(&aurp.ZIRspPacket{
		Subcode:     aurp.SubcodeZoneInfoExt,
		TotalTuples: 2,
		Zones: aurp.ZoneTuples{
			{Network: 2700, Name: "Zone B"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !complete {
		t.Fatal("completed extended response remained incomplete")
	}
	if _, ok := peer.pendingZoneInfo[2700]; ok {
		t.Fatal("completed zone list remained pending")
	}

	before = len(peer.DumpChatLog())
	if err := peer.retryIncompleteZoneInfo(time.Now().Add(10 * aurpZoneInfoRetryTimer)); err != nil {
		t.Fatal(err)
	}
	if got := len(peer.DumpChatLog()); got != before {
		t.Fatalf("completed zone list was retried: chat log %d -> %d", before, got)
	}
}

func TestSet26RIRspRequestsZoneInfoOnlyWhenMissing(t *testing.T) {
	peer := newRestartTestPeer(t)
	peer.setRState(ReceiverConnected)
	peer.Transport.ResetRemoteSeq()

	if _, err := peer.RouteTable.UpsertRoute(peer, true, 2800, 2800, 2); err != nil {
		t.Fatal(err)
	}
	if err := peer.RouteTable.ReplaceZonesForNetwork(2800, "Known Zone"); err != nil {
		t.Fatal(err)
	}

	known := &aurp.RIRspPacket{
		Header: aurp.Header{
			TrHeader: aurp.TrHeader{
				ConnectionID: peer.Transport.LocalConnID(),
				Sequence:     peer.Transport.RemoteSeq(),
			},
			CommandCode: aurp.CmdCodeRIRsp,
		},
		Networks: aurp.NetworkTuples{{
			Extended: true, RangeStart: 2800, RangeEnd: 2800, Distance: 1,
		}},
	}
	if err := peer.handleRIRsp(peer.logger, known); err != nil {
		t.Fatal(err)
	}
	entries := peer.DumpChatLog()
	ack, ok := entries[len(entries)-1].Packet.(*aurp.RIAckPacket)
	if !ok {
		t.Fatalf("response to known-zone RI-Rsp = %T, want RI-Ack", entries[len(entries)-1].Packet)
	}
	if ack.Flags&aurp.RoutingFlagSendZoneInfo != 0 {
		t.Fatal("SZI requested for network with complete zone information")
	}

	missing := &aurp.RIRspPacket{
		Header: aurp.Header{
			TrHeader: aurp.TrHeader{
				ConnectionID: peer.Transport.LocalConnID(),
				Sequence:     peer.Transport.RemoteSeq(),
			},
			CommandCode: aurp.CmdCodeRIRsp,
		},
		Networks: aurp.NetworkTuples{{
			Extended: true, RangeStart: 2801, RangeEnd: 2801, Distance: 1,
		}},
	}
	if err := peer.handleRIRsp(peer.logger, missing); err != nil {
		t.Fatal(err)
	}
	entries = peer.DumpChatLog()
	ack, ok = entries[len(entries)-1].Packet.(*aurp.RIAckPacket)
	if !ok {
		t.Fatalf("response to missing-zone RI-Rsp = %T, want RI-Ack", entries[len(entries)-1].Packet)
	}
	if ack.Flags&aurp.RoutingFlagSendZoneInfo == 0 {
		t.Fatal("SZI was not requested for network lacking zone information")
	}
	if _, ok := peer.pendingZoneInfo[2801]; !ok {
		t.Fatal("missing-zone network was not tracked for retry")
	}
}

func TestSet26RepeatedPendingMarkPreservesExtendedAssembly(t *testing.T) {
	peer := newRestartTestPeer(t)
	if _, err := peer.RouteTable.UpsertRoute(peer, true, 2900, 2900, 2); err != nil {
		t.Fatal(err)
	}
	peer.markZoneInfoPending(2900)

	complete, _, err := peer.applyExtendedZIRsp(&aurp.ZIRspPacket{
		Subcode:     aurp.SubcodeZoneInfoExt,
		TotalTuples: 2,
		Zones: aurp.ZoneTuples{
			{Network: 2900, Name: "First"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if complete {
		t.Fatal("one of two zone tuples reported complete")
	}
	before := peer.pendingZoneInfo[2900]
	if before == nil || !before.zones.Contains("First") {
		t.Fatal("first extended fragment was not retained")
	}

	peer.markZoneInfoPending(2900)
	after := peer.pendingZoneInfo[2900]
	if after != before || !after.zones.Contains("First") {
		t.Fatal("repeated pending mark reset partial extended ZI assembly")
	}
}

func TestSet26RouteDeletionClearsPendingZoneInfoImmediately(t *testing.T) {
	peer := newRestartTestPeer(t)
	if _, err := peer.RouteTable.UpsertRoute(peer, true, 2910, 2910, 2); err != nil {
		t.Fatal(err)
	}
	peer.markZoneInfoPending(2910)
	if _, ok := peer.pendingZoneInfo[2910]; !ok {
		t.Fatal("pending zone state not created")
	}

	if _, err := peer.applyRIUpdEvent(aurp.EventTuple{
		EventCode:  aurp.EventCodeND,
		Extended:   true,
		RangeStart: 2910,
		RangeEnd:   2910,
	}); err != nil {
		t.Fatal(err)
	}
	if _, ok := peer.pendingZoneInfo[2910]; ok {
		t.Fatal("route deletion left stale pending zone state")
	}
}

func TestSet26LoopIndicativeRouteDetection(t *testing.T) {
	rt := NewRouteTable(t.Context())
	direct := fakeTarget{key: "direct-loop-test", class: TargetClassDirect}
	peer := &AURPPeer{
		tunnelID:   "cfg:loop.example",
		RouteTable: rt,
	}

	if _, err := rt.UpsertRoute(direct, true, 100, 109, 0); err != nil {
		t.Fatal(err)
	}
	if err := rt.ReplaceZonesForNetwork(100, "Engineering", "Shared"); err != nil {
		t.Fatal(err)
	}
	if _, err := rt.UpsertRoute(peer, true, 500, 509, 2); err != nil {
		t.Fatal(err)
	}

	accepted, ignored := peer.applyNonExtendedZIRsp(&aurp.ZIRspPacket{
		Subcode: aurp.SubcodeZoneInfoNonExt,
		Zones: aurp.ZoneTuples{
			{Network: 500, Name: "Shared"},
			{Network: 500, Name: "Engineering"},
		},
	})
	if accepted != 2 || ignored != 0 {
		t.Fatalf("zone tuples accepted/ignored = %d/%d, want 2/0", accepted, ignored)
	}
	if got := peer.LoopIndicativeRoutes(); got != 1 {
		t.Fatalf("loop-indicative counter = %d, want 1", got)
	}
}

func TestSet26LoopIndicativeRequiresExactRangeSizeAndZones(t *testing.T) {
	rt := NewRouteTable(t.Context())
	direct := fakeTarget{key: "direct-loop-negative", class: TargetClassDirect}
	peer := &AURPPeer{
		tunnelID:   "cfg:loop-negative.example",
		RouteTable: rt,
	}

	if _, err := rt.UpsertRoute(direct, true, 100, 109, 0); err != nil {
		t.Fatal(err)
	}
	if err := rt.ReplaceZonesForNetwork(100, "Engineering", "Shared"); err != nil {
		t.Fatal(err)
	}
	if _, err := rt.UpsertRoute(peer, true, 600, 608, 2); err != nil {
		t.Fatal(err)
	}
	peer.applyNonExtendedZIRsp(&aurp.ZIRspPacket{
		Subcode: aurp.SubcodeZoneInfoNonExt,
		Zones: aurp.ZoneTuples{
			{Network: 600, Name: "Engineering"},
			{Network: 600, Name: "Shared"},
		},
	})
	if got := peer.LoopIndicativeRoutes(); got != 0 {
		t.Fatalf("different range size produced %d loop indication(s)", got)
	}
}

func TestSet26LoopProbeDestinationMapsMatchingOffset(t *testing.T) {
	local := Route{NetEnd: 109}
	local.NetStart = 100
	remote := Route{NetEnd: 509}
	remote.NetStart = 500
	dst, err := loopProbeDestination(
		ddp.Addr{Network: 104, Node: 42},
		local,
		remote,
	)
	if err != nil {
		t.Fatal(err)
	}
	if dst != 504 {
		t.Fatalf("loop-probe destination = %d, want 504", dst)
	}
}

func TestSet26LoopProbeReturnSignalsPeerOnlyOnMatchingPort(t *testing.T) {
	peer := newRestartTestPeer(t)
	peer.loopDetectedCh = make(chan struct{}, 1)
	portA := &EtherTalkPort{device: "a"}
	portB := &EtherTalkPort{device: "b"}
	token := []byte("set26-loop-probe")
	investigation := &loopProbeInvestigation{
		key:   "cfg:test|500",
		token: token,
		peer:  peer,
		port:  portA,
	}
	rtr := &Router{
		Logger: testLogger(t),
		loopProbes: map[string]*loopProbeInvestigation{
			string(token): investigation,
		},
		loopProbeByKey: map[string]string{
			investigation.key: string(token),
		},
	}

	if rtr.handleLoopProbeReturn(portB, new(ddp.ExtPacket), token) {
		t.Fatal("probe returning on wrong local port confirmed loop")
	}
	if !rtr.handleLoopProbeReturn(portA, new(ddp.ExtPacket), token) {
		t.Fatal("matching returned probe did not confirm loop")
	}
	if got := peer.ConfirmedRoutingLoops(); got != 1 {
		t.Fatalf("confirmed loop counter = %d, want 1", got)
	}
	select {
	case <-peer.loopDetectedCh:
	default:
		t.Fatal("confirmed loop did not signal peer handler")
	}
}

func TestSet26ConfirmedLoopDisablesPeerAndRemovesRoutes(t *testing.T) {
	peer := newRestartTestPeer(t)
	peer.disconnectSender()
	if _, err := peer.RouteTable.UpsertRoute(
		peer,
		true,
		3500,
		3500,
		2,
	); err != nil {
		t.Fatal(err)
	}
	peer.handleConfirmedRoutingLoop()
	if !peer.LoopDisabled() {
		t.Fatal("confirmed loop did not disable peer")
	}
	if got := peer.RouteTable.find(peer, 3500); !got.Zero() {
		t.Fatalf("route survived confirmed loop: %v", got)
	}
}
