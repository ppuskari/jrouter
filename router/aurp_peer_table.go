/*
   Copyright 2025 Josh Deprez

   Licensed under the Apache License, Version 2.0 (the "License");
   you may not use this file except in compliance with the License.
   You may obtain a copy of the License at

       http://www.apache.org/licenses/LICENSE-2.0

   Unless required by applicable law or agreed to in writing, software
   distributed under the License is distributed on an "AS IS" BASIS,
   WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
   See the License for the specific language governing permissions and
   limitations under the License.
*/

package router

import (
	"cmp"
	"context"
	_ "embed"
	"encoding/binary"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net"
	"net/http"
	"slices"
	"sync"
	"text/template"
	"time"

	"drjosh.dev/jrouter/aurp"
	"drjosh.dev/jrouter/status"
	"github.com/prometheus/client_golang/prometheus"
)

// AURPPeerTable tracks connections to AURP peers.
type AURPPeerTable struct {
	logger *slog.Logger

	mu         sync.RWMutex
	peersByIP  map[[4]byte]*AURPPeer // for dispatching packets
	nextConnID uint16
}

// NewAURPPeerTable creates a new AURP peer table.
func NewAURPPeerTable(ctx context.Context, logger *slog.Logger) *AURPPeerTable {
	t := &AURPPeerTable{
		logger:    logger,
		peersByIP: make(map[[4]byte]*AURPPeer),
	}
	for t.nextConnID == 0 {
		t.nextConnID = uint16(rand.UintN(0x10000))
	}
	status.AddItem(ctx, "AURP Peers", peerTableTemplate, t.status)
	prometheus.MustRegister(t)
	return t
}

// RunAll runs all peer handlers in goroutines.
func (t *AURPPeerTable) RunAll(ctx context.Context, wg *sync.WaitGroup) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	wg.Add(len(t.peersByIP))
	for _, peer := range t.peersByIP {
		go peer.Handle(ctx, wg)
	}
}

// LookupOrCreate looks up a peer by raddr, or creates a peer if it is not
// found. It returns an error if raddr is not an IPv4 address.
func (t *AURPPeerTable) LookupOrCreate(
	ctx context.Context,
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
		Transport:      aurp.NewTransport(localDI, remoteDI, t.nextConnID, 0),
		UDPConn:        udpConn,
		ConfiguredAddr: peerAddr,
		RemoteAddr:     raddr,
		ReceiveCh:      make(chan aurp.RoutingPacket, 1024),
		RouteTable:     routes,

		logger:      logger.With("raddr", raddr, "remote-di", remoteDI),
		reconnectCh: make(chan struct{}, 1),
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	// Already exists?
	if p := t.peersByIP[key]; p != nil {
		return p, nil
	}
	// New.
	t.peersByIP[key] = peer
	t.nextConnID = aurp.Succ(t.nextConnID)

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

// ServeHTTP serves diagnostic pages for AURP peers, such as the chatlog.
func (t *AURPPeerTable) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Only the chat log so far
	ipStr := r.PathValue("ip")
	peer, err := t.Lookup(net.ParseIP(ipStr))
	if err != nil {
		http.Error(w, fmt.Sprintf("invalid address %q: %v", ipStr, err), http.StatusNotFound)
		return
	}
	if peer == nil {
		http.Error(w, fmt.Sprintf("peer %q not found", ipStr), http.StatusNotFound)
		return
	}

	if err := chatLogTmpl.Execute(w, peer); err != nil {
		t.logger.Error("Executing chatlog template", "error", err)
	}
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

// PeriodicallyAttemptConnections scans the peer table every 10 seconds looking
// for configured peers that are disconnected, and attempts to connect them.
func (t *AURPPeerTable) PeriodicallyAttemptConnections(ctx context.Context, logger *slog.Logger, wg *sync.WaitGroup) {
	defer wg.Done()

	ctx, setStatus, _ := status.AddSimpleItem(ctx, "Periodically Attempt Connections")
	setStatus("Running")
	defer setStatus("Stopped!")

	scanTicker := time.NewTicker(10 * time.Second)
	defer scanTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return

		case <-scanTicker.C:
			// continue below
		}

		peers := func() []*AURPPeer {
			t.mu.RLock()
			defer t.mu.RUnlock()
			peers := make([]*AURPPeer, 0, len(t.peersByIP))
			for _, peer := range t.peersByIP {
				if peer.ConfiguredAddr != "" && peer.ReceiverState() == ReceiverUnconnected {
					peers = append(peers, peer)
				}
			}
			return peers
		}()

		for _, peer := range peers {
			t.reconnectPeer(ctx, logger, wg, peer)
		}
	}
}

func (t *AURPPeerTable) reconnectPeer(ctx context.Context, logger *slog.Logger, wg *sync.WaitGroup, peer *AURPPeer) error {
	if peer.ConfiguredAddr == "" {
		return nil
	}

	raddr, err := net.ResolveIPAddr("ip4", peer.ConfiguredAddr)
	if err != nil {
		logger.Warn("Couldn't resolve UDP address, skipping", "configured-addr", peer.ConfiguredAddr, "error", err)
		return nil
	}
	logger.Debug("AURP Peer: resolved address", "configured-addr", peer.ConfiguredAddr, "raddr", raddr)
	raddr4 := raddr.IP.To4()
	if raddr4 == nil {
		logger.Warn("Resolved peer address is not an IPv4 address, skipping", "configured-addr", peer.ConfiguredAddr, "raddr", raddr)
		return nil
	}

	// Did it resolve to the same address?
	if peer.RemoteAddr.Equal(raddr4) {
		peer.reconnectCh <- struct{}{}
		return nil
	}

	// It was a different address.
	newPeer, err := t.LookupOrCreate(ctx, logger,
		peer.RouteTable,
		peer.UDPConn,
		peer.ConfiguredAddr,
		raddr4,
		peer.Transport.LocalDI(),
		peer.Transport.RemoteDI(),
	)
	if err != nil {
		return err
	}

	// Make the new peer the "configured" peer.
	newPeer.ConfiguredAddr = peer.ConfiguredAddr
	peer.ConfiguredAddr = ""

	if newPeer.Running() {
		newPeer.reconnectCh <- struct{}{}
		return nil
	}

	// Not running. The handle loop sends an Open-Req on startup.
	wg.Add(1)
	go newPeer.Handle(ctx, wg)
	return nil
}

//go:embed chatlog.html.tmpl
var chatLogTmplSrc string

var chatLogTmpl = template.Must(template.New("chatlog").Funcs(status.FuncMap()).Parse(chatLogTmplSrc))

const peerTableTemplate = `
<table>
	<thead><tr>
		<th>Configured addr</th>
		<th>Remote addr</th>
		<th>Running?</th>
		<th>Receiver state</th>
		<th>Sender state</th>
		<th>RecvCh len</th>
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
		<td><a href="/chatlog/{{$peer.RemoteAddr}}">{{$peer.RemoteAddr}}</a></td>
		<td class="{{if $peer.Running}}green{{else}}red{{end}}">{{if $peer.Running}}running{{else}}stopped{{end}}</td>
		<td class="{{if $peer.ReceiverConnected}}green{{else}}red{{end}}">{{$peer.ReceiverState}}</td>
		<td class="{{if $peer.SenderConnected}}green{{else}}red{{end}}">{{$peer.SenderState}}</td>
		<td>{{$peer.ReceiveChLen}}</td>
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
