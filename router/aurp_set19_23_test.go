package router

import (
	"testing"
	"time"

	"drjosh.dev/jrouter/aurp"
)

func testTickleAck(peer *AURPPeer) *aurp.TickleAckPacket {
	return &aurp.TickleAckPacket{Header: aurp.Header{
		TrHeader: aurp.TrHeader{
			ConnectionID: peer.Transport.LocalConnID(),
			Sequence:     0,
		},
		CommandCode: aurp.CmdCodeTickleAck,
	}}
}

func TestSet20LateTickleAckCannotBypassRIRspSync(t *testing.T) {
	peer := newRestartTestPeer(t)
	peer.setRState(ReceiverWaitForRIRsp)
	peer.lastHeardFrom.Store(time.Time{})

	if err := peer.handleTickleAck(peer.logger, testTickleAck(peer)); err != nil {
		t.Fatal(err)
	}
	if got := peer.ReceiverState(); got != ReceiverWaitForRIRsp {
		t.Fatalf("late Tickle-Ack changed receiver state to %v", got)
	}
	if got := peer.LastHeardFrom(); !got.IsZero() {
		t.Fatalf("stray Tickle-Ack refreshed last-heard during RI sync: %v", got)
	}
	if got := peer.LateTickleAcks(); got != 1 {
		t.Fatalf("late Tickle-Ack counter = %d, want 1", got)
	}
}

func TestSet20LateTickleAckOnConnectedPeerIsHarmlessLiveness(t *testing.T) {
	peer := newRestartTestPeer(t)
	peer.setRState(ReceiverConnected)
	peer.lastHeardFrom.Store(time.Time{})

	if err := peer.handleTickleAck(peer.logger, testTickleAck(peer)); err != nil {
		t.Fatal(err)
	}
	if got := peer.ReceiverState(); got != ReceiverConnected {
		t.Fatalf("late Tickle-Ack changed connected state to %v", got)
	}
	if got := peer.LastHeardFrom(); got.IsZero() {
		t.Fatal("late Tickle-Ack on connected peer did not prove liveness")
	}
	if got := peer.LateTickleAcks(); got != 1 {
		t.Fatalf("late Tickle-Ack counter = %d, want 1", got)
	}
}

func TestSet20ExpectedTickleAckResetsRetryState(t *testing.T) {
	peer := newRestartTestPeer(t)
	peer.setRState(ReceiverWaitForTickleAck)
	peer.sendRetries.Store(4)

	if err := peer.handleTickleAck(peer.logger, testTickleAck(peer)); err != nil {
		t.Fatal(err)
	}
	if got := peer.ReceiverState(); got != ReceiverConnected {
		t.Fatalf("expected Tickle-Ack state = %v, want connected", got)
	}
	if got := peer.SendRetries(); got != 0 {
		t.Fatalf("send retries = %d, want 0", got)
	}
	if got := peer.LateTickleAcks(); got != 0 {
		t.Fatalf("expected Tickle-Ack counted as late: %d", got)
	}
}

func TestSet21DuplicateSenderRDReAckHasNoSZIFlag(t *testing.T) {
	peer := newRestartTestPeer(t)
	peer.Transport.IncRemoteSeq() // expect sequence 2

	rd := &aurp.RDPacket{
		Header: aurp.Header{
			TrHeader: aurp.TrHeader{
				ConnectionID: peer.Transport.LocalConnID(),
				Sequence:     1,
			},
			CommandCode: aurp.CmdCodeRD,
		},
		ErrorCode: aurp.ErrCodeNormalClose,
	}
	if err := peer.handleRD(peer.logger, rd); err != nil {
		t.Fatal(err)
	}
	if got := peer.ReceiverState(); got != ReceiverConnected {
		t.Fatalf("duplicate RD closed receiver: %v", got)
	}
	if got := peer.ReacksSent(); got != 1 {
		t.Fatalf("duplicate RD re-ACK count = %d, want 1", got)
	}

	log := peer.DumpChatLog()
	if len(log) == 0 {
		t.Fatal("no chat log entries")
	}
	ack, ok := log[len(log)-1].Packet.(*aurp.RIAckPacket)
	if !ok {
		t.Fatalf("last packet = %T, want RI-Ack", log[len(log)-1].Packet)
	}
	if ack.Flags != 0 {
		t.Fatalf("duplicate RD RI-Ack flags = %v, want 0", ack.Flags)
	}
}

