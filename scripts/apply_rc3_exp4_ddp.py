from pathlib import Path

port_path = Path("router/etalk_port.go")
text = port_path.read_text(encoding="utf-8")
old = "\treturn port.pcapHandle.WritePacketData(outFrameRaw)\n}\n\ntype outbox struct {"
new = """\t// Preserve the proven physical-Ethernet transmit path first. The\n\t// Windows receive-path mirror is additive and is never allowed to make\n\t// a successful physical transmit fail.\n\tif err := port.pcapHandle.WritePacketData(outFrameRaw); err != nil {\n\t\treturn err\n\t}\n\tif shouldMirrorDDPToReceivePath(pkt) {\n\t\tif err := mirrorDDPTransmit(port, outFrameRaw); err != nil {\n\t\t\tport.logger.Warn(\n\t\t\t\t\"EtherTalk: experimental Windows SendToRx DDP mirror failed\",\n\t\t\t\t\"error\", err,\n\t\t\t)\n\t\t}\n\t}\n\treturn nil\n}\n\ntype outbox struct {"""
if old not in text:
    raise SystemExit("etalk_port.go send tail did not match expected exp3 source")
port_path.write_text(text.replace(old, new, 1), encoding="utf-8")

Path("router/ddp_sendtorx_scope.go").write_text(r'''package router

import "github.com/sfiera/multitalk/pkg/ddp"

// shouldMirrorDDPToReceivePath keeps the rc3-exp4 experiment additive without
// double-injecting packets that rc3-exp2 already mirrors explicitly.
func shouldMirrorDDPToReceivePath(pkt *ddp.ExtPacket) bool {
	if pkt == nil {
		return false
	}

	// Periodic RTMP broadcasts already have the rc3-exp1/exp2 receive mirror.
	if pkt.DstNet == 0 &&
		pkt.DstNode == 0xff &&
		pkt.DstSocket == 1 &&
		pkt.Proto == ddp.ProtoRTMPResp {
		return false
	}

	// ZIP replies are already mirrored by sendZIPWithReceiveMirror. Keep ZIP
	// query traffic on the normal physical path during this experiment.
	if pkt.DstSocket == 6 &&
		(pkt.Proto == ddp.ProtoZIP || pkt.Proto == ddp.ProtoATP) {
		return false
	}

	return true
}
''', encoding="utf-8")

Path("router/ddp_sendtorx_other.go").write_text(r'''//go:build !windows

package router

func mirrorDDPTransmit(_ *EtherTalkPort, _ []byte) error {
	return nil
}
''', encoding="utf-8")

Path("router/ddp_sendtorx_windows.go").write_text(r'''//go:build windows

package router

import (
	"fmt"
	"sync"
)

var ddpMirrorSlots sync.Map

type ddpMirrorSlot struct {
	once   sync.Once
	mirror *npcapRTMPMirror
}

// mirrorDDPTransmit injects one already-marshalled EtherTalk DDP frame into
// Npcap's receive path. The normal pcap transmit has already completed before
// this function is called.
func mirrorDDPTransmit(port *EtherTalkPort, raw []byte) error {
	v, _ := ddpMirrorSlots.LoadOrStore(
		port,
		&ddpMirrorSlot{},
	)
	slot := v.(*ddpMirrorSlot)

	slot.once.Do(func() {
		mirror, err := openNpcapRTMPMirror(port.device)
		if err != nil {
			port.logger.Warn(
				"EtherTalk: experimental Windows DDP SendToRx mirror unavailable",
				"error", err,
			)
			return
		}
		slot.mirror = mirror
		port.logger.Warn(
			"EtherTalk: EXPERIMENTAL Windows Npcap DDP SendToRx mirror enabled",
			"device", port.device,
			"mode", fmt.Sprintf("0x%04x", npcapModeSendToRx),
		)
	})

	if slot.mirror == nil {
		return nil
	}
	return slot.mirror.send(raw)
}
''', encoding="utf-8")

Path("router/ddp_sendtorx_scope_test.go").write_text(r'''package router

import (
	"testing"

	"github.com/sfiera/multitalk/pkg/ddp"
)

func TestReceiveMirrorScope(t *testing.T) {
	tests := []struct {
		name string
		pkt  *ddp.ExtPacket
		want bool
	}{
		{
			name: "AEP unicast is generically mirrored",
			pkt: &ddp.ExtPacket{ExtHeader: ddp.ExtHeader{
				DstNet: 1007, DstNode: 132, DstSocket: 4,
			}},
			want: true,
		},
		{
			name: "NBP is generically mirrored",
			pkt: &ddp.ExtPacket{ExtHeader: ddp.ExtHeader{
				DstNet: 1007, DstNode: 132, DstSocket: 2,
				Proto: ddp.ProtoNBP,
			}},
			want: true,
		},
		{
			name: "periodic RTMP broadcast stays on existing mirror",
			pkt: &ddp.ExtPacket{ExtHeader: ddp.ExtHeader{
				DstNet: 0, DstNode: 0xff, DstSocket: 1,
				Proto: ddp.ProtoRTMPResp,
			}},
			want: false,
		},
		{
			name: "ZIP reply stays on existing mirror",
			pkt: &ddp.ExtPacket{ExtHeader: ddp.ExtHeader{
				DstNet: 1007, DstNode: 132, DstSocket: 6,
				Proto: ddp.ProtoZIP,
			}},
			want: false,
		},
		{
			name: "ZIP ATP reply stays on existing mirror",
			pkt: &ddp.ExtPacket{ExtHeader: ddp.ExtHeader{
				DstNet: 1007, DstNode: 132, DstSocket: 6,
				Proto: ddp.ProtoATP,
			}},
			want: false,
		},
		{name: "nil packet", pkt: nil, want: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := shouldMirrorDDPToReceivePath(tc.pkt); got != tc.want {
				t.Fatalf("shouldMirrorDDPToReceivePath() = %v, want %v", got, tc.want)
			}
		})
	}
}
''', encoding="utf-8")
