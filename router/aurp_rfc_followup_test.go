package router

import (
	"testing"
	"time"

	"drjosh.dev/jrouter/aurp"
)

func TestAURPLastHeardOnlyAdvancesForReceiverLiveness(t *testing.T) {
	peer := newRestartTestPeer(t)
	peer.lastHeardFrom.Store(time.Time{})

	// An RI-Ack belongs to our sender-side connection and must not refresh
	// receiver last-heard state.
	ack := peer.Transport.NewRIAckPacket(
		peer.Transport.RemoteConnID(),
		peer.Transport.LocalSeq(),
		0,
	)
	if err := peer.handleRIAck(peer.logger, ack); err != nil {
		t.Fatal(err)
	}
	if got := peer.LastHeardFrom(); !got.IsZero() {
		t.Fatalf("sender-side RI-Ack refreshed last-heard: %v", got)
	}

	peer.setRState(ReceiverWaitForTickleAck)
	tickleAck := &aurp.TickleAckPacket{Header: aurp.Header{
		TrHeader: aurp.TrHeader{
			ConnectionID: peer.Transport.LocalConnID(),
			Sequence:     0,
		},
		CommandCode: aurp.CmdCodeTickleAck,
	}}
	if err := peer.handleTickleAck(peer.logger, tickleAck); err != nil {
		t.Fatal(err)
	}
	if got := peer.LastHeardFrom(); got.IsZero() {
		t.Fatal("valid Tickle-Ack did not refresh last-heard")
	}
}

func TestAURPOpenReqIgnoresUnsupportedOptions(t *testing.T) {
	peer := newRestartTestPeer(t)
	peer.disconnectSender()

	req := newRestartOpenReq(3000)
	req.Options = aurp.Options{
		{Type: aurp.OptionTypeAuthentication, Data: []byte{1, 2, 3}},
		{Type: aurp.OptionType(0xfe), Data: []byte{4, 5}},
	}
	if err := peer.handleOpenReq(peer.logger, req); err != nil {
		t.Fatal(err)
	}
	if got := peer.SenderState(); got != SenderConnected {
		t.Fatalf("sender state = %v, want connected", got)
	}
	if got := peer.Transport.RemoteConnID(); got != 3000 {
		t.Fatalf("remote connection ID = %d, want 3000", got)
	}

	entries := peer.DumpChatLog()
	if len(entries) == 0 {
		t.Fatal("Open-Req produced no Open-Rsp")
	}
	rsp, ok := entries[len(entries)-1].Packet.(*aurp.OpenRspPacket)
	if !ok {
		t.Fatalf("last packet = %T, want *aurp.OpenRspPacket", entries[len(entries)-1].Packet)
	}
	if rsp.RateOrErrCode < 0 {
		t.Fatalf("Open-Rsp rejected unsupported options with %d", rsp.RateOrErrCode)
	}
	if len(rsp.Options) != 0 {
		t.Fatalf("unsupported options were echoed: %v", rsp.Options)
	}
}

func TestAURPSenderRDPacketUsesSequence(t *testing.T) {
	peer := newRestartTestPeer(t)
	peer.setRState(ReceiverConnected)
	wantSeq := peer.Transport.RemoteSeq()

	rd := &aurp.RDPacket{
		Header: aurp.Header{
			TrHeader: aurp.TrHeader{
				ConnectionID: peer.Transport.LocalConnID(),
				Sequence:     wantSeq,
			},
			CommandCode: aurp.CmdCodeRD,
		},
		ErrorCode: aurp.ErrCodeNormalClose,
	}
	if err := peer.handleRD(peer.logger, rd); err != nil {
		t.Fatal(err)
	}
	if got := peer.ReceiverState(); got != ReceiverUnconnected {
		t.Fatalf("receiver state after sender RD = %v, want unconnected", got)
	}
	if got := peer.Transport.RemoteSeq(); got != 1 {
		t.Fatalf("receiver sequence after disconnect = %d, want reset 1", got)
	}
}

func TestAURPReceiverRDPacketOnlyClosesSenderSide(t *testing.T) {
	peer := newRestartTestPeer(t)
	rd := &aurp.RDPacket{
		Header: aurp.Header{
			TrHeader: aurp.TrHeader{
				ConnectionID: peer.Transport.RemoteConnID(),
				Sequence:     0,
			},
			CommandCode: aurp.CmdCodeRD,
		},
		ErrorCode: aurp.ErrCodeNormalClose,
	}
	if err := peer.handleRD(peer.logger, rd); err != nil {
		t.Fatal(err)
	}
	if got := peer.SenderState(); got != SenderUnconnected {
		t.Fatalf("sender state after receiver RD = %v, want unconnected", got)
	}
	if got := peer.ReceiverState(); got != ReceiverConnected {
		t.Fatalf("receiver side was incorrectly closed: %v", got)
	}
}

