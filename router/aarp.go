/*
   Copyright 2024 Josh Deprez

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
	"context"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"sync"
	"time"

	"drjosh.dev/jrouter/status"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/sfiera/multitalk/pkg/aarp"
	"github.com/sfiera/multitalk/pkg/ddp"
	"github.com/sfiera/multitalk/pkg/ethernet"
	"github.com/sfiera/multitalk/pkg/ethertalk"
)

const (
	maxAMTEntryAge        = 30 * time.Second
	aarpRequestRetransmit = 1 * time.Second
	aarpRequestTimeout    = 10 * time.Second

	aarpBodyLength = 28 // bytes
)

const aarpStatusTemplate = `
Status: {{.Status}}<br/>
<table>
	<thead><tr>
		<th>DDP addr</th>
		<th>Ethernet addr</th>
		<th>Valid?
		<th>Last updated</th>
	</tr></thead>
	<tbody>
{{range $key, $entry := .AMT}}
		<tr>
			<td>{{$key.Network}}.{{$key.Node}}</td>
			<td>{{$entry.HWAddr}}</td>
			<td class="{{if $entry.Valid}}green{{else}}red{{end}}">{{if $entry.Valid}}valid{{else}}stale{{end}}</td>
			<td>{{$entry.LastUpdated | ago}}</td>
		</tr>
{{end}}
	</tbody>
</table>
`

// AARPMachine maintains both an Address Mapping Table and handles AARP packets
// (sending and receiving requests, responses, and probes). This process assumes
// a particular network range rather than using the startup range, since this
// program is a seed router.
type AARPMachine struct {
	*addressMappingTable

	port *EtherTalkPort

	incomingCh chan *ethertalk.Packet

	logger *slog.Logger

	// The Run goroutine is responsible for all writes to myAddr.Proto and
	// probes, so this mutex is not used to enforce a single writer, only
	// consistent reads
	mu         sync.RWMutex
	statusMsg  string
	myAddr     aarp.AddrPair
	probes     int
	assigned   bool
	assignedCh chan struct{}
}

// NewAARPMachine creates a new AARPMachine.
func NewAARPMachine(logger *slog.Logger, port *EtherTalkPort, myHWAddr ethernet.Addr) *AARPMachine {
	return &AARPMachine{
		addressMappingTable: new(addressMappingTable),
		port:                port,
		incomingCh:          make(chan *ethertalk.Packet, 1024), // arbitrary capacity
		logger:              logger,
		myAddr: aarp.AddrPair{
			Hardware: myHWAddr,
		},
		assignedCh: make(chan struct{}),
	}
}

// Handle handles a packet.
func (a *AARPMachine) Handle(ctx context.Context, pkt *ethertalk.Packet) {
	select {
	case <-ctx.Done():
	case a.incomingCh <- pkt:
	}
}

// Address returns the address of this node, and reports if the address is valid
// (i.e. not tentative).
func (a *AARPMachine) Address() (aarp.AddrPair, bool) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.myAddr, a.assigned
}

// Assigned returns a channel that is closed when the local address is valid.
func (a *AARPMachine) Assigned() <-chan struct{} {
	return a.assignedCh
}

func (a *AARPMachine) status(ctx context.Context) (any, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return struct {
		Status string
		AMT    map[ddp.Addr]AMTEntry
	}{
		Status: a.statusMsg,
		AMT:    a.addressMappingTable.Dump(),
	}, nil
}

// Run executes the machine.
func (a *AARPMachine) Run(ctx context.Context) error {
	ctx, done := status.AddItem(ctx, fmt.Sprintf("AARP on %s", a.port.device), aarpStatusTemplate, a.status)
	defer done()

	// Initialise our DDP address with a preferred address (first network.1)
	a.mu.Lock()
	a.statusMsg = "Initialising"
	a.probes = 0
	a.myAddr.Proto = ddp.Addr{
		Network: ddp.Network(a.port.netStart),
		Node:    1,
	}
	a.mu.Unlock()

	ticker := time.NewTicker(200 * time.Millisecond) // 200ms is the AARP probe retransmit
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()

		case <-ticker.C:
			if a.probes >= 10 {
				a.mu.Lock()
				a.statusMsg = fmt.Sprintf("Assigned address %d.%d", a.myAddr.Proto.Network, a.myAddr.Proto.Node)
				a.assigned = true
				a.mu.Unlock()
				close(a.assignedCh)
				ticker.Stop()
				continue
			}

			a.mu.Lock()
			a.statusMsg = fmt.Sprintf("Probed %d times", a.probes)
			a.probes++
			a.mu.Unlock()

			if err := a.probe(); err != nil {
				a.logger.Error("AARP: Couldn't broadcast a Probe", "error", err)
			}

		case ethFrame, open := <-a.incomingCh:
			if !open {
				a.incomingCh = nil
			}

			// sfiera/multitalk will return an "excess data" error if the
			// payload is too big. Most traffic I've seen locally does not have
			// this problem, but I've seen one report with some junk trailing
			// data on AARP packets.
			payload := ethFrame.Payload
			if len(payload) > aarpBodyLength {
				payload = payload[:aarpBodyLength]
			}

			var aapkt aarp.Packet
			if err := aarp.Unmarshal(payload, &aapkt); err != nil {
				a.logger.Error("AARP: Couldn't unmarshal packet", "error", err)
				continue
			}

			switch aapkt.Opcode {
			case aarp.RequestOp:
				a.logger.Debug(fmt.Sprintf("AARP: Who has %d.%d? Tell %d.%d",
					aapkt.Dst.Proto.Network, aapkt.Dst.Proto.Node,
					aapkt.Src.Proto.Network, aapkt.Src.Proto.Node,
				))
				// Glean that aapkt.Src.Proto -> aapkt.Src.Hardware
				a.addressMappingTable.learn(aapkt.Src.Proto, aapkt.Src.Hardware)
				// a.logger.Debug(fmt.Sprintf("AARP: Gleaned that %d.%d -> %v", aapkt.Src.Proto.Network, aapkt.Src.Proto.Node, aapkt.Src.Hardware))

				if aapkt.Dst.Proto != a.myAddr.Proto {
					a.logger.Debug(fmt.Sprintf("AARP: not replying to request for %d.%d (not my address)", aapkt.Dst.Proto.Network, aapkt.Dst.Proto.Node))
					continue
				}
				if !a.assigned {
					a.logger.Debug(fmt.Sprintf("AARP: not replying to request for %d.%d (address still tentative)", aapkt.Dst.Proto.Network, aapkt.Dst.Proto.Node))
					continue
				}

				// Hey that's me! Let them know!
				if err := a.heyThatsMe(aapkt.Src); err != nil {
					a.logger.Error("AARP: Couldn't respond to Request", "error", err)
					continue
				}

			case aarp.ResponseOp:
				a.logger.Debug(fmt.Sprintf("AARP: %d.%d is at %v",
					aapkt.Dst.Proto.Network, aapkt.Dst.Proto.Node, aapkt.Dst.Hardware,
				))
				a.addressMappingTable.learn(aapkt.Dst.Proto, aapkt.Dst.Hardware)

				if aapkt.Dst.Proto != a.myAddr.Proto {
					continue
				}
				if !a.assigned {
					a.reroll()
				}

			case aarp.ProbeOp:
				a.logger.Debug(fmt.Sprintf("AARP: %v probing to see if %d.%d is available",
					aapkt.Src.Hardware, aapkt.Src.Proto.Network, aapkt.Src.Proto.Node,
				))
				// AMT should not be updated, because the address is tentative

				if aapkt.Dst.Proto != a.myAddr.Proto {
					continue
				}
				if !a.assigned {
					// Another node is probing for the same address! Unlucky
					a.reroll()
					continue
				}

				if err := a.heyThatsMe(aapkt.Src); err != nil {
					a.logger.Error("AARP: Couldn't respond to Probe", "error", err)
					continue
				}
			}
		}
	}
}

// Re-roll a local address
func (a *AARPMachine) reroll() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.port.netStart != a.port.netEnd {
		// Pick a new network number at random
		a.myAddr.Proto.Network = rand.N(
			a.port.netEnd-a.port.netStart+1,
		) + a.port.netStart
	}

	// Can't use: 0x00, 0xff, 0xfe, and should avoid the existing node number
	newNode := rand.N[ddp.Node](0xfd) + 1
	for newNode != a.myAddr.Proto.Node {
		newNode = rand.N[ddp.Node](0xfd) + 1
	}
	a.myAddr.Proto.Node = newNode
	a.probes = 0
}

// Send an AARP response
func (a *AARPMachine) heyThatsMe(targ aarp.AddrPair) error {
	respFrame, err := ethertalk.AARP(a.myAddr.Hardware, aarp.Response(a.myAddr, targ))
	if err != nil {
		return err
	}
	//a.logger.Debug("AARP: sending packet", "resp-frame", respFrame)
	// Instead of broadcasting the reply, send it to the target specifically?
	respFrame.Dst = targ.Hardware
	return a.send(respFrame)
}

// Broadcast an AARP Probe
func (a *AARPMachine) probe() error {
	probeFrame, err := ethertalk.AARP(a.myAddr.Hardware, aarp.Probe(a.myAddr.Hardware, a.myAddr.Proto))
	if err != nil {
		return err
	}
	return a.send(probeFrame)
}

// Broadcast an AARP Request
func (a *AARPMachine) request(ddpAddr ddp.Addr) error {
	reqFrame, err := ethertalk.AARP(a.myAddr.Hardware, aarp.Request(a.myAddr, ddpAddr))
	if err != nil {
		return err
	}
	return a.send(reqFrame)
}

func (a *AARPMachine) send(pkt *ethertalk.Packet) error {
	frameRaw, err := ethertalk.Marshal(*pkt)
	if err != nil {
		return err
	}
	if len(frameRaw) < 64 {
		frameRaw = append(frameRaw, make([]byte, 64-len(frameRaw))...)
	}

	promLabels := prometheus.Labels{
		"port": a.port.device,
	}
	aarpPacketsOutCounter.With(promLabels).Inc()
	aarpBytesOutCounter.With(promLabels).Add(float64(len(frameRaw)))

	return a.port.pcapHandle.WritePacketData(frameRaw)
}

// AMTEntry is an entry in an address mapping table.
type AMTEntry struct {
	// The hardware address that the entry maps to.
	HWAddr ethernet.Addr

	// The last time this entry was updated.
	LastUpdated time.Time

	// Closed when this entry is updated.
	updated chan struct{}
}

// Valid reports if the entry is valid.
func (e AMTEntry) Valid() bool {
	return time.Since(e.LastUpdated) < maxAMTEntryAge
}

// addressMappingTable implements a concurrent-safe Address Mapping Table for
// AppleTalk (DDP) addresses to Ethernet hardware addresses.
type addressMappingTable struct {
	mu    sync.Mutex
	table map[ddp.Addr]*AMTEntry
}

// Dump returns a copy of the table at a point in time.
func (t *addressMappingTable) Dump() map[ddp.Addr]AMTEntry {
	t.mu.Lock()
	defer t.mu.Unlock()

	table := make(map[ddp.Addr]AMTEntry, len(t.table))
	for k, v := range t.table {
		table[k] = *v
	}
	return table
}

// learn adds or updates an AMT entry.
func (t *addressMappingTable) learn(ddpAddr ddp.Addr, hwAddr ethernet.Addr) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.table == nil {
		t.table = make(map[ddp.Addr]*AMTEntry)
	}
	oldEnt := t.table[ddpAddr]
	if oldEnt == nil {
		// Create new entry
		t.table[ddpAddr] = &AMTEntry{
			HWAddr:      hwAddr,
			LastUpdated: time.Now(),
			updated:     make(chan struct{}),
		}
		return
	}
	// Update existing entry
	oldEnt.HWAddr = hwAddr
	oldEnt.LastUpdated = time.Now()
	close(oldEnt.updated)
	oldEnt.updated = make(chan struct{})
}

// lookupOrWait returns either the valid cached Ethernet address for the given
// DDP address, or a non-nil channel that is closed when the entry is updated.
func (t *addressMappingTable) lookupOrWait(ddpAddr ddp.Addr) (ethernet.Addr, <-chan struct{}) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.table == nil {
		t.table = make(map[ddp.Addr]*AMTEntry)
	}
	ent := t.table[ddpAddr]
	if ent == nil {
		// Create new entry and channel.
		ch := make(chan struct{})
		t.table[ddpAddr] = &AMTEntry{updated: ch}
		return ethernet.Addr{}, ch
	}
	if !ent.Valid() {
		// Return existing channel.
		return ent.HWAddr, ent.updated
	}
	// Entry exists and is valid
	return ent.HWAddr, nil
}
