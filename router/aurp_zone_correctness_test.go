package router

import (
	"fmt"
	"slices"
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
		map[uint16][]string{2500: zones},
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
