package router

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
