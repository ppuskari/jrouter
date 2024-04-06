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

// TODO: verify this parameter
const maxAMTEntryAge = 30 * time.Second

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

	myHWAddr  ethernet.Addr
	myDDPAddr ddp.Addr
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
	a.myDDPAddr = ddp.Addr{
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

				if aapkt.Dst.Proto != a.myDDPAddr {
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

				if aapkt.Dst.Proto != a.myDDPAddr {
					continue
				}
				if a.state == aarpStateProbing {
					a.reroll()
				}

			case aarp.ProbeOp:
				log.Printf("AARP: %v probing to see if %v is available", aapkt.Src.Hardware, aapkt.Src.Proto)
				// AMT should not be updated, because the address is tentative

				if aapkt.Dst.Proto != a.myDDPAddr {
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

// Re-roll a local address
func (a *AARPMachine) reroll() {
	if a.cfg.EtherTalk.NetStart != a.cfg.EtherTalk.NetEnd {
		// Pick a new network number at random
		a.myDDPAddr.Network = rand.N[ddp.Network](
			a.cfg.EtherTalk.NetEnd-a.cfg.EtherTalk.NetStart+1,
		) + a.cfg.EtherTalk.NetStart
	}

	// Can't use: 0x00, 0xff, 0xfe, or the existing node number
	newNode := rand.N[ddp.Node](0xfd) + 1
	for newNode != a.myDDPAddr.Node {
		newNode = rand.N[ddp.Node](0xfd) + 1
	}
	a.myDDPAddr.Node = newNode
	a.probes = 0
}

// Send an AARP response
func (a *AARPMachine) heyThatsMe(targ aarp.AddrPair) error {
	respFrame, err := ethertalk.AARP(a.myHWAddr, aarp.Response(targ, aarp.AddrPair{
		Proto:    a.myDDPAddr,
		Hardware: a.myHWAddr,
	}))
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
	probeFrame, err := ethertalk.AARP(a.myHWAddr, aarp.Probe(a.myHWAddr, a.myDDPAddr))
	if err != nil {
		return err
	}
	probeFrameRaw, err := ethertalk.Marshal(*probeFrame)
	if err != nil {
		return err
	}
	return a.pcapHandle.WritePacketData(probeFrameRaw)
}

type amtEntry struct {
	hwAddr ethernet.Addr
	last   time.Time
}

// AMT implements a concurrent-safe Address Mapping Table for AppleTalk (DDP)
// addresses to Ethernet hardware addresses.
type AMT struct {
	mu    sync.RWMutex
	table map[ddp.Addr]amtEntry
}

// Learn adds or updates an AMT entry.
func (t *AMT) Learn(ddpAddr ddp.Addr, hwAddr ethernet.Addr) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.table == nil {
		t.table = make(map[ddp.Addr]amtEntry)
	}
	t.table[ddpAddr] = amtEntry{
		hwAddr: hwAddr,
		last:   time.Now(),
	}
}

// Lookup searches for a non-expired entry in the table only. It does not send
// any packets.
func (t *AMT) Lookup(ddpAddr ddp.Addr) (ethernet.Addr, bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	ent, ok := t.table[ddpAddr]
	return ent.hwAddr, ok && time.Since(ent.last) < maxAMTEntryAge
}
