package router

import (
	"bytes"
	"testing"

	"drjosh.dev/jrouter/atalk/nbp"
	"github.com/sfiera/multitalk/pkg/ddp"
)

func makeNBPCompatPacketData(
	t *testing.T,
	function nbp.Function,
) []byte {
	t.Helper()

	pkt := &nbp.Packet{
		Function: function,
		NBPID:    1,
		Tuples: []nbp.Tuple{
			{
				Network:    1004,
				Node:       82,
				Socket:     223,
				Enumerator: 0,
				Object:     "=",
				Type:       "AFPServer",
				Zone:       "btr",
			},
		},
	}

	data, err := pkt.Marshal()
	if err != nil {
		t.Fatalf("NBP marshal failed: %v", err)
	}
	return data
}

func TestNormalizeAURPNBPFwdReqChecksumFieldPacket(t *testing.T) {
	data := makeNBPCompatPacketData(t, nbp.FunctionFwdReq)
	pkt := &ddp.ExtPacket{
		ExtHeader: ddp.ExtHeader{
			Cksum:     0x44e9,
			DstNet:    2137,
			SrcNet:    1004,
			DstNode:   0,
			SrcNode:   82,
			DstSocket: 2,
			SrcSocket: 2,
			Proto:     ddp.ProtoNBP,
		},
		Data: data,
	}

	out := normalizeAURPNBPFwdReqChecksum(pkt, nil)

	if out == pkt {
		t.Fatal("normalization mutated/reused the input packet; want a clone")
	}
	if out.Cksum != 0 {
		t.Fatalf("normalized checksum = %#04x, want 0", out.Cksum)
	}
	if pkt.Cksum != 0x44e9 {
		t.Fatalf("input checksum changed to %#04x, want %#04x", pkt.Cksum, 0x44e9)
	}
	if out.DstNet != pkt.DstNet ||
		out.SrcNet != pkt.SrcNet ||
		out.DstNode != pkt.DstNode ||
		out.SrcNode != pkt.SrcNode ||
		out.DstSocket != pkt.DstSocket ||
		out.SrcSocket != pkt.SrcSocket ||
		out.Proto != pkt.Proto {
		t.Fatal("normalization modified DDP addressing or protocol fields")
	}
	if !bytes.Equal(out.Data, pkt.Data) {
		t.Fatal("normalization modified NBP payload")
	}

	nbpkt, err := nbp.Unmarshal(out.Data)
	if err != nil {
		t.Fatalf("normalized NBP payload no longer parses: %v", err)
	}
	if nbpkt.Function != nbp.FunctionFwdReq {
		t.Fatalf("NBP function = %v, want FwdReq", nbpkt.Function)
	}
	if len(nbpkt.Tuples) != 1 {
		t.Fatalf("NBP tuple count = %d, want 1", len(nbpkt.Tuples))
	}
	tuple := nbpkt.Tuples[0]
	if tuple.Network != 1004 ||
		tuple.Node != 82 ||
		tuple.Socket != 223 ||
		tuple.Object != "=" ||
		tuple.Type != "AFPServer" ||
		tuple.Zone != "btr" {
		t.Fatalf("NBP requester/query tuple changed: %+v", tuple)
	}
}

func TestNormalizeAURPNBPFwdReqChecksumLeavesZeroChecksum(t *testing.T) {
	pkt := &ddp.ExtPacket{
		ExtHeader: ddp.ExtHeader{
			Cksum: 0,
			Proto: ddp.ProtoNBP,
		},
		Data: makeNBPCompatPacketData(t, nbp.FunctionFwdReq),
	}

	out := normalizeAURPNBPFwdReqChecksum(pkt, nil)
	if out != pkt {
		t.Fatal("zero-checksum FwdReq should be returned unchanged")
	}
}

func TestNormalizeAURPNBPFwdReqChecksumLeavesLookup(t *testing.T) {
	pkt := &ddp.ExtPacket{
		ExtHeader: ddp.ExtHeader{
			Cksum: 0x1234,
			Proto: ddp.ProtoNBP,
		},
		Data: makeNBPCompatPacketData(t, nbp.FunctionLkUp),
	}

	out := normalizeAURPNBPFwdReqChecksum(pkt, nil)
	if out != pkt {
		t.Fatal("ordinary NBP LkUp should be returned unchanged")
	}
	if out.Cksum != 0x1234 {
		t.Fatalf("LkUp checksum = %#04x, want %#04x", out.Cksum, 0x1234)
	}
}

func TestNormalizeAURPNBPFwdReqChecksumLeavesNonNBP(t *testing.T) {
	pkt := &ddp.ExtPacket{
		ExtHeader: ddp.ExtHeader{
			Cksum: 0x5678,
			Proto: ddp.ProtoAEP,
		},
		Data: []byte{1, 2, 3},
	}

	out := normalizeAURPNBPFwdReqChecksum(pkt, nil)
	if out != pkt {
		t.Fatal("non-NBP DDP packet should be returned unchanged")
	}
	if out.Cksum != 0x5678 {
		t.Fatalf("non-NBP checksum = %#04x, want %#04x", out.Cksum, 0x5678)
	}
}

func TestNormalizeAURPNBPFwdReqChecksumLeavesMalformedNBP(t *testing.T) {
	pkt := &ddp.ExtPacket{
		ExtHeader: ddp.ExtHeader{
			Cksum: 0x9abc,
			Proto: ddp.ProtoNBP,
		},
		Data: []byte{0x41},
	}

	out := normalizeAURPNBPFwdReqChecksum(pkt, nil)
	if out != pkt {
		t.Fatal("malformed NBP should be returned unchanged")
	}
	if out.Cksum != 0x9abc {
		t.Fatalf("malformed NBP checksum = %#04x, want %#04x", out.Cksum, 0x9abc)
	}
}
