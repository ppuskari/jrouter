package router

import (
	"bytes"
	"fmt"
	"slices"

	"drjosh.dev/jrouter/aurp"
	"github.com/sfiera/multitalk/pkg/ddp"
)

type pendingAURPZoneInfo struct {
	total uint16
	zones Set[string]
}

func dedupeSortedZones(zones []string) []string {
	set := make(Set[string])
	for _, zone := range zones {
		if zone != "" {
			set.Insert(zone)
		}
	}
	out := set.ToSlice()
	slices.Sort(out)
	return out
}

func aurpPacketSize(pkt aurp.Packet) (int, error) {
	var b bytes.Buffer
	if _, err := pkt.WriteTo(&b); err != nil {
		return 0, err
	}
	return b.Len(), nil
}

func buildZIRspPackets(
	tr *aurp.Transport,
	zoneLists map[ddp.Network][]string,
) ([]*aurp.ZIRspPacket, error) {
	networks := make([]ddp.Network, 0, len(zoneLists))
	for network := range zoneLists {
		networks = append(networks, network)
	}
	slices.Sort(networks)

	var packets []*aurp.ZIRspPacket
	for _, network := range networks {
		zones := dedupeSortedZones(zoneLists[network])
		if len(zones) == 0 {
			continue
		}

		one := map[ddp.Network][]string{network: zones}
		nonext := tr.NewZIRspPacket(one)
		size, err := aurpPacketSize(nonext)
		if err != nil {
			return nil, err
		}
		if size <= aurpRoutingDatagramBudget {
			packets = append(packets, nonext)
			continue
		}

		total := len(zones)
		if total > 65535 {
			return nil, fmt.Errorf(
				"network %d has too many zones for extended ZI-Rsp: %d",
				network,
				total,
			)
		}

		empty := tr.NewExtendedZIRspPacket(uint16(total), nil)
		baseSize, err := aurpPacketSize(empty)
		if err != nil {
			return nil, err
		}
		budget := aurpRoutingDatagramBudget - baseSize
		if budget <= 0 {
			return nil, fmt.Errorf(
				"extended ZI-Rsp header size %d exceeds datagram budget %d",
				baseSize,
				aurpRoutingDatagramBudget,
			)
		}

		var chunk aurp.ZoneTuples
		chunkSize := 0
		flush := func() {
			if len(chunk) == 0 {
				return
			}
			packets = append(
				packets,
				tr.NewExtendedZIRspPacket(uint16(total), chunk),
			)
			chunk = nil
			chunkSize = 0
		}

		for _, zone := range zones {
			tupleSize := 3 + len(zone)
			if tupleSize > budget {
				return nil, fmt.Errorf(
					"zone %q tuple size %d exceeds extended ZI-Rsp payload budget %d",
					zone,
					tupleSize,
					budget,
				)
			}
			if len(chunk) > 0 && chunkSize+tupleSize > budget {
				flush()
			}
			chunk = append(chunk, aurp.ZoneTuple{
				Network: network,
				Name:    zone,
			})
			chunkSize += tupleSize
		}
		flush()
	}
	return packets, nil
}

func (p *AURPPeer) sendZIRspPackets(
	zoneLists map[ddp.Network][]string,
) error {
	packets, err := buildZIRspPackets(p.Transport, zoneLists)
	if err != nil {
		return err
	}
	for _, pkt := range packets {
		if _, err := p.send(pkt); err != nil {
			return err
		}
	}
	return nil
}

func (p *AURPPeer) ownsAURPNetwork(network ddp.Network) bool {
	return p.RouteTable != nil &&
		!p.RouteTable.find(p, network).Zero()
}

func (p *AURPPeer) applyNonExtendedZIRsp(
	pkt *aurp.ZIRspPacket,
) (accepted, ignored int) {
	grouped := make(map[ddp.Network][]string)
	for _, zt := range pkt.Zones {
		if !p.ownsAURPNetwork(zt.Network) {
			ignored++
			continue
		}
		grouped[zt.Network] = append(grouped[zt.Network], zt.Name)
	}

	for network, zones := range grouped {
		if err := p.RouteTable.ReplaceZonesForNetwork(
			network,
			dedupeSortedZones(zones)...,
		); err != nil {
			ignored += len(zones)
			continue
		}
		delete(p.pendingZoneInfo, network)
		accepted += len(zones)
	}
	return accepted, ignored
}

func (p *AURPPeer) applyExtendedZIRsp(
	pkt *aurp.ZIRspPacket,
) (complete bool, network ddp.Network, err error) {
	if len(pkt.Zones) == 0 {
		return false, 0, nil
	}
	network = pkt.Zones[0].Network
	if !p.ownsAURPNetwork(network) {
		return false, network, nil
	}
	if pkt.TotalTuples == 0 {
		return false, network, fmt.Errorf(
			"extended ZI-Rsp for network %d has zero total tuple count",
			network,
		)
	}

	if p.pendingZoneInfo == nil {
		p.pendingZoneInfo = make(map[ddp.Network]*pendingAURPZoneInfo)
	}
	pending := p.pendingZoneInfo[network]
	if pending == nil || pending.total != pkt.TotalTuples {
		pending = &pendingAURPZoneInfo{
			total: pkt.TotalTuples,
			zones: make(Set[string]),
		}
		p.pendingZoneInfo[network] = pending
	}

	for _, zt := range pkt.Zones {
		if zt.Network != network {
			return false, network, fmt.Errorf(
				"extended ZI-Rsp mixed network %d with %d",
				network,
				zt.Network,
			)
		}
		if zt.Name != "" {
			pending.zones.Insert(zt.Name)
		}
	}

	if len(pending.zones) > int(pending.total) {
		delete(p.pendingZoneInfo, network)
		return false, network, fmt.Errorf(
			"extended ZI-Rsp for network %d exceeded declared tuple count %d",
			network,
			pending.total,
		)
	}
	if len(pending.zones) < int(pending.total) {
		return false, network, nil
	}

	zones := pending.zones.ToSlice()
	slices.Sort(zones)
	if err := p.RouteTable.ReplaceZonesForNetwork(network, zones...); err != nil {
		return false, network, err
	}
	delete(p.pendingZoneInfo, network)
	return true, network, nil
}
