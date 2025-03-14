package router

import (
	"cmp"
	"context"
	"encoding/binary"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net"
	"slices"
	"sync"

	"drjosh.dev/jrouter/aurp"
	"drjosh.dev/jrouter/status"
)

// AURPPeerTable tracks connections to AURP peers.
type AURPPeerTable struct {
	mu         sync.RWMutex
	peersByIP  map[[4]byte]*AURPPeer
	nextConnID uint16
}

// NewAURPPeerTable creates a new AURP peer table.
func NewAURPPeerTable(ctx context.Context) *AURPPeerTable {
	t := &AURPPeerTable{
		peersByIP: make(map[[4]byte]*AURPPeer),
	}
	for t.nextConnID == 0 {
		t.nextConnID = uint16(rand.UintN(0x10000))
	}
	status.AddItem(ctx, "AURP Peers", peerTableTemplate, t.status)
	return t
}

// LookupOrCreate looks up a peer by raddr, or creates a peer if it is not
// found. It returns an error if raddr is not an IPv4 address.
func (t *AURPPeerTable) LookupOrCreate(
	logger *slog.Logger,
	routes *RouteTable,
	udpConn *net.UDPConn,
	peerAddr string,
	raddr net.IP,
	localDI, remoteDI aurp.DomainIdentifier,
) (*AURPPeer, error) {
	raddr4 := raddr.To4()
	if len(raddr4) != 4 {
		return nil, fmt.Errorf("remote addr %v is not an IPv4 address", raddr)
	}
	key := [4]byte(raddr4)

	if remoteDI == nil {
		remoteDI = aurp.IPDomainIdentifier(raddr)
	}
	peer := &AURPPeer{
		Transport: &aurp.Transport{
			LocalDI:     localDI,
			RemoteDI:    remoteDI,
			LocalConnID: t.nextConnID,
		},
		UDPConn:        udpConn,
		ConfiguredAddr: peerAddr,
		RemoteAddr:     raddr,
		ReceiveCh:      make(chan aurp.Packet, 1024),
		RouteTable:     routes,
		logger:         logger.With("raddr", raddr, "remote-di", remoteDI),
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	// Existing?
	if p := t.peersByIP[key]; p != nil {
		return p, nil
	}
	// New.
	t.peersByIP[key] = peer
	aurp.Inc(&t.nextConnID)
	return peer, nil
}

// Lookup looks up the peer associated with this IP address. It returns an error
// if the address is not an IPv4 address.
func (t *AURPPeerTable) Lookup(raddr net.IP) (*AURPPeer, error) {
	raddr4 := raddr.To4()
	if len(raddr4) != 4 {
		return nil, fmt.Errorf("remote addr %v is not an IPv4 address", raddr)
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.peersByIP[[4]byte(raddr4)], nil
}

func (t *AURPPeerTable) status(ctx context.Context) (any, error) {
	var peerInfo []*AURPPeer
	func() {
		t.mu.RLock()
		defer t.mu.RUnlock()
		peerInfo = make([]*AURPPeer, 0, len(t.peersByIP))
		for _, p := range t.peersByIP {
			peerInfo = append(peerInfo, p)
		}
	}()
	slices.SortFunc(peerInfo, func(pa, pb *AURPPeer) int {
		return cmp.Or(
			-cmp.Compare(
				bool2Int(pa.ReceiverState() == ReceiverConnected),
				bool2Int(pb.ReceiverState() == ReceiverConnected),
			),
			-cmp.Compare(
				bool2Int(pa.SenderState() == SenderConnected),
				bool2Int(pb.SenderState() == SenderConnected),
			),
			cmp.Compare(pa.ConfiguredAddr, pb.ConfiguredAddr),
			cmp.Compare(
				binary.BigEndian.Uint32(pa.RemoteAddr),
				binary.BigEndian.Uint32(pb.RemoteAddr),
			),
		)
	})
	return peerInfo, nil
}

func bool2Int(b bool) int {
	if b {
		return 1
	}
	return 0
}

const peerTableTemplate = `
<table>
	<thead><tr>
		<th>Configured addr</th>
		<th>Remote addr</th>
		<th>Running?</th>
		<th>Receiver state</th>
		<th>Sender state</th>
		<th>Last heard from</th>
		<th>Last reconnect</th>
		<th>Last update</th>
		<th>Last send</th>
		<th>Send retries</th>
	</tr></thead>
	<tbody>
{{range $peer := . }}
	<tr>
		<td>{{$peer.ConfiguredAddr}}</td>
		<td>{{$peer.RemoteAddr}}</td>
		<td>{{if $peer.Running}}✅{{else}}🛑{{end}}</td>
		<td>{{$peer.ReceiverState}}</td>
		<td>{{$peer.SenderState}}</td>
		<td>{{$peer.LastHeardFromAgo}}</td>
		<td>{{$peer.LastReconnectAgo}}</td>
		<td>{{$peer.LastUpdateAgo}}</td>
		<td>{{$peer.LastSendAgo}}</td>
		<td>{{$peer.SendRetries}}</td>
	</tr>
{{end}}
	</tbody>
</table>
`
