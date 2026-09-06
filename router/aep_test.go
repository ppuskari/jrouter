package router

import (
	"testing"

	"drjosh.dev/jrouter/atalk/aep"
	"github.com/sfiera/multitalk/pkg/ddp"
)

func TestPrepareAEPReplyClearsStaleChecksum(t *testing.T) {
	pkt := &ddp.ExtPacket{
		ExtHeader: ddp.ExtHeader{
			Size:      21,
			Cksum:     0x4a31,
			DstNet:    1009,
			SrcNet:    1007,
			DstNode:   45,
			SrcNode:   132,
			DstSocket: 4,
			SrcSocket: 135,
			Proto:     ddp.ProtoAEP,
		},
		Data: []byte{byte(aep.EchoRequest), 0xaa, 0x55},
	}

	prepareAEPReply(pkt)

	if pkt.Cksum != 0 {
		t.Fatalf("AEP reply checksum = %#04x, want 0", pkt.Cksum)
	}
	if pkt.DstNet != 1007 || pkt.SrcNet != 1009 {
		t.Fatalf("AEP reply networks = %d <- %d, want 1007 <- 1009", pkt.DstNet, pkt.SrcNet)
	}
	if pkt.DstNode != 132 || pkt.SrcNode != 45 {
		t.Fatalf("AEP reply nodes = %d <- %d, want 132 <- 45", pkt.DstNode, pkt.SrcNode)
	}
	if pkt.DstSocket != 135 || pkt.SrcSocket != 4 {
		t.Fatalf("AEP reply sockets = %d <- %d, want 135 <- 4", pkt.DstSocket, pkt.SrcSocket)
	}
	if got := aep.Function(pkt.Data[0]); got != aep.EchoReply {
		t.Fatalf("AEP function = %d, want EchoReply", got)
	}
	if len(pkt.Data) != 3 || pkt.Data[1] != 0xaa || pkt.Data[2] != 0x55 {
		t.Fatalf("AEP payload was modified: %v", pkt.Data)
	}
}
