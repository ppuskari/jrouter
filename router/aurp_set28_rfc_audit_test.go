package router

import (
	"net"
	"testing"
	"time"

	"drjosh.dev/jrouter/aurp"
	"github.com/sfiera/multitalk/pkg/ddp"
)

func TestSet28OpenRspEnvironmentFlagsReflectActivePolicies(t *testing.T) {
	tests := []struct {
		name string
		peer *AURPPeer
		want aurp.RoutingFlag
	}{
		{name: "none", peer: &AURPPeer{tunnelID: "cfg:none.example"}},
		{
			name: "matching remap",
			peer: &AURPPeer{
				tunnelID:       "cfg:remap.example",
				ConfiguredAddr: "remap.example",
				timing: AURPConfig{RemapRules: []AURPRemapRule{{
					Peer: "cfg:remap.example", RemoteStart: 100, RemoteEnd: 109,
					LocalStart: 5000, LocalEnd: 5009,
				}}},
			},
			want: aurp.RoutingFlagRemappingActive,
		},
		{
			name: "nonmatching remap",
			peer: &AURPPeer{
				tunnelID: "cfg:other.example",
				timing: AURPConfig{RemapRules: []AURPRemapRule{{
					Peer: "cfg:remap.example", RemoteStart: 100, RemoteEnd: 109,
					LocalStart: 5000, LocalEnd: 5009,
				}}},
			},
		},
		{
			name: "hcr",
			peer: &AURPPeer{tunnelID: "cfg:hcr.example",
				timing: AURPConfig{HopCountReduction: true}},
			want: aurp.RoutingFlagHopCountReduction,
		},
		{
			name: "both",
			peer: &AURPPeer{
				tunnelID: "cfg:both.example", ConfiguredAddr: "both.example",
				timing: AURPConfig{
					HopCountReduction: true,
					RemapRules: []AURPRemapRule{{
						Peer: "both.example", RemoteStart: 200, RemoteEnd: 209,
						LocalStart: 6000, LocalEnd: 6009,
					}},
				},
			},
			want: aurp.RoutingFlagRemappingActive |
				aurp.RoutingFlagHopCountReduction,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.peer.openResponseEnvironmentFlags(); got != tc.want {
				t.Fatalf("environment flags = 0x%04x, want 0x%04x", got, tc.want)
			}
		})
	}
}

func TestSet28OpenRspRateTracksConfiguredUpdateInterval(t *testing.T) {
	peer := &AURPPeer{timing: AURPConfig{UpdateInterval: 30 * time.Second}}
	if got := peer.openResponseRate(); got != 3 {
		t.Fatalf("Open-Rsp nominal rate = %d, want 3 ten-second units", got)
	}
	peer.timing.UpdateInterval = 0
	if got := peer.openResponseRate(); got != 1 {
		t.Fatalf("default Open-Rsp nominal rate = %d, want 1", got)
	}
}

func TestSet28RIReqDoesNotOverwriteOutstandingSequencedPacket(t *testing.T) {
	peer := newRestartTestPeer(t)
	peer.setSUIFlags(aurp.RoutingFlagSUINA)
	peer.Transport.IncLocalSeq()
	outstanding := peer.Transport.NewRIUpdPacket(aurp.EventTuples{{
		EventCode: aurp.EventCodeNull,
	}})
	peer.lastRISent = outstanding
	peer.pendingRIUpd = []aurp.EventTuples{{
		{EventCode: aurp.EventCodeNull},
	}}
	peer.setSState(SenderWaitForRIUpdAck)

	beforeSeq := peer.Transport.LocalSeq()
	beforeFlags := aurp.RoutingFlag(peer.suiFlags.Load())
	beforePending := len(peer.pendingRIUpd)
	req := &aurp.RIReqPacket{Header: aurp.Header{
		TrHeader: aurp.TrHeader{
			ConnectionID: peer.Transport.RemoteConnID(),
			Sequence:     0,
		},
		CommandCode: aurp.CmdCodeRIReq,
		Flags:       aurp.RoutingFlagAllSUI,
	}}
	if err := peer.handleRIReq(peer.logger, req); err != nil {
		t.Fatal(err)
	}
	if got := peer.Transport.LocalSeq(); got != beforeSeq {
		t.Fatalf("RI-Req reset outstanding sequence: got %d want %d", got, beforeSeq)
	}
	if peer.lastRISent != outstanding {
		t.Fatal("RI-Req overwrote the outstanding sequenced packet")
	}
	if got := len(peer.pendingRIUpd); got != beforePending {
		t.Fatalf("RI-Req replaced pending RI-Upd chunks: got %d want %d", got, beforePending)
	}
	if got := aurp.RoutingFlag(peer.suiFlags.Load()); got != beforeFlags {
		t.Fatalf("deferred RI-Req changed SUI flags: got 0x%04x want 0x%04x", got, beforeFlags)
	}
	if got := peer.SenderState(); got != SenderWaitForRIUpdAck {
		t.Fatalf("sender state = %v, want waiting for RI-Upd ACK", got)
	}
}

