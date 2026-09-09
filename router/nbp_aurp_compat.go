package router

import (
	"log/slog"

	"drjosh.dev/jrouter/atalk/nbp"
	"github.com/sfiera/multitalk/pkg/ddp"
)

// normalizeAURPNBPFwdReqChecksum clears a non-zero DDP checksum on an NBP
// Forward Request before the packet is sent through AURP. AURP routers convert
// Forward Requests into local NBP lookups by changing the DDP/NBP packet. An
// older peer that preserves the original checksum during that conversion can
// therefore emit a stale checksum and cause otherwise valid NBP discovery to
// fail. A zero DDP checksum is valid and remains valid after the peer rewrites
// the packet.
//
// The input packet is never mutated. Packets that do not match this exact
// compatibility case are returned unchanged.
func normalizeAURPNBPFwdReqChecksum(
	ddpkt *ddp.ExtPacket,
	logger *slog.Logger,
) *ddp.ExtPacket {
	if ddpkt == nil ||
		ddpkt.Cksum == 0 ||
		ddpkt.Proto != ddp.ProtoNBP {
		return ddpkt
	}

	nbpkt, err := nbp.Unmarshal(ddpkt.Data)
	if err != nil || nbpkt.Function != nbp.FunctionFwdReq {
		return ddpkt
	}

	normalized := *ddpkt
	normalized.Cksum = 0

	if logger != nil {
		logger.Debug(
			"AURP: cleared DDP checksum on outbound NBP FwdReq",
			"dstnet", normalized.DstNet,
			"original-checksum", ddpkt.Cksum,
		)
	}

	return &normalized
}
