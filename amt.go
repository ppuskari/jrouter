package main

import (
	"sync"
	"time"

	"github.com/sfiera/multitalk/pkg/ddp"
	"github.com/sfiera/multitalk/pkg/ethernet"
)

// TODO: verify this parameter
const maxAMTEntryAge = 30 * time.Second

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
