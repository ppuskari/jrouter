package router

import (
	"context"
	"fmt"
	"slices"

	"drjosh.dev/jrouter/atalk/nbp"
	"drjosh.dev/jrouter/atalk/rtmp"
	"github.com/sfiera/multitalk/pkg/ddp"
)

func (c AURPConfig) clusterForNetwork(
	network ddp.Network,
) (AURPClusterRule, bool) {
	for _, cluster := range c.Clusters {
		if cluster.contains(network) {
			return cluster, true
		}
	}
	return AURPClusterRule{}, false
}

func buildRTMPTuplesWithClusters(
	routes []Route,
	cfg AURPConfig,
	hopCountReduction bool,
) []rtmp.NetworkTuple {
	type clusterState struct {
		rule    AURPClusterRule
		members []Route
		zones   Set[string]
		distance uint8
	}
	clusters := make(map[ddp.Network]*clusterState)
	var normal []Route

	for _, route := range routes {
		clustered := false
		if route.RouteOrigin().Kind == RouteOriginAURP {
			for _, rule := range cfg.Clusters {
				if !rule.containsRoute(route) {
					continue
				}
				state := clusters[rule.Start]
				if state == nil {
					state = &clusterState{
						rule: rule,
						zones: make(Set[string]),
						distance: rtmpAdvertisedDistance(
							route,
							hopCountReduction,
						),
					}
					clusters[rule.Start] = state
				}
				state.members = append(state.members, route)
				for _, zone := range route.ZoneNames() {
					state.zones.Insert(zone)
				}
				d := rtmpAdvertisedDistance(route, hopCountReduction)
				if d < state.distance {
					state.distance = d
				}
				clustered = true
				break
			}
		}
		if !clustered {
			normal = append(normal, route)
		}
	}

	var tuples []rtmp.NetworkTuple
	for _, route := range normal {
		tuples = append(tuples, rtmp.NetworkTuple{
			Extended:   route.Extended,
			RangeStart: route.NetStart,
			RangeEnd:   route.NetEnd,
			Distance: rtmpAdvertisedDistance(
				route,
				hopCountReduction,
			),
		})
	}
	for _, state := range clusters {
		if len(state.zones) > 255 {
			for _, route := range state.members {
				tuples = append(tuples, rtmp.NetworkTuple{
						Extended:   route.Extended,
						RangeStart: route.NetStart,
						RangeEnd:   route.NetEnd,
						Distance: rtmpAdvertisedDistance(
							route,
							hopCountReduction,
						),
					})
			}
			continue
		}
		tuples = append(tuples, rtmp.NetworkTuple{
			Extended:   true,
			RangeStart: state.rule.Start,
			RangeEnd:   state.rule.End,
			Distance:   state.distance,
		})
	}
	slices.SortFunc(tuples, func(a, b rtmp.NetworkTuple) int {
		return int(a.RangeStart) - int(b.RangeStart)
	})
	return tuples
}

func (rtr *Router) clusterZones(
	cluster AURPClusterRule,
) []string {
	zones := make(Set[string])
	for route := range rtr.RouteTable.ValidRoutesForClass(TargetClassAURPPeer) {
		if !cluster.containsRoute(route) {
			continue
		}
		for _, zone := range route.ZoneNames() {
			zones.Insert(zone)
		}
	}
	out := zones.ToSlice()
	slices.Sort(out)
	return out
}

func (rtr *Router) zonesForZIPNetworks(
	networks []ddp.Network,
) map[ddp.Network][]string {
	result := make(map[ddp.Network][]string)
	for _, network := range networks {
		if rtr.Config != nil {
			if cluster, ok := rtr.Config.AURP.clusterForNetwork(network); ok {
				zones := rtr.clusterZones(cluster)
				if len(zones) > 0 {
					result[cluster.Start] = zones
				}
				continue
			}
		}
		for n, zones := range rtr.RouteTable.ZonesForNetworks(
			[]ddp.Network{network},
		) {
			result[n] = zones
		}
	}
	return result
}

func routeHasZone(route Route, zone string) bool {
	for _, candidate := range route.ZoneNames() {
		if candidate == zone {
			return true
		}
	}
	return false
}

func (rtr *Router) handleClusteredNBPFwdReq(
	ctx context.Context,
	packet *ddp.ExtPacket,
) (bool, error) {
	if rtr.Config == nil {
		return false, nil
	}
	cluster, ok := rtr.Config.AURP.clusterForNetwork(packet.DstNet)
	if !ok {
		return false, nil
	}
	nbpPacket, err := nbp.Unmarshal(packet.Data)
	if err != nil {
		return true, fmt.Errorf(
			"unmarshal clustered NBP FwdReq: %w",
			err,
		)
	}
	if nbpPacket.Function != nbp.FunctionFwdReq ||
		len(nbpPacket.Tuples) != 1 {
		return false, nil
	}
	zone := nbpPacket.Tuples[0].Zone
	for route := range rtr.RouteTable.ValidRoutesForClass(TargetClassAURPPeer) {
		if !cluster.containsRoute(route) || !routeHasZone(route, zone) {
			continue
		}
		clone := *packet
		clone.Data = append([]byte(nil), packet.Data...)
		clone.DstNet = route.NetStart
		if err := rtr.Forward(ctx, &clone); err != nil {
			return true, err
		}
	}
	return true, nil
}
