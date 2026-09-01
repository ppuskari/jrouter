package router

import (
	"fmt"
	"strings"

	"drjosh.dev/jrouter/atalk/nbp"
	"drjosh.dev/jrouter/aurp"
	"github.com/sfiera/multitalk/pkg/ddp"
)

func peerSelectorMatches(selector string, peer *AURPPeer) bool {
	if strings.TrimSpace(selector) == "" {
		return true
	}
	selector = strings.ToLower(strings.TrimSpace(selector))
	if selector == strings.ToLower(peer.TunnelID()) {
		return true
	}
	if peer.ConfiguredAddr != "" {
		configured := strings.ToLower(peer.ConfiguredAddr)
		if selector == configured || selector == "cfg:"+configured {
			return true
		}
	}
	return false
}

func (r AURPRemapRule) matchesPeer(peer *AURPPeer) bool {
	return peerSelectorMatches(r.Peer, peer)
}

func remapNetworkNumber(
	network ddp.Network,
	fromStart ddp.Network,
	fromEnd ddp.Network,
	toStart ddp.Network,
) (ddp.Network, bool) {
	if network < fromStart || network > fromEnd {
		return network, false
	}
	return toStart + (network - fromStart), true
}

func (p *AURPPeer) remapInboundNetwork(
	network ddp.Network,
) (ddp.Network, bool) {
	for _, rule := range p.timing.RemapRules {
		if !rule.matchesPeer(p) {
			continue
		}
		if mapped, ok := remapNetworkNumber(
			network,
			rule.RemoteStart,
			rule.RemoteEnd,
			rule.LocalStart,
		); ok {
			return mapped, true
		}
	}
	return network, false
}

func (p *AURPPeer) remapOutboundNetwork(
	network ddp.Network,
) (ddp.Network, bool) {
	for _, rule := range p.timing.RemapRules {
		if !rule.matchesPeer(p) {
			continue
		}
		if mapped, ok := remapNetworkNumber(
			network,
			rule.LocalStart,
			rule.LocalEnd,
			rule.RemoteStart,
		); ok {
			return mapped, true
		}
	}
	return network, false
}

func (p *AURPPeer) remapInboundRange(
	start ddp.Network,
	end ddp.Network,
) (ddp.Network, ddp.Network, bool, error) {
	for _, rule := range p.timing.RemapRules {
		if !rule.matchesPeer(p) {
			continue
		}
		if start < rule.RemoteStart || end > rule.RemoteEnd {
			continue
		}
		mappedStart := rule.LocalStart + (start - rule.RemoteStart)
		mappedEnd := rule.LocalStart + (end - rule.RemoteStart)
		return mappedStart, mappedEnd, true, nil
	}
	return start, end, false, nil
}

func (p *AURPPeer) remapInboundNetworkTuple(
	tuple aurp.NetworkTuple,
) (aurp.NetworkTuple, error) {
	start, end, mapped, err := p.remapInboundRange(
		tuple.RangeStart,
		tuple.RangeEnd,
	)
	if err != nil {
		return tuple, err
	}
	if mapped {
		tuple.RangeStart = start
		tuple.RangeEnd = end
	}
	return tuple, nil
}

func (p *AURPPeer) remapInboundEvent(
	event aurp.EventTuple,
) (aurp.EventTuple, error) {
	switch event.EventCode {
	case aurp.EventCodeNA, aurp.EventCodeNDC:
		start, end, mapped, err := p.remapInboundRange(
			event.RangeStart,
			event.RangeEnd,
		)
		if err != nil {
			return event, err
		}
		if mapped {
			event.RangeStart = start
			event.RangeEnd = end
		}
	case aurp.EventCodeND, aurp.EventCodeNRC:
		if mapped, ok := p.remapInboundNetwork(event.RangeStart); ok {
			event.RangeStart = mapped
		}
	}
	return event, nil
}

func (p *AURPPeer) remapInboundDDP(packet *ddp.ExtPacket) error {
	changed := false
	if mapped, ok := p.remapInboundNetwork(packet.SrcNet); ok {
		packet.SrcNet = mapped
		changed = true
	}

	if packet.Proto == ddp.ProtoNBP {
		nbpPacket, err := nbp.Unmarshal(packet.Data)
		if err != nil {
			return fmt.Errorf("unmarshal NBP for remapping: %w", err)
		}
		nbpChanged := false
		for i := range nbpPacket.Tuples {
			mapped, ok := p.remapInboundNetwork(nbpPacket.Tuples[i].Network)
			if !ok {
				continue
			}
			nbpPacket.Tuples[i].Network = mapped
			nbpChanged = true
		}
		if nbpChanged {
			data, err := nbpPacket.Marshal()
			if err != nil {
				return fmt.Errorf("marshal remapped NBP: %w", err)
			}
			packet.Data = data
			changed = true
		}
	}

	if changed {
		packet.Cksum = 0
	}
	return nil
}

func (p *AURPPeer) remapOutboundDDP(
	packet *ddp.ExtPacket,
) (*ddp.ExtPacket, error) {
	mapped, ok := p.remapOutboundNetwork(packet.DstNet)
	if !ok {
		return packet, nil
	}
	clone := *packet
	clone.DstNet = mapped
	clone.Cksum = 0
	clone.Data = append([]byte(nil), packet.Data...)
	return &clone, nil
}

func (r AURPImportHideRule) matchesPeer(peer *AURPPeer) bool {
	return peerSelectorMatches(r.Peer, peer)
}

func (p *AURPPeer) importNetworkHidden(network ddp.Network) bool {
	for _, rule := range p.timing.HiddenImportNetworks {
		if !rule.matchesPeer(p) {
			continue
		}
		if network >= rule.Start && network <= rule.End {
			return true
		}
	}
	return false
}

func (p *AURPPeer) importRangeHidden(
	start ddp.Network,
	end ddp.Network,
) bool {
	for _, rule := range p.timing.HiddenImportNetworks {
		if !rule.matchesPeer(p) {
			continue
		}
		if start <= rule.End && end >= rule.Start {
			return true
		}
	}
	return false
}
