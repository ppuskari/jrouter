package router

import (
	"fmt"
	"strings"

	"drjosh.dev/jrouter/atalk"
	"drjosh.dev/jrouter/atalk/nbp"
	"github.com/sfiera/multitalk/pkg/ddp"
)

func deviceHideDirectionMatches(ruleDirection, direction string) bool {
	ruleDirection = strings.ToLower(strings.TrimSpace(ruleDirection))
	return ruleDirection == "" ||
		ruleDirection == "both" ||
		ruleDirection == direction
}

func deviceHideFieldMatches(rule, value string) bool {
	rule = strings.TrimSpace(rule)
	return rule == "" ||
		rule == "*" ||
		strings.EqualFold(rule, value)
}

func (r AURPDeviceHideRule) hides(
	peer *AURPPeer,
	tuple nbp.Tuple,
	direction string,
) bool {
	return peerSelectorMatches(r.Peer, peer) &&
		deviceHideDirectionMatches(r.Direction, direction) &&
		deviceHideFieldMatches(r.Object, tuple.Object) &&
		deviceHideFieldMatches(r.Type, tuple.Type)
}

func setDDPDataSize(packet *ddp.ExtPacket, dataLen int) {
	hops := packet.Size & 0x3c00
	packet.Size = hops | (uint16(dataLen)+atalk.DDPExtHeaderSize)&0x03ff
}

func (p *AURPPeer) filterDeviceNBP(
	packet *ddp.ExtPacket,
	direction string,
) (*ddp.ExtPacket, bool, error) {
	if packet == nil ||
		packet.Proto != ddp.ProtoNBP ||
		len(p.timing.HiddenDevices) == 0 {
		return packet, false, nil
	}

	nbpPacket, err := nbp.Unmarshal(packet.Data)
	if err != nil {
		return nil, false, fmt.Errorf(
			"unmarshal NBP for device hiding: %w",
			err,
		)
	}
	if nbpPacket.Function != nbp.FunctionLkUpReply {
		return packet, false, nil
	}

	kept := make([]nbp.Tuple, 0, len(nbpPacket.Tuples))
	for _, tuple := range nbpPacket.Tuples {
		hidden := false
		for _, rule := range p.timing.HiddenDevices {
			if rule.hides(p, tuple, direction) {
				hidden = true
				break
			}
		}
		if !hidden {
			kept = append(kept, tuple)
		}
	}
	if len(kept) == len(nbpPacket.Tuples) {
		return packet, false, nil
	}
	if len(kept) == 0 {
		return nil, true, nil
	}

	nbpPacket.Tuples = kept
	data, err := nbpPacket.Marshal()
	if err != nil {
		return nil, false, fmt.Errorf(
			"marshal filtered NBP: %w",
			err,
		)
	}
	clone := *packet
	clone.Data = data
	clone.Cksum = 0
	setDDPDataSize(&clone, len(data))
	return &clone, false, nil
}
