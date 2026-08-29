package router

import (
	"context"
	"fmt"
	"sync"
	"time"

	"drjosh.dev/jrouter/atalk"
	"drjosh.dev/jrouter/atalk/zip"
	"github.com/sfiera/multitalk/pkg/ddp"
)

type seedStatus struct {
	Mode             SeedMode
	Effective        string
	ConfiguredStart  ddp.Network
	ConfiguredEnd    ddp.Network
	ObservedStart    ddp.Network
	ObservedEnd      ddp.Network
	ExternalAuthority bool
	Conflict         bool
	LastObservation  time.Time
}

type seedController struct {
	mu sync.RWMutex

	mode  SeedMode
	delay time.Duration

	configuredStart ddp.Network
	configuredEnd   ddp.Network

	active            bool
	externalAuthority bool
	conflict          bool
	observedStart     ddp.Network
	observedEnd       ddp.Network
	lastObservation   time.Time
}

func newSeedController(
	mode SeedMode,
	delay time.Duration,
	start ddp.Network,
	end ddp.Network,
) *seedController {
	s := &seedController{
		mode:            mode,
		delay:           delay,
		configuredStart: start,
		configuredEnd:   end,
	}
	s.active = mode == SeedModeHard
	return s
}

func (s *seedController) observeRange(
	start ddp.Network,
	end ddp.Network,
	authority bool,
	now time.Time,
) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.observedStart = start
	s.observedEnd = end
	s.lastObservation = now
	if authority {
		s.externalAuthority = true
	}

	if start != s.configuredStart || end != s.configuredEnd {
		s.conflict = true
		s.active = false
		return
	}

	if authority && s.mode == SeedModeSoft {
		// A soft seed yields to an established cable authority.
		s.active = false
	}
}

func (s *seedController) promoteSoft() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.mode != SeedModeSoft ||
		s.externalAuthority ||
		s.conflict ||
		s.active {
		return false
	}
	s.active = true
	return true
}

func (s *seedController) activeAuthority() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.active && !s.conflict
}

func (s *seedController) snapshot() seedStatus {
	s.mu.RLock()
	defer s.mu.RUnlock()

	effective := "non-seed"
	switch {
	case s.conflict:
		effective = "conflict-fail-closed"
	case s.active && s.mode == SeedModeHard:
		effective = "hard-seed"
	case s.active && s.mode == SeedModeSoft:
		effective = "soft-seed-active"
	case s.mode == SeedModeSoft && s.externalAuthority:
		effective = "soft-standby"
	case s.mode == SeedModeSoft:
		effective = "soft-waiting"
	case s.mode == SeedModeNone && s.externalAuthority:
		effective = "non-seed-external"
	}

	return seedStatus{
		Mode:              s.mode,
		Effective:         effective,
		ConfiguredStart:   s.configuredStart,
		ConfiguredEnd:     s.configuredEnd,
		ObservedStart:     s.observedStart,
		ObservedEnd:       s.observedEnd,
		ExternalAuthority: s.externalAuthority,
		Conflict:          s.conflict,
		LastObservation:   s.lastObservation,
	}
}

func (port *EtherTalkPort) seedAuthorityActive() bool {
	return port.seed != nil && port.seed.activeAuthority()
}

func (port *EtherTalkPort) observeCableRange(
	start ddp.Network,
	end ddp.Network,
	source string,
	authority bool,
) {
	if port.seed == nil {
		return
	}
	before := port.seed.snapshot()
	port.seed.observeRange(start, end, authority, time.Now())
	after := port.seed.snapshot()

	if !before.Conflict && after.Conflict {
		port.logger.Error(
			"AppleTalk seed configuration conflict; local seed authority disabled",
			"source", source,
			"configured-range", fmt.Sprintf("%d-%d", after.ConfiguredStart, after.ConfiguredEnd),
			"observed-range", fmt.Sprintf("%d-%d", after.ObservedStart, after.ObservedEnd),
			"seed-mode", after.Mode,
		)
		return
	}
	if authority && !before.ExternalAuthority && after.ExternalAuthority {
		port.logger.Info(
			"AppleTalk cable authority observed",
			"source", source,
			"range", fmt.Sprintf("%d-%d", start, end),
			"seed-mode", after.Mode,
			"effective", after.Effective,
		)
	}
}

func (port *EtherTalkPort) activateConfiguredZones() error {
	if err := port.router.RouteTable.AddZonesToNetwork(
		port.netStart,
		port.availableZones.ToSlice()...,
	); err != nil {
		return err
	}
	return nil
}

func (port *EtherTalkPort) sendSeedDiscovery() error {
	if port.seed == nil {
		return nil
	}
	myAddr, ok := port.aarpMachine.Address()
	if !ok {
		return nil
	}
	port.myAddr = myAddr.Proto

	body, err := (&zip.GetNetInfoPacket{
		ZoneName: port.defaultZoneName,
	}).Marshal()
	if err != nil {
		return err
	}
	pkt := &ddp.ExtPacket{
		ExtHeader: ddp.ExtHeader{
			Size:      uint16(len(body)) + atalk.DDPExtHeaderSize,
			Cksum:     0,
			DstNet:    0,
			DstNode:   0xff,
			DstSocket: 6,
			SrcNet:    port.myAddr.Network,
			SrcNode:   port.myAddr.Node,
			SrcSocket: 6,
			Proto:     ddp.ProtoZIP,
		},
		Data: body,
	}
	return port.Broadcast(pkt)
}

// RunSeedState coordinates per-interface hard, soft, and non-seed behavior.
//
// hard: configured cable/zone authority is active immediately.
// soft: actively probes for an existing ZIP cable authority, then promotes
//       only when none is observed during SoftSeedDelay.
// none: never becomes a cable/zone authority; a discovery probe is still sent
//       so status/conflict checks can validate the configured expectation.
func (port *EtherTalkPort) RunSeedState(ctx context.Context) error {
	if port.seed == nil {
		return nil
	}

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-port.aarpMachine.Assigned():
	}

	if err := port.sendSeedDiscovery(); err != nil {
		port.logger.Warn("AppleTalk seed discovery probe failed", "error", err)
	}

	if port.seed.mode != SeedModeSoft {
		return nil
	}

	deadline := time.NewTimer(port.seed.delay)
	defer deadline.Stop()
	probe := time.NewTicker(5 * time.Second)
	defer probe.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-probe.C:
			if port.seed.snapshot().ExternalAuthority ||
				port.seed.snapshot().Conflict {
				return nil
			}
			if err := port.sendSeedDiscovery(); err != nil {
				port.logger.Warn("AppleTalk soft-seed discovery probe failed", "error", err)
			}
		case <-deadline.C:
			if !port.seed.promoteSoft() {
				return nil
			}
			if err := port.activateConfiguredZones(); err != nil {
				return err
			}
			port.logger.Warn(
				"AppleTalk soft seed promoted after discovery interval",
				"delay", port.seed.delay,
				"range", fmt.Sprintf("%d-%d", port.netStart, port.netEnd),
				"default-zone", port.defaultZoneName,
			)
			return nil
		}
	}
}
