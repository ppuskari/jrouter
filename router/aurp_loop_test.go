package router

import (
	"io"
	"log/slog"
	"testing"
	"time"

	"drjosh.dev/jrouter/atalk/rtmp"
	"github.com/sfiera/multitalk/pkg/ddp"
)

func TestSet27LoopProbeRFCMinimums(t *testing.T) {
	if loopProbeAttempts < 4 {
		t.Fatalf("Loop Probe attempts = %d, RFC 1504 requires at least 4", loopProbeAttempts)
	}
	if loopProbeInterval < 2*time.Second {
		t.Fatalf("Loop Probe interval = %v, RFC 1504 requires at least 2s", loopProbeInterval)
	}
}

func TestSet27LoopProbePacketMapsRangeAndCarriesToken(t *testing.T) {
	local := Route{
		RouteKey: RouteKey{NetStart: 1000},
		NetEnd:   1009,
	}
	remote := Route{
		RouteKey: RouteKey{NetStart: 5000},
		NetEnd:   5009,
	}
	token := []byte("set27-loop-token")
	localAddr := ddp.Addr{Network: 1004, Node: 77}

	pkt, err := buildLoopProbePacket(localAddr, local, remote, token)
	if err != nil {
		t.Fatal(err)
	}
	if pkt.DstNet != 5004 || pkt.DstNode != 77 || pkt.DstSocket != 1 {
		t.Fatalf("probe destination = %d.%d.%d, want 5004.77.1",
			pkt.DstNet, pkt.DstNode, pkt.DstSocket)
	}
	if pkt.SrcNet != 1004 || pkt.SrcNode != 77 || pkt.SrcSocket != 1 {
		t.Fatalf("probe source = %d.%d.%d, want 1004.77.1",
			pkt.SrcNet, pkt.SrcNode, pkt.SrcSocket)
	}

	req, err := rtmp.UnmarshalRequestPacket(pkt.Data)
	if err != nil {
		t.Fatal(err)
	}
	if req.Function != rtmp.FunctionLoopProbe {
		t.Fatalf("RTMP function = %v, want Loop Probe", req.Function)
	}
	if string(req.Data) != string(token) {
		t.Fatalf("Loop Probe recognition data = %x, want %x", req.Data, token)
	}
}

func TestSet27LoopProbeOnlyConfirmsOnExpectedLocalPort(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	router := &Router{
		Logger:         logger,
		loopProbes:     make(map[string]*loopProbeInvestigation),
		loopProbeByKey: make(map[string]string),
	}
	peer := &AURPPeer{
		tunnelID:       "cfg:loop.example",
		loopDetectedCh: make(chan struct{}, 1),
	}
	expectedPort := &EtherTalkPort{device: "expected"}
	wrongPort := &EtherTalkPort{device: "wrong"}
	token := []byte("recognition-data")
	investigation := &loopProbeInvestigation{
		key:   "cfg:loop.example|5000",
		token: token,
		peer:  peer,
		port:  expectedPort,
	}
	tokenKey := string(token)
	router.loopProbes[tokenKey] = investigation
	router.loopProbeByKey[investigation.key] = tokenKey
	pkt := &ddp.ExtPacket{
		ExtHeader: ddp.ExtHeader{
			SrcNet:  5004,
			SrcNode: 77,
		},
	}

	if router.handleLoopProbeReturn(wrongPort, pkt, token) {
		t.Fatal("Loop Probe returning on the wrong local port confirmed a loop")
	}
	if peer.ConfirmedRoutingLoops() != 0 {
		t.Fatalf("confirmed loops after wrong-port return = %d, want 0",
			peer.ConfirmedRoutingLoops())
	}
	if router.loopProbes[tokenKey] == nil {
		t.Fatal("wrong-port return retired the active investigation")
	}

	if !router.handleLoopProbeReturn(expectedPort, pkt, token) {
		t.Fatal("Loop Probe returning on the expected local port did not confirm")
	}
	if peer.ConfirmedRoutingLoops() != 1 {
		t.Fatalf("confirmed loops = %d, want 1", peer.ConfirmedRoutingLoops())
	}
	if router.loopProbes[tokenKey] != nil {
		t.Fatal("confirmed Loop Probe was not retired")
	}
	select {
	case <-peer.loopDetectedCh:
	default:
		t.Fatal("confirmed Loop Probe did not signal peer shutdown")
	}
}
