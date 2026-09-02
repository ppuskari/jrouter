package router

import (
	"slices"
	"testing"
	"time"

	"drjosh.dev/jrouter/aurp"
	"github.com/sfiera/multitalk/pkg/ddp"
)

func TestSet28ExtendedZIReorderAndDuplicateAreIdempotent(t *testing.T) {
	peer := newRestartTestPeer(t)
	if _, err := peer.RouteTable.UpsertRoute(peer, true, 3800, 3800, 2); err != nil {
		t.Fatal(err)
	}
	if err := peer.RouteTable.ReplaceZonesForNetwork(3800, "Old Zone"); err != nil {
		t.Fatal(err)
	}

	fragments := []*aurp.ZIRspPacket{
		{
			Subcode:     aurp.SubcodeZoneInfoExt,
			TotalTuples: 3,
			Zones: aurp.ZoneTuples{
				{Network: 3800, Name: "Zone C"},
			},
		},
		{
			Subcode:     aurp.SubcodeZoneInfoExt,
			TotalTuples: 3,
			Zones: aurp.ZoneTuples{
				{Network: 3800, Name: "Zone A"},
				{Network: 3800, Name: "Zone B"},
			},
		},
	}

	complete, _, err := peer.applyExtendedZIRsp(fragments[0])
	if err != nil {
		t.Fatal(err)
	}
	if complete {
		t.Fatal("single reordered fragment completed zone list")
	}

	complete, _, err = peer.applyExtendedZIRsp(fragments[0])
	if err != nil {
		t.Fatal(err)
	}
	if complete {
		t.Fatal("duplicate fragment completed zone list")
	}
	if got := len(peer.pendingZoneInfo[3800].zones); got != 1 {
		t.Fatalf("duplicate fragment grew partial set to %d, want 1", got)
	}

	complete, _, err = peer.applyExtendedZIRsp(fragments[1])
	if err != nil {
		t.Fatal(err)
	}
	if !complete {
		t.Fatal("reordered complete extended zone list did not publish")
	}
	got := peer.RouteTable.find(peer, 3800).ZoneNames()
	slices.Sort(got)
	want := []string{"Zone A", "Zone B", "Zone C"}
	if !slices.Equal(got, want) {
		t.Fatalf("published zones = %v, want %v", got, want)
	}
	if _, ok := peer.pendingZoneInfo[3800]; ok {
		t.Fatal("completed extended zone assembly remained pending")
	}
}

