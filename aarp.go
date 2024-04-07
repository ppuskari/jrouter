package main

import (
	"context"
	"log"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/google/gopacket/pcap"
	"github.com/sfiera/multitalk/pkg/aarp"
	"github.com/sfiera/multitalk/pkg/ddp"
	"github.com/sfiera/multitalk/pkg/ethernet"
	"github.com/sfiera/multitalk/pkg/ethertalk"
)

const (
	// TODO: verify parameters
	maxAMTEntryAge        = 30 * time.Second
	aarpRequestRetransmit = 1 * time.Second
	aarpRequestTimeout    = 10 * time.Second
)

// AARPMachine maintains both an Address Mapping Table and handles AARP packets
// (sending and receiving requests, responses, and probes). This process assumes
// a particular network range rather than using the startup range, since this
// program is a seed router.
type AARPMachine struct {
	*addressMappingTable

	cfg        *config
	pcapHandle *pcap.Handle

	// The Run goroutine is responsible for all writes to myAddr.Proto and
	// probes, so this mutex is not used to enforce a single writer, only
	// consistent reads
	mu         sync.RWMutex
	myAddr     aarp.AddrPair
	probes     int
	assignedCh chan struct{}
}

// NewAARPMachine creates a new AARPMachine.
func NewAARPMachine(cfg *config, pcapHandle *pcap.Handle, myHWAddr ethernet.Addr) *AARPMachine {
	return &AARPMachine{
		addressMappingTable: new(addressMappingTable),
		cfg:                 cfg,
		pcapHandle:          pcapHandle,
		myAddr: aarp.AddrPair{
			Hardware: myHWAddr,
		},
		assignedCh: make(chan struct{}),
	}
}

// Address returns the address of this node, and reports if the address is valid
// (i.e. not tentative).
func (a *AARPMachine) Address() (aarp.AddrPair, bool) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.myAddr, a.assigned()
}

// Assigned returns a channel that is closed when the local address is valid.
func (a *AARPMachine) Assigned() <-chan struct{} {
	return a.assignedCh
}

// Run executes the machine.
func (a *AARPMachine) Run(ctx context.Context, incomingCh <-chan *ethertalk.Packet) error {
	// Initialise our DDP address with a preferred address (first network.1)
	a.mu.Lock()
	a.probes = 0
	a.myAddr.Proto = ddp.Addr{
		Network: ddp.Network(a.cfg.EtherTalk.NetStart),
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
			if a.assigned() {
				close(a.assignedCh)
				// No need to keep the ticker running if assigned
				ticker.Stop()
				continue
			}

			a.mu.Lock()
			a.probes++
			a.mu.Unlock()

			if err := a.probe(); err != nil {
				log.Printf("Couldn't broadcast a Probe: %v", err)
			}

		case ethFrame, ok := <-incomingCh:
			if !ok {
				incomingCh = nil
			}

			var aapkt aarp.Packet
			if err := aarp.Unmarshal(ethFrame.Payload, &aapkt); err != nil {
				log.Printf("Couldn't unmarshal AARP packet: %v", err)
				continue
			}

			switch aapkt.Opcode {
			case aarp.RequestOp:
				log.Printf("AARP: Who has %v? Tell %v", aapkt.Dst.Proto, aapkt.Src.Proto)
				// Glean that aapkt.Src.Proto -> aapkt.Src.Hardware
				a.addressMappingTable.Learn(aapkt.Src.Proto, aapkt.Src.Hardware)
				log.Printf("AARP: Gleaned that %v -> %v", aapkt.Src.Proto, aapkt.Src.Hardware)

				if !(aapkt.Dst.Proto == a.myAddr.Proto && a.assigned()) {
					continue
				}

				// Hey that's me! Let them know!
				if err := a.heyThatsMe(aapkt.Src); err != nil {
					log.Printf("AARP: Couldn't respond to Request: %v", err)
					continue
				}

			case aarp.ResponseOp:
				log.Printf("AARP: %v is at %v", aapkt.Dst.Proto, aapkt.Dst.Hardware)
				a.addressMappingTable.Learn(aapkt.Dst.Proto, aapkt.Dst.Hardware)

				if aapkt.Dst.Proto != a.myAddr.Proto {
					continue
				}
				if !a.assigned() {
					a.reroll()
				}

			case aarp.ProbeOp:
				log.Printf("AARP: %v probing to see if %v is available", aapkt.Src.Hardware, aapkt.Src.Proto)
				// AMT should not be updated, because the address is tentative

				if aapkt.Dst.Proto != a.myAddr.Proto {
					continue
				}
				if !a.assigned() {
					// Another node is probing for the same address! Unlucky
					a.reroll()
					continue
				}

				if err := a.heyThatsMe(aapkt.Src); err != nil {
					log.Printf("AARP: Couldn't respond to Probe: %v", err)
					continue
				}
			}
		}
	}
}

