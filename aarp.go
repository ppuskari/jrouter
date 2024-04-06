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
	*AMT

	cfg        *config
	pcapHandle *pcap.Handle

	state  aarpState
	probes int

	myAddr aarp.AddrPair
}

type aarpState int

const (
	aarpStateProbing aarpState = iota
	aarpStateAssigned
)

func (a *AARPMachine) Run(ctx context.Context, incomingCh <-chan *ethertalk.Packet) error {
	ticker := time.NewTicker(200 * time.Millisecond) // 200ms is the AARP probe retransmit
	defer ticker.Stop()

	a.state = aarpStateProbing
	a.probes = 0

	// Initialise our DDP address with a preferred address (first network.1)
	a.myAddr.Proto = ddp.Addr{
		Network: ddp.Network(a.cfg.EtherTalk.NetStart),
		Node:    1,
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()

		case <-ticker.C:
			switch a.state {
			case aarpStateAssigned:
				// No need to keep the ticker running if assigned
				ticker.Stop()

			case aarpStateProbing:
				if a.probes >= 10 {
					a.state = aarpStateAssigned
					continue
				}
				a.probes++
				if err := a.probe(); err != nil {
					log.Printf("Couldn't broadcast a Probe: %v", err)
					continue
				}
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
				a.AMT.Learn(aapkt.Src.Proto, aapkt.Src.Hardware)
				log.Printf("AARP: Gleaned that %v -> %v", aapkt.Src.Proto, aapkt.Src.Hardware)

				if aapkt.Dst.Proto != a.myAddr.Proto {
					continue
				}
				if a.state != aarpStateAssigned {
					continue
				}
				// Hey that's me! Let them know!
				if err := a.heyThatsMe(aapkt.Src); err != nil {
					log.Printf("AARP: Couldn't respond to Request: %v", err)
					continue
				}

			case aarp.ResponseOp:
				log.Printf("AARP: %v is at %v", aapkt.Dst.Proto, aapkt.Dst.Hardware)
				a.AMT.Learn(aapkt.Dst.Proto, aapkt.Dst.Hardware)

				if aapkt.Dst.Proto != a.myAddr.Proto {
					continue
				}
				if a.state == aarpStateProbing {
					a.reroll()
				}

			case aarp.ProbeOp:
				log.Printf("AARP: %v probing to see if %v is available", aapkt.Src.Hardware, aapkt.Src.Proto)
				// AMT should not be updated, because the address is tentative

				if aapkt.Dst.Proto != a.myAddr.Proto {
					continue
				}
				switch a.state {
				case aarpStateProbing:
					// Another node is probing for the same address! Unlucky
					a.reroll()

				case aarpStateAssigned:
					if err := a.heyThatsMe(aapkt.Src); err != nil {
						log.Printf("AARP: Couldn't respond to Probe: %v", err)
						continue
					}
				}
			}

		}
	}
}

// Resolve resolves an AppleTalk node address to an Ethernet address.
// If the address is in the cache (AMT) and is still valid, that is used.
// Otherwise, the address is resolved using AARP.
func (a *AARPMachine) Resolve(ctx context.Context, ddpAddr ddp.Addr) (ethernet.Addr, error) {
	// try the cache first
	result, ok := a.AMT.Lookup(ddpAddr)
	if ok {
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
		// We might have a result already
		result, ok := a.AMT.Lookup(ddpAddr)
		if ok {
			return result, nil
		}

		select {
		case <-ctx.Done():
			return ethernet.Addr{}, ctx.Err()

		case <-a.AMT.Wait(ddpAddr):
			// Should have a result now.

		case <-ticker.C:
			if err := a.request(ddpAddr); err != nil {
				return ethernet.Addr{}, err
			}
		}
	}
}

// Re-roll a local address
func (a *AARPMachine) reroll() {
	if a.cfg.EtherTalk.NetStart != a.cfg.EtherTalk.NetEnd {
		// Pick a new network number at random
		a.myAddr.Proto.Network = rand.N[ddp.Network](
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
func (a AARPMachine) request(ddpAddr ddp.Addr) error {
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

// AMT implements a concurrent-safe Address Mapping Table for AppleTalk (DDP)
// addresses to Ethernet hardware addresses.
type AMT struct {
	mu    sync.RWMutex
	table map[ddp.Addr]*amtEntry
}

// Learn adds or updates an AMT entry.
func (t *AMT) Learn(ddpAddr ddp.Addr, hwAddr ethernet.Addr) {
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

// Wait returns a channel that is closed when the entry for ddpAddr is updated.
func (t *AMT) Wait(ddpAddr ddp.Addr) <-chan struct{} {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.table == nil {
		t.table = make(map[ddp.Addr]*amtEntry)
	}
	oldEnt := t.table[ddpAddr]
	if oldEnt != nil {
		return oldEnt.updated
	}
	ch := make(chan struct{})
	t.table[ddpAddr] = &amtEntry{
		updated: ch,
	}
	return ch
}

// Lookup searches for a non-expired entry in the table only. It does not send
// any packets.
func (t *AMT) Lookup(ddpAddr ddp.Addr) (ethernet.Addr, bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	ent, ok := t.table[ddpAddr]
	return ent.hwAddr, ok && time.Since(ent.last) < maxAMTEntryAge
}