func TestSet22EarlyRIUpdRecoveryCounters(t *testing.T) {
	peer := newRestartTestPeer(t)
	peer.setRState(ReceiverWaitForRIRsp)
	peer.Transport.ResetRemoteSeq()

	upd := &aurp.RIUpdPacket{
		Header: aurp.Header{
			TrHeader: aurp.TrHeader{
				ConnectionID: peer.Transport.LocalConnID(),
				Sequence:     1,
			},
			CommandCode: aurp.CmdCodeRIUpd,
		},
		Events: aurp.EventTuples{{
			EventCode:  aurp.EventCodeNA,
			Extended:   true,
			RangeStart: 4242,
			RangeEnd:   4242,
			Distance:   1,
		}},
	}
	if err := peer.handleRIUpd(peer.logger, upd); err != nil {
		t.Fatal(err)
	}
	if got := peer.EarlyRIUpdates(); got != 1 {
		t.Fatalf("early RI-Upd count = %d, want 1", got)
	}
	if got := peer.EarlyRIUpdateAcks(); got != 1 {
		t.Fatalf("early RI-Upd ACK count = %d, want 1", got)
	}
	if route := peer.RouteTable.find(peer, 4242); !route.Zero() {
		t.Fatalf("early RI-Upd mutated partial baseline: %v", route)
	}
}

func TestSet23ZoneTelemetryTracksOwnershipAndExtendedCompletion(t *testing.T) {
	peer := newRestartTestPeer(t)
	if _, err := peer.RouteTable.UpsertRoute(peer, true, 5000, 5000, 2); err != nil {
		t.Fatal(err)
	}

	nonext := &aurp.ZIRspPacket{
		Header: aurp.Header{
			TrHeader: aurp.TrHeader{
				ConnectionID: peer.Transport.LocalConnID(),
				Sequence:     0,
			},
			CommandCode: aurp.CmdCodeZoneRsp,
		},
		Subcode: aurp.SubcodeZoneInfoNonExt,
		Zones: aurp.ZoneTuples{
			{Network: 5000, Name: "Owned"},
			{Network: 6000, Name: "Foreign"},
		},
	}
	if err := peer.handleZIRsp(peer.logger, nonext); err != nil {
		t.Fatal(err)
	}
	if got := peer.ZoneTuplesAccepted(); got != 1 {
		t.Fatalf("accepted zone tuples = %d, want 1", got)
	}
	if got := peer.ZoneTuplesIgnored(); got != 1 {
		t.Fatalf("ignored zone tuples = %d, want 1", got)
	}

	part1 := &aurp.ZIRspPacket{
		Header: nonext.Header,
		Subcode:     aurp.SubcodeZoneInfoExt,
		TotalTuples: 2,
		Zones: aurp.ZoneTuples{
			{Network: 5000, Name: "Zone A"},
		},
	}
	part2 := &aurp.ZIRspPacket{
		Header: nonext.Header,
		Subcode:     aurp.SubcodeZoneInfoExt,
		TotalTuples: 2,
		Zones: aurp.ZoneTuples{
			{Network: 5000, Name: "Zone B"},
		},
	}
	if err := peer.handleZIRsp(peer.logger, part1); err != nil {
		t.Fatal(err)
	}
	if err := peer.handleZIRsp(peer.logger, part2); err != nil {
		t.Fatal(err)
	}
	if got := peer.ExtendedZIFragments(); got != 2 {
		t.Fatalf("extended ZI fragments = %d, want 2", got)
	}
	if got := peer.ExtendedZICompleted(); got != 1 {
		t.Fatalf("extended ZI completed = %d, want 1", got)
	}
}
