package router

import (
	"context"
	"net"
	"testing"

	"drjosh.dev/jrouter/atalk/nbp"
	"drjosh.dev/jrouter/aurp"
	"github.com/sfiera/multitalk/pkg/ddp"
)

type set28RecordingTarget struct {
	key     string
	packets []*ddp.ExtPacket
}

func (t *set28RecordingTarget) Forward(
	_ context.Context,
	packet *ddp.ExtPacket,
) error {
	clone := *packet
	clone.Data = append([]byte(nil), packet.Data...)
	t.packets = append(t.packets, &clone)
	return nil
}

func (t *set28RecordingTarget) RouteTargetKey() string {
	return t.key
}

func (t *set28RecordingTarget) Class() TargetClass {
	return TargetClassAURPPeer
}

func TestSet28RemapDeviceHideChecksumComposition(t *testing.T) {
	for _, withChecksum := range []bool{false, true} {
		name := "zero-checksum"
		if withChecksum {
			name = "valid-checksum"
		}
		t.Run(name, func(t *testing.T) {
			peer := &AURPPeer{
				tunnelID: "cfg:combo.example",
				timing: AURPConfig{
					RemapRules: []AURPRemapRule{{
						Peer:        "cfg:combo.example",
						RemoteStart: 100,
						RemoteEnd:   109,
						LocalStart:  5000,
						LocalEnd:    5009,
					}},
					HiddenDevices: []AURPDeviceHideRule{{
						Peer:      "cfg:combo.example",
						Type:      "LaserWriter",
						Direction: "import",
					}},
				},
			}

			raw, err := (&nbp.Packet{
				Function: nbp.FunctionLkUpReply,
				NBPID:    7,
				Tuples: []nbp.Tuple{
					{
						Network: 103,
						Node:    10,
						Socket:  2,
						Object:  "Printer",
						Type:    "LaserWriter",
						Zone:    "Remote",
					},
					{
						Network: 104,
						Node:    11,
						Socket:  2,
						Object:  "Server",
						Type:    "AFPServer",
						Zone:    "Remote",
					},
				},
			}).Marshal()
			if err != nil {
				t.Fatal(err)
			}

			packet := &ddp.ExtPacket{
				ExtHeader: ddp.ExtHeader{
					Size:      uint16(13 + len(raw)),
					SrcNet:    102,
					DstNet:    900,
					SrcNode:   1,
					DstNode:   2,
					SrcSocket: 2,
					DstSocket: 2,
					Proto:     ddp.ProtoNBP,
				},
				Data: raw,
			}
			if withChecksum {
				checksum, err := computeDDPChecksum(packet)
				if err != nil {
					t.Fatal(err)
				}
				packet.Cksum = checksum
			}

			if err := peer.remapInboundDDP(packet); err != nil {
				t.Fatal(err)
			}
			if packet.SrcNet != 5002 {
				t.Fatalf("remapped source = %d, want 5002", packet.SrcNet)
			}
			if packet.Cksum != 0 {
				t.Fatalf("checksum after remap = 0x%04x, want zero", packet.Cksum)
			}

			filtered, drop, err := peer.filterDeviceNBP(packet, "import")
			if err != nil {
				t.Fatal(err)
			}
			if drop {
				t.Fatal("mixed hidden/visible NBP reply was dropped")
			}
			parsed, err := nbp.Unmarshal(filtered.Data)
			if err != nil {
				t.Fatal(err)
			}
			if len(parsed.Tuples) != 1 {
				t.Fatalf("visible tuples = %d, want 1", len(parsed.Tuples))
			}
			got := parsed.Tuples[0]
			if got.Type != "AFPServer" || got.Network != 5004 {
				t.Fatalf(
					"visible tuple = type %q network %d, want AFPServer/5004",
					got.Type,
					got.Network,
				)
			}
			if filtered.Cksum != 0 {
				t.Fatalf(
					"checksum after remap+filter = 0x%04x, want zero",
					filtered.Cksum,
				)
			}
		})
	}
}