func TestSet28TickleBeforeDataThreshold(t *testing.T) {
	now := time.Now()
	peer := &AURPPeer{timing: AURPConfig{LastHeardFromTimeout: 5 * time.Minute}}
	peer.setRState(ReceiverConnected)
	peer.lastHeardFrom.Store(now.Add(-3 * time.Minute))
	if !peer.needsTickleBeforeData(now) {
		t.Fatal("stale >2-minute receiver with long LHFT did not require pre-data Tickle")
	}
	peer.timing.LastHeardFromTimeout = 90 * time.Second
	if peer.needsTickleBeforeData(now) {
		t.Fatal("default 90-second LHFT incorrectly enabled pre-data Tickle")
	}
	peer.timing.LastHeardFromTimeout = 5 * time.Minute
	peer.lastHeardFrom.Store(now.Add(-time.Minute))
	if peer.needsTickleBeforeData(now) {
		t.Fatal("recent receiver activity incorrectly required pre-data Tickle")
	}
	peer.lastHeardFrom.Store(now.Add(-3 * time.Minute))
	peer.setRState(ReceiverWaitForTickleAck)
	if peer.needsTickleBeforeData(now) {
		t.Fatal("already outstanding Tickle requested another pre-data Tickle")
	}
}

func TestSet28ForwardSendsTickleBeforeAppleTalkData(t *testing.T) {
	sink, err := net.ListenUDP("udp4", &net.UDPAddr{
		IP: net.IPv4(127, 0, 0, 2), Port: 387,
	})
	if err != nil {
		t.Skipf("cannot reserve loopback AURP port for ordering test: %v", err)
	}
	defer sink.Close()

	peer := newRestartTestPeer(t)
	peer.setRemoteAddr(net.IPv4(127, 0, 0, 2))
	peer.timing.LastHeardFromTimeout = 5 * time.Minute
	peer.lastHeardFrom.Store(time.Now().Add(-3 * time.Minute))
	peer.setRState(ReceiverConnected)

	packet := &ddp.ExtPacket{
		ExtHeader: ddp.ExtHeader{
			Size: 14, SrcNet: 100, DstNet: 200, SrcNode: 1, DstNode: 2,
			SrcSocket: 4, DstSocket: 4, Proto: ddp.ProtoAEP,
		},
		Data: []byte{1},
	}
	if err := peer.Forward(t.Context(), packet); err != nil {
		t.Fatal(err)
	}
	if got := peer.ReceiverState(); got != ReceiverWaitForTickleAck {
		t.Fatalf("receiver state = %v, want waiting for Tickle-Ack", got)
	}
	if err := sink.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	for i, wantTickle := range []bool{true, false} {
		buf := make([]byte, 2048)
		n, _, err := sink.ReadFromUDP(buf)
		if err != nil {
			t.Fatalf("reading datagram %d: %v", i, err)
		}
		_, got, err := aurp.ParsePacket(buf[:n])
		if err != nil {
			t.Fatalf("parsing datagram %d: %v", i, err)
		}
		_, isTickle := got.(*aurp.TicklePacket)
		_, isAppleTalk := got.(*aurp.AppleTalkPacket)
		if wantTickle && !isTickle {
			t.Fatalf("first datagram type = %T, want Tickle", got)
		}
		if !wantTickle && !isAppleTalk {
			t.Fatalf("second datagram type = %T, want AppleTalk data", got)
		}
	}
}
