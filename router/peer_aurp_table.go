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
	"github.com/prometheus/client_golang/prometheus"
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
	prometheus.MustRegister(t)
	return t
}

// LookupOrCreate looks up a peer by raddr, or creates a peer if it is not
// found. It returns an error if raddr is not an IPv4 address. If it creates a
// new peer, it runs its handler in a new goroutine and increments wg.
func (t *AURPPeerTable) LookupOrCreate(
	ctx context.Context,
	logger *slog.Logger,
	wg *sync.WaitGroup,
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
	// Already exists?
	if p := t.peersByIP[key]; p != nil {
		return p, nil
	}
	// New.
	t.peersByIP[key] = peer
	aurp.Inc(&t.nextConnID)

	wg.Add(1)
	go peer.Handle(ctx, wg)
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

var (
	aurpPeerReceiverConnectedDesc = prometheus.NewDesc(
		"jrouter_aurp_peer_receiver_connected",
		"0 if the receiver state for this peer is unconnected, 1 otherwise",
		[]string{"peer"},
		nil,
	)
	aurpPeerSenderConnectedDesc = prometheus.NewDesc(
		"jrouter_aurp_peer_sender_connected",
		"0 if the sender state for this peer is unconnected, 1 otherwise",
		[]string{"peer"},
		nil,
	)
	aurpPeerSendRetriesDesc = prometheus.NewDesc(
		"jrouter_aurp_peer_send_retries",
		"current send retries for each peer",
		[]string{"peer"},
		nil,
	)
	aurpPeerLastHeardFromDesc = prometheus.NewDesc(
		"jrouter_aurp_peer_last_heard_from_timestamp_seconds",
		"timestamp of lastHeardFrom",
		[]string{"peer"},
		nil,
	)
	aurpPeerLastReconnectDesc = prometheus.NewDesc(
		"jrouter_aurp_peer_last_reconnect_timestamp_seconds",
		"timestamp of lastReconnect",
		[]string{"peer"},
		nil,
	)
	aurpPeerLastSendDesc = prometheus.NewDesc(
		"jrouter_aurp_peer_last_send_timestamp_seconds",
		"timestamp of lastSend",
		[]string{"peer"},
		nil,
	)
	aurpPeerLastUpdateDesc = prometheus.NewDesc(
		"jrouter_aurp_peer_last_update_timestamp_seconds",
		"timestamp of lastUpdate",
		[]string{"peer"},
		nil,
	)
)

func (t *AURPPeerTable) Describe(ch chan<- *prometheus.Desc) {
	ch <- aurpPeerReceiverConnectedDesc
	ch <- aurpPeerSenderConnectedDesc
	ch <- aurpPeerSendRetriesDesc
	ch <- aurpPeerLastHeardFromDesc
	ch <- aurpPeerLastReconnectDesc
	ch <- aurpPeerLastSendDesc
	ch <- aurpPeerLastUpdateDesc
}

func (t *AURPPeerTable) Collect(ch chan<- prometheus.Metric) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	for _, p := range t.peersByIP {
		rconn, sconn := 1, 1
		if p.ReceiverState() == ReceiverUnconnected {
			rconn = 0
		}
		if p.SenderState() == SenderUnconnected {
			sconn = 0
		}
		raddr := p.RemoteAddr.String()
		ch <- prometheus.MustNewConstMetric(
			aurpPeerReceiverConnectedDesc,
			prometheus.GaugeValue,
			float64(rconn),
			raddr,
		)
		ch <- prometheus.MustNewConstMetric(
			aurpPeerSenderConnectedDesc,
			prometheus.GaugeValue,
			float64(sconn),
			raddr,
		)
		ch <- prometheus.MustNewConstMetric(
			aurpPeerSendRetriesDesc,
			prometheus.GaugeValue,
			float64(p.SendRetries()),
			raddr,
		)
		ch <- prometheus.MustNewConstMetric(
			aurpPeerLastHeardFromDesc,
			prometheus.GaugeValue,
			float64(p.LastHeardFrom().Unix()),
			raddr,
		)
		ch <- prometheus.MustNewConstMetric(
			aurpPeerLastReconnectDesc,
			prometheus.GaugeValue,
			float64(p.LastReconnect().Unix()),
			raddr,
		)
		ch <- prometheus.MustNewConstMetric(
			aurpPeerLastSendDesc,
			prometheus.GaugeValue,
			float64(p.LastSend().Unix()),
			raddr,
		)
		ch <- prometheus.MustNewConstMetric(
			aurpPeerLastUpdateDesc,
			prometheus.GaugeValue,
			float64(p.LastUpdate().Unix()),
			raddr,
		)
	}
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
		<td>{{$peer.LastHeardFrom | ago}}</td>
		<td>{{$peer.LastReconnect | ago}}</td>
		<td>{{$peer.LastUpdate | ago}}</td>
		<td>{{$peer.LastSend | ago}}</td>
		<td>{{$peer.SendRetries}}</td>
	</tr>
{{end}}
	</tbody>
</table>
`