func TestAURPRIUpdDuringRIRspSyncIsAckedButNotApplied(t *testing.T) {
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
	if got := peer.ReceiverState(); got != ReceiverWaitForRIRsp {
		t.Fatalf("receiver state = %v, want waiting for RI-Rsp", got)
	}
	if got := peer.Transport.RemoteSeq(); got != 2 {
		t.Fatalf("remote sequence = %d, want 2", got)
	}
	if route := peer.RouteTable.find(peer, 4242); !route.Zero() {
		t.Fatalf("early RI-Upd mutated incomplete routing baseline: %v", route)
	}
}

func TestAURPRIRspLateAndDuplicateHandlingIsIdempotent(t *testing.T) {
	peer := newRestartTestPeer(t)
	peer.setRState(ReceiverWaitForRIRsp)
	peer.Transport.ResetRemoteSeq()

	first := &aurp.RIRspPacket{
		Header: aurp.Header{
			TrHeader: aurp.TrHeader{
				ConnectionID: peer.Transport.LocalConnID(),
				Sequence:     1,
			},
			CommandCode: aurp.CmdCodeRIRsp,
			Flags:       aurp.RoutingFlagLast,
		},
		Networks: aurp.NetworkTuples{{
			Extended: true, RangeStart: 4242, RangeEnd: 4242, Distance: 1,
		}},
	}
	if err := peer.handleRIRsp(peer.logger, first); err != nil {
		t.Fatal(err)
	}
	if got := peer.ReceiverState(); got != ReceiverConnected {
		t.Fatalf("receiver state after RI-Rsp = %v, want connected", got)
	}
	if got := peer.Transport.RemoteSeq(); got != 2 {
		t.Fatalf("remote sequence after RI-Rsp = %d, want 2", got)
	}

	// A valid refresh can arrive after the state has returned to connected.
	late := &aurp.RIRspPacket{
		Header: aurp.Header{
			TrHeader: aurp.TrHeader{
				ConnectionID: peer.Transport.LocalConnID(),
				Sequence:     2,
			},
			CommandCode: aurp.CmdCodeRIRsp,
		},
		Networks: aurp.NetworkTuples{{
			Extended: true, RangeStart: 4243, RangeEnd: 4243, Distance: 2,
		}},
	}
	if err := peer.handleRIRsp(peer.logger, late); err != nil {
		t.Fatal(err)
	}
	if got := peer.Transport.RemoteSeq(); got != 3 {
		t.Fatalf("remote sequence after late RI-Rsp = %d, want 3", got)
	}

	// Replaying the same packet must only re-ACK and drop; it must not
	// refresh sequence state or create another route candidate.
	if err := peer.handleRIRsp(peer.logger, late); err != nil {
		t.Fatal(err)
	}
	if got := peer.Transport.RemoteSeq(); got != 3 {
		t.Fatalf("remote sequence after duplicate RI-Rsp = %d, want 3", got)
	}
	if route := peer.RouteTable.find(peer, 4243); route.Zero() || route.Distance != 3 {
		t.Fatalf("late RI-Rsp route after duplicate = %v, want distance 3", route)
	}
}

func TestAURPSUIFlagsFilterIncrementalEvents(t *testing.T) {
	local := fakeTarget{key: "local", class: TargetClassAppleTalkPeer}
	before := Route{}
	after := testAURPRoute(local, 5000, 2)

	peer := &AURPPeer{}
	peer.setSState(SenderConnected)
	peer.setSUIFlags(aurp.RoutingFlagSUINDC)
	peer.queueBestNetworkTransition(before, after)
	if got := peer.takePendingEvents(); len(got) != 0 {
		t.Fatalf("NA escaped NDC-only SUI subscription: %v", got)
	}

	peer.setSUIFlags(aurp.RoutingFlagSUINA)
	peer.queueBestNetworkTransition(before, after)
	got := peer.takePendingEvents()
	if len(got) != 1 || got[0].EventCode != aurp.EventCodeNA {
		t.Fatalf("NA not emitted for NA SUI subscription: %v", got)
	}
}