func TestSet28ClusterZoneUnionAndFwdReqExpansion(t *testing.T) {
	rt := NewRouteTable(t.Context())
	targetA := &set28RecordingTarget{key: "cluster-a"}
	targetB := &set28RecordingTarget{key: "cluster-b"}

	if _, err := rt.UpsertRoute(targetA, true, 5000, 5000, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := rt.UpsertRoute(targetB, true, 5001, 5001, 1); err != nil {
		t.Fatal(err)
	}
	if err := rt.ReplaceZonesForNetwork(5000, "Zone A"); err != nil {
		t.Fatal(err)
	}
	if err := rt.ReplaceZonesForNetwork(5001, "Zone B"); err != nil {
		t.Fatal(err)
	}

	rtr := &Router{
		RouteTable: rt,
		Config: &Config{AURP: AURPConfig{
			Clusters: []AURPClusterRule{{
				Start: 5000,
				End:   5009,
			}},
		}},
	}

	zones := rtr.zonesForZIPNetworks([]ddp.Network{5000})[5000]
	if len(zones) != 2 || zones[0] != "Zone A" || zones[1] != "Zone B" {
		t.Fatalf("cluster zone union = %v, want [Zone A Zone B]", zones)
	}

	raw, err := (&nbp.Packet{
		Function: nbp.FunctionFwdReq,
		NBPID:    9,
		Tuples: []nbp.Tuple{{
			Network: 5000,
			Node:    0,
			Socket:  2,
			Object:  "=",
			Type:    "=",
			Zone:    "Zone A",
		}},
	}).Marshal()
	if err != nil {
		t.Fatal(err)
	}
	packet := &ddp.ExtPacket{
		ExtHeader: ddp.ExtHeader{
			DstNet:    5000,
			DstNode:   0,
			DstSocket: 2,
			Proto:     ddp.ProtoNBP,
		},
		Data: raw,
	}

	handled, err := rtr.handleClusteredNBPFwdReq(
		t.Context(),
		packet,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !handled {
		t.Fatal("clustered NBP FwdReq was not handled")
	}
	if len(targetA.packets) != 1 {
		t.Fatalf("Zone A forwards = %d, want 1", len(targetA.packets))
	}
	if len(targetB.packets) != 0 {
		t.Fatalf("Zone B received %d unexpected forwards", len(targetB.packets))
	}
	if targetA.packets[0].DstNet != 5000 {
		t.Fatalf(
			"expanded destination = %d, want member network 5000",
			targetA.packets[0].DstNet,
		)
	}
	if packet.DstNet != 5000 {
		t.Fatalf("original clustered packet mutated to %d", packet.DstNet)
	}
}

func TestSet28DNSEndpointSwitchKeepsInstalledRouteOwnership(t *testing.T) {
	table := newTestAURPPeerTable()
	rt := NewRouteTable(t.Context())
	localDI := aurp.IPDomainIdentifier(net.IPv4(192, 0, 2, 1))
	ip1, ip2, _ := testPeerIPs()

	peer, err := table.LookupOrCreate(
		t.Context(),
		table.logger,
		rt,
		nil,
		"route-owner.example",
		ip1,
		localDI,
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := table.LookupOrCreate(
		t.Context(),
		table.logger,
		rt,
		nil,
		"route-owner.example",
		ip2,
		localDI,
		nil,
	); err != nil {
		t.Fatal(err)
	}

	if _, err := rt.UpsertRoute(peer, true, 4200, 4200, 2); err != nil {
		t.Fatal(err)
	}
	if err := rt.ReplaceZonesForNetwork(4200, "Stable Route"); err != nil {
		t.Fatal(err)
	}
	before := rt.Lookup(4200)
	if before.Zero() || before.Target != peer {
		t.Fatalf("initial route = %v, want configured peer", before)
	}
	wantKey := peer.RouteTargetKey()
	wantOrigin := before.RouteOrigin()

	switched, err := table.setConfiguredCandidates(peer, []net.IP{ip2})
	if err != nil {
		t.Fatal(err)
	}
	if !switched {
		t.Fatal("DNS candidate removal did not switch active endpoint")
	}
	if got := peer.RouteTargetKey(); got != wantKey {
		t.Fatalf("route target key changed: %q -> %q", wantKey, got)
	}

	// Routes learned through the old transport must be withdrawn while the
	// new endpoint reconnects. Keeping them would preserve stale reachability.
	if stale := rt.Lookup(4200); !stale.Zero() {
		t.Fatalf("stale route survived endpoint switch: %v", stale)
	}
	if got := peer.RemoteAddr().String(); got != ip2.String() {
		t.Fatalf("active endpoint = %s, want %s", got, ip2)
	}

	// Simulate the RI-Rsp relearn after the new endpoint connects. The route
	// must return under the same logical tunnel identity, not a new endpoint
	// identity that would orphan policy or alternative-path state.
	if _, err := rt.UpsertRoute(peer, true, 4200, 4200, 2); err != nil {
		t.Fatal(err)
	}
	if err := rt.ReplaceZonesForNetwork(4200, "Stable Route"); err != nil {
		t.Fatal(err)
	}
	after := rt.Lookup(4200)
	if after.Zero() || after.Target != peer {
		t.Fatalf("relearned route = %v, want configured peer", after)
	}
	if got := after.RouteOrigin(); got != wantOrigin {
		t.Fatalf("relearned route origin changed: got %+v want %+v", got, wantOrigin)
	}
}
