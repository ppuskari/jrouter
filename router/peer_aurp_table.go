package router

import (
	"cmp"
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"slices"
	"sync"

	"drjosh.dev/jrouter/status"
)

// AURPPeerTable tracks connections to AURP peers.
type AURPPeerTable struct {
	mu        sync.RWMutex
	peersByIP map[[4]byte]*AURPPeer
}

func NewAURPPeerTable(ctx context.Context) *AURPPeerTable {
	t := &AURPPeerTable{
		peersByIP: make(map[[4]byte]*AURPPeer),
	}
	status.AddItem(ctx, "AURP Peers", peerTableTemplate, t.status)
	return t
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

// Insert associates a peer with its IP address. It returns an error if the
// address is not an IPv4 address or if another peer for the address already
// exists in the table.
func (t *AURPPeerTable) Insert(peer *AURPPeer) error {
	raddr4 := peer.RemoteAddr.To4()
	if len(raddr4) != 4 {
		return fmt.Errorf("remote addr %v is not an IPv4 address", peer.RemoteAddr)
	}
	key := [4]byte(raddr4)
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.peersByIP[key] != nil {
		return fmt.Errorf("peer already exists for %v", peer.RemoteAddr)
	}
	t.peersByIP[key] = peer
	return nil
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