func TestSet28MalformedExtendedZIDoesNotContaminatePartialAssembly(t *testing.T) {
	peer := newRestartTestPeer(t)
	for _, network := range []ddp.Network{3810, 3811} {
		if _, err := peer.RouteTable.UpsertRoute(
			peer, true, network, network, 2,
		); err != nil {
			t.Fatal(err)
		}
	}

	complete, _, err := peer.applyExtendedZIRsp(&aurp.ZIRspPacket{
		Subcode:     aurp.SubcodeZoneInfoExt,
		TotalTuples: 3,
		Zones: aurp.ZoneTuples{
			{Network: 3810, Name: "Good A"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if complete {
		t.Fatal("initial partial fragment completed unexpectedly")
	}

	pending := peer.pendingZoneInfo[3810]
	if pending == nil || !pending.zones.Contains("Good A") {
		t.Fatal("initial valid partial fragment was not retained")
	}

	_, _, err = peer.applyExtendedZIRsp(&aurp.ZIRspPacket{
		Subcode:     aurp.SubcodeZoneInfoExt,
		TotalTuples: 3,
		Zones: aurp.ZoneTuples{
			{Network: 3810, Name: "Bad Injection"},
			{Network: 3811, Name: "Wrong Network"},
		},
	})
	if err == nil {
		t.Fatal("mixed-network extended ZI fragment was accepted")
	}
	if got := peer.pendingZoneInfo[3810]; got != pending {
		t.Fatal("malformed fragment replaced the valid partial assembly")
	}
	if pending.zones.Contains("Bad Injection") {
		t.Fatal("malformed fragment contaminated partial zone assembly")
	}
	if got := len(pending.zones); got != 1 {
		t.Fatalf("partial zones after malformed fragment = %d, want 1", got)
	}
}

func TestSet28ExtendedZIConflictingTotalPreservesProgress(t *testing.T) {
	peer := newRestartTestPeer(t)
	if _, err := peer.RouteTable.UpsertRoute(peer, true, 3820, 3820, 2); err != nil {
		t.Fatal(err)
	}
	if _, _, err := peer.applyExtendedZIRsp(&aurp.ZIRspPacket{
		Subcode:     aurp.SubcodeZoneInfoExt,
		TotalTuples: 3,
		Zones: aurp.ZoneTuples{
			{Network: 3820, Name: "Zone A"},
		},
	}); err != nil {
		t.Fatal(err)
	}

	pending := peer.pendingZoneInfo[3820]
	if pending == nil || pending.total != 3 {
		t.Fatalf("initial pending assembly = %+v, want total 3", pending)
	}

	_, _, err := peer.applyExtendedZIRsp(&aurp.ZIRspPacket{
		Subcode:     aurp.SubcodeZoneInfoExt,
		TotalTuples: 2,
		Zones: aurp.ZoneTuples{
			{Network: 3820, Name: "Conflicting"},
		},
	})
	if err == nil {
		t.Fatal("conflicting extended ZI total was accepted")
	}
	if got := peer.pendingZoneInfo[3820]; got != pending {
		t.Fatal("conflicting total replaced valid partial assembly")
	}
	if pending.total != 3 || !pending.zones.Contains("Zone A") ||
		pending.zones.Contains("Conflicting") {
		t.Fatalf("partial assembly mutated by conflicting total: %+v", pending)
	}
}

func TestSet28RIRspRetryResendsOutstandingChunkBeforeAdvancing(t *testing.T) {
	peer := newRestartTestPeer(t)
	peer.Transport.ResetLocalSeq()
	peer.pendingRIRsp = []aurp.NetworkTuples{
		{{Extended: true, RangeStart: 3900, RangeEnd: 3900, Distance: 1}},
		{{Extended: true, RangeStart: 3901, RangeEnd: 3901, Distance: 1}},
	}
	if err := peer.sendNextRIRsp(false); err != nil {
		t.Fatal(err)
	}
	first, ok := peer.lastRISent.(*aurp.RIRspPacket)
	if !ok {
		t.Fatalf("first outstanding packet = %T, want RI-Rsp", peer.lastRISent)
	}
	if first.Sequence != 1 || len(peer.pendingRIRsp) != 1 {
		t.Fatalf("initial RI-Rsp seq/pending = %d/%d, want 1/1",
			first.Sequence, len(peer.pendingRIRsp))
	}

	beforeLog := len(peer.DumpChatLog())
	peer.lastSend.Store(time.Now().Add(-2 * peer.retryInterval()))
	if err := peer.stickerTasks(); err != nil {
		t.Fatal(err)
	}
	entries := peer.DumpChatLog()
	if len(entries) != beforeLog+1 {
		t.Fatalf("retry chat entries = %d, want %d", len(entries), beforeLog+1)
	}
	if entries[len(entries)-1].Packet != first {
		t.Fatal("RI-Rsp retry did not resend the exact outstanding packet")
	}
	if peer.Transport.LocalSeq() != 1 || len(peer.pendingRIRsp) != 1 {
		t.Fatalf("retry advanced seq/pending to %d/%d, want 1/1",
			peer.Transport.LocalSeq(), len(peer.pendingRIRsp))
	}

	ack := peer.Transport.NewRIAckPacket(
		peer.Transport.RemoteConnID(),
		first.Sequence,
		0,
	)
	if err := peer.handleRIAck(peer.logger, ack); err != nil {
		t.Fatal(err)
	}
	second, ok := peer.lastRISent.(*aurp.RIRspPacket)
	if !ok {
		t.Fatalf("second outstanding packet = %T, want RI-Rsp", peer.lastRISent)
	}
	if second == first || second.Sequence != 2 || len(peer.pendingRIRsp) != 0 {
		t.Fatalf("ACK advancement seq/pending = %d/%d, want 2/0",
			second.Sequence, len(peer.pendingRIRsp))
	}
}

func TestSet28RIUpdRetryResendsExactOutstandingPacket(t *testing.T) {
	peer := newRestartTestPeer(t)
	peer.Transport.ResetLocalSeq()
	peer.pendingRIUpd = []aurp.EventTuples{
		{{EventCode: aurp.EventCodeNA, Extended: true,
			RangeStart: 3910, RangeEnd: 3910, Distance: 1}},
	}
	if err := peer.sendNextRIUpd(true); err != nil {
		t.Fatal(err)
	}
	first, ok := peer.lastRISent.(*aurp.RIUpdPacket)
	if !ok {
		t.Fatalf("outstanding packet = %T, want RI-Upd", peer.lastRISent)
	}
	seq := first.Sequence

	beforeLog := len(peer.DumpChatLog())
	peer.lastSend.Store(time.Now().Add(-2 * peer.retryInterval()))
	if err := peer.stickerTasks(); err != nil {
		t.Fatal(err)
	}
	entries := peer.DumpChatLog()
	if len(entries) != beforeLog+1 {
		t.Fatalf("retry chat entries = %d, want %d", len(entries), beforeLog+1)
	}
	if entries[len(entries)-1].Packet != first {
		t.Fatal("RI-Upd retry did not resend the exact outstanding packet")
	}
	if peer.Transport.LocalSeq() != seq {
		t.Fatalf("RI-Upd retry advanced local sequence to %d, want %d",
			peer.Transport.LocalSeq(), seq)
	}
}

func TestSet28RouterDownRetryAndAckCloseSender(t *testing.T) {
	peer := newRestartTestPeer(t)
	if err := peer.startSenderShutdown(); err != nil {
		t.Fatal(err)
	}
	rd, ok := peer.lastRISent.(*aurp.RDPacket)
	if !ok {
		t.Fatalf("outstanding shutdown packet = %T, want RD", peer.lastRISent)
	}
	if rd.ErrorCode != aurp.ErrCodeNormalClose {
		t.Fatalf("RD code = %v, want normal close", rd.ErrorCode)
	}
	if got := peer.SenderState(); got != SenderWaitForRDAck {
		t.Fatalf("sender state = %v, want waiting for RD ACK", got)
	}
	seq := rd.Sequence

	beforeLog := len(peer.DumpChatLog())
	peer.lastSend.Store(time.Now().Add(-2 * peer.retryInterval()))
	if err := peer.stickerTasks(); err != nil {
		t.Fatal(err)
	}
	entries := peer.DumpChatLog()
	if len(entries) != beforeLog+1 {
		t.Fatalf("RD retry chat entries = %d, want %d", len(entries), beforeLog+1)
	}
	if entries[len(entries)-1].Packet != rd {
		t.Fatal("RD retry did not resend the exact outstanding packet")
	}
	if peer.Transport.LocalSeq() != seq {
		t.Fatalf("RD retry advanced sequence to %d, want %d",
			peer.Transport.LocalSeq(), seq)
	}

	ack := peer.Transport.NewRIAckPacket(
		peer.Transport.RemoteConnID(),
		seq,
		0,
	)
	if err := peer.handleRIAck(peer.logger, ack); err != nil {
		t.Fatal(err)
	}
	if got := peer.SenderState(); got != SenderUnconnected {
		t.Fatalf("sender state after RD ACK = %v, want unconnected", got)
	}
	if got := peer.Transport.RemoteConnID(); got != 0 {
		t.Fatalf("remote connection ID after RD ACK = %d, want 0", got)
	}
}
