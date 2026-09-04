package router

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
