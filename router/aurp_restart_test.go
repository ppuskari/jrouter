package router

import (
	"context"
	"io"
	"log/slog"
	"net"
	"testing"
	"time"

	"drjosh.dev/jrouter/aurp"
)

func newRestartTestPeer(t *testing.T) *AURPPeer {
	t.Helper()

	udpConn, err := net.ListenUDP("udp4", &net.UDPAddr{
		IP:   net.IPv4(127, 0, 0, 1),
		Port: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { udpConn.Close() })

	localDI := aurp.IPDomainIdentifier(net.IPv4(192, 0, 2, 1))
	remoteDI := aurp.IPDomainIdentifier(net.IPv4(127, 0, 0, 1))
	peer := &AURPPeer{
		Transport:   aurp.NewTransport(localDI, remoteDI, 1000, 2000),
		UDPConn:     udpConn,
		ReceiveCh:   make(chan aurp.RoutingPacket, 16),
		RouteTable:  NewRouteTable(context.Background()),
		reconnectCh: make(chan struct{}, 1),
		logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	peer.setRemoteAddr(net.IPv4(127, 0, 0, 1))
	peer.setRState(ReceiverConnected)
	peer.setSState(SenderConnected)
	peer.RouteTable.AddObserver(peer)
	return peer
}

func newRestartOpenReq(connID uint16) *aurp.OpenReqPacket {
	remoteDI := aurp.IPDomainIdentifier(net.IPv4(127, 0, 0, 1))
	localDI := aurp.IPDomainIdentifier(net.IPv4(192, 0, 2, 1))
	return aurp.NewTransport(
		remoteDI,
		localDI,
		connID,
		0,
	).NewOpenReqPacket(nil)
}

func TestAURPRestartProbeAckKeepsOldSenderConnection(t *testing.T) {
	peer := newRestartTestPeer(t)
	oldConnID := peer.Transport.RemoteConnID()
	oldSeq := peer.Transport.LocalSeq()

	if err := peer.handleOpenReq(
		peer.logger,
		newRestartOpenReq(oldConnID+1),
	); err != nil {
		t.Fatal(err)
	}

	if !peer.restartProbe {
		t.Fatal("replacement Open-Req did not start restart probe")
	}
	if got := peer.SenderState(); got != SenderWaitForRIUpdAck {
		t.Fatalf("sender state = %v, want waiting for RI-Upd ack", got)
	}
	if got := peer.Transport.RemoteConnID(); got != oldConnID {
		t.Fatalf("remote conn ID changed to %d, want old %d", got, oldConnID)
	}
	if got := peer.Transport.LocalSeq(); got != aurp.Succ(oldSeq) {
		t.Fatalf("local seq = %d, want %d", got, aurp.Succ(oldSeq))
	}

	ack := peer.Transport.NewRIAckPacket(
		oldConnID,
		peer.Transport.LocalSeq(),
		0,
	)
	if err := peer.handleRIAck(peer.logger, ack); err != nil {
		t.Fatal(err)
	}

	if peer.restartProbe {
		t.Fatal("restart probe remained active after matching RI-Ack")
	}
	if got := peer.SenderState(); got != SenderConnected {
		t.Fatalf("sender state = %v, want connected", got)
	}
	if got := peer.Transport.RemoteConnID(); got != oldConnID {
		t.Fatalf("old connection was not retained: got %d want %d", got, oldConnID)
	}
}

func TestAURPRestartOpenReqDoesNotOverwriteOutstandingTransaction(t *testing.T) {
	peer := newRestartTestPeer(t)
	oldConnID := peer.Transport.RemoteConnID()

	peer.setSState(SenderWaitForRIUpdAck)
	if err := peer.handleOpenReq(
		peer.logger,
		newRestartOpenReq(oldConnID+1),
	); err != nil {
		t.Fatal(err)
	}
	if got := peer.Transport.RemoteConnID(); got != oldConnID {
		t.Fatalf("normal outstanding transaction overwrote conn ID: got %d want %d", got, oldConnID)
	}
	if peer.restartProbe {
		t.Fatal("restart probe started during a normal outstanding transaction")
	}

	peer.setSState(SenderConnected)
	if err := peer.handleOpenReq(
		peer.logger,
		newRestartOpenReq(oldConnID+1),
	); err != nil {
		t.Fatal(err)
	}
	if !peer.restartProbe {
		t.Fatal("restart probe did not start")
	}

	if err := peer.handleOpenReq(
		peer.logger,
		newRestartOpenReq(oldConnID+2),
	); err != nil {
		t.Fatal(err)
	}
	if got := peer.Transport.RemoteConnID(); got != oldConnID {
		t.Fatalf("competing Open-Req overwrote conn ID: got %d want %d", got, oldConnID)
	}
	if got := peer.SenderState(); got != SenderWaitForRIUpdAck {
		t.Fatalf("sender state = %v, want waiting for restart-probe ack", got)
	}
}

func TestAURPRestartProbeTimeoutLateAckAndReplacement(t *testing.T) {
	peer := newRestartTestPeer(t)
	oldConnID := peer.Transport.RemoteConnID()
	newConnID := oldConnID + 1

	if err := peer.handleOpenReq(
		peer.logger,
		newRestartOpenReq(newConnID),
	); err != nil {
		t.Fatal(err)
	}
	oldProbeSeq := peer.Transport.LocalSeq()

	peer.sendRetries.Store(sendRetryLimit)
	peer.lastSend.Store(time.Now().Add(-2 * sendRetryTimer))
	if err := peer.stickerTasks(); err != nil {
		t.Fatal(err)
	}

	if got := peer.SenderState(); got != SenderUnconnected {
		t.Fatalf("sender state after probe timeout = %v, want unconnected", got)
	}
	if got := peer.Transport.RemoteConnID(); got != 0 {
		t.Fatalf("remote conn ID after probe timeout = %d, want 0", got)
	}
	if peer.restartProbe {
		t.Fatal("restart probe remained active after timeout")
	}

	lateAck := peer.Transport.NewRIAckPacket(oldConnID, oldProbeSeq, 0)
	if err := peer.handleRIAck(peer.logger, lateAck); err != nil {
		t.Fatal(err)
	}
	if got := peer.SenderState(); got != SenderUnconnected {
		t.Fatalf("late old ack resurrected sender: state = %v", got)
	}
	if got := peer.Transport.RemoteConnID(); got != 0 {
		t.Fatalf("late old ack restored conn ID %d", got)
	}

	if err := peer.handleOpenReq(
		peer.logger,
		newRestartOpenReq(newConnID),
	); err != nil {
		t.Fatal(err)
	}
	if got := peer.SenderState(); got != SenderConnected {
		t.Fatalf("replacement Open-Req state = %v, want connected", got)
	}
	if got := peer.Transport.RemoteConnID(); got != newConnID {
		t.Fatalf("replacement conn ID = %d, want %d", got, newConnID)
	}

	mismatches := peer.ConnectionIDMismatches()
	oldPacket := peer.Transport.NewRIAckPacket(
		oldConnID,
		peer.Transport.LocalSeq(),
		0,
	)
	if err := peer.handleRIAck(peer.logger, oldPacket); err != nil {
		t.Fatal(err)
	}
	if got := peer.Transport.RemoteConnID(); got != newConnID {
		t.Fatalf("old packet changed replacement conn ID: got %d want %d", got, newConnID)
	}
	if got := peer.ConnectionIDMismatches(); got != mismatches+1 {
		t.Fatalf("old packet mismatch counter = %d, want %d", got, mismatches+1)
	}
}