// Resolve resolves an AppleTalk node address to an Ethernet address.
// If the address is in the cache (AMT) and is still valid, that is used.
// Otherwise, the address is resolved using AARP.
func (a *AARPMachine) Resolve(ctx context.Context, ddpAddr ddp.Addr) (ethernet.Addr, error) {
	result, waitCh := a.lookupOrWait(ddpAddr)
	if waitCh == nil {
		return result, nil
	}

	if err := a.request(ddpAddr); err != nil {
		return ethernet.Addr{}, err
	}

	ticker := time.NewTicker(aarpRequestRetransmit)
	defer ticker.Stop()

	ctx, cancel := context.WithTimeout(ctx, aarpRequestTimeout)
	defer cancel()

	for {
		select {
		case <-ctx.Done():
			return ethernet.Addr{}, ctx.Err()

		case <-waitCh:
			result, waitCh = a.lookupOrWait(ddpAddr)
			if waitCh == nil {
				return result, nil
			}

		case <-ticker.C:
			if err := a.request(ddpAddr); err != nil {
				return ethernet.Addr{}, err
			}
		}
	}
}

func (a *AARPMachine) assigned() bool { return a.probes >= 10 }

// Re-roll a local address
func (a *AARPMachine) reroll() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.cfg.EtherTalk.NetStart != a.cfg.EtherTalk.NetEnd {
		// Pick a new network number at random
		a.myAddr.Proto.Network = rand.N(
			a.cfg.EtherTalk.NetEnd-a.cfg.EtherTalk.NetStart+1,
		) + a.cfg.EtherTalk.NetStart
	}

	// Can't use: 0x00, 0xff, 0xfe, or the existing node number
	newNode := rand.N[ddp.Node](0xfd) + 1
	for newNode != a.myAddr.Proto.Node {
		newNode = rand.N[ddp.Node](0xfd) + 1
	}
	a.myAddr.Proto.Node = newNode
	a.probes = 0
}

// Send an AARP response
func (a *AARPMachine) heyThatsMe(targ aarp.AddrPair) error {
	respFrame, err := ethertalk.AARP(a.myAddr.Hardware, aarp.Response(targ, a.myAddr))
	if err != nil {
		return err
	}
	// Instead of broadcasting the reply, send it to the target specifically
	respFrame.Dst = targ.Hardware
	respFrameRaw, err := ethertalk.Marshal(*respFrame)
	if err != nil {
		return err
	}
	return a.pcapHandle.WritePacketData(respFrameRaw)
}

// Broadcast an AARP Probe
func (a *AARPMachine) probe() error {
	probeFrame, err := ethertalk.AARP(a.myAddr.Hardware, aarp.Probe(a.myAddr.Hardware, a.myAddr.Proto))
	if err != nil {
		return err
	}
	probeFrameRaw, err := ethertalk.Marshal(*probeFrame)
	if err != nil {
		return err
	}
	return a.pcapHandle.WritePacketData(probeFrameRaw)
}

// Broadcast an AARP Request
func (a *AARPMachine) request(ddpAddr ddp.Addr) error {
	reqFrame, err := ethertalk.AARP(a.myAddr.Hardware, aarp.Request(a.myAddr, ddpAddr))
	if err != nil {
		return err
	}
	reqFrameRaw, err := ethertalk.Marshal(*reqFrame)
	if err != nil {
		return err
	}
	return a.pcapHandle.WritePacketData(reqFrameRaw)
}

type amtEntry struct {
	hwAddr  ethernet.Addr
	last    time.Time
	updated chan struct{}
}

// addressMappingTable implements a concurrent-safe Address Mapping Table for
// AppleTalk (DDP) addresses to Ethernet hardware addresses.
type addressMappingTable struct {
	mu    sync.Mutex
	table map[ddp.Addr]*amtEntry
}

// Learn adds or updates an AMT entry.
func (t *addressMappingTable) Learn(ddpAddr ddp.Addr, hwAddr ethernet.Addr) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.table == nil {
		t.table = make(map[ddp.Addr]*amtEntry)
	}
	oldEnt := t.table[ddpAddr]
	if oldEnt == nil {
		t.table[ddpAddr] = &amtEntry{
			hwAddr:  hwAddr,
			last:    time.Now(),
			updated: make(chan struct{}),
		}
		return
	}

	if oldEnt.hwAddr == hwAddr && time.Since(oldEnt.last) < maxAMTEntryAge {
		oldEnt.last = time.Now()
		return
	}
	oldEnt.hwAddr = hwAddr
	oldEnt.last = time.Now()
	close(oldEnt.updated)
	oldEnt.updated = make(chan struct{})
}

// lookupOrWait returns either the valid cached Ethernet address for the given
// DDP address, or a channel that is closed when the entry is updated.
func (t *addressMappingTable) lookupOrWait(ddpAddr ddp.Addr) (ethernet.Addr, <-chan struct{}) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.table == nil {
		t.table = make(map[ddp.Addr]*amtEntry)
	}
	ent, ok := t.table[ddpAddr]
	if ok && time.Since(ent.last) < maxAMTEntryAge {
		return ent.hwAddr, nil
	}
	ch := make(chan struct{})
	t.table[ddpAddr] = &amtEntry{
		updated: ch,
	}
	return ethernet.Addr{}, ch
}
