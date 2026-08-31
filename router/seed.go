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
	Mode                     SeedMode
	Effective                string
	ConfiguredStart          ddp.Network
	ConfiguredEnd            ddp.Network
	ConfiguredZone           string
	ObservedStart            ddp.Network
	ObservedEnd              ddp.Network
	ObservedZone             string
	ExternalAuthority        bool
	Conflict                 bool
	LastObservation          time.Time
	LastAuthorityObservation time.Time
}

type seedController struct {
	mu sync.RWMutex

	mode  SeedMode
	delay time.Duration

	configuredStart ddp.Network
	configuredEnd   ddp.Network
	configuredZone  string

	active                   bool
	externalAuthority        bool
	conflict                 bool
	observedStart            ddp.Network
	observedEnd              ddp.Network
	observedZone             string
	lastObservation          time.Time
	lastAuthorityObservation time.Time
}

func newSeedController(
	mode SeedMode,
	delay time.Duration,
	start ddp.Network,
	end ddp.Network,
	zone string,
) *seedController {
	s := &seedController{
		mode:            mode,
		delay:           delay,
		configuredStart: start,
		configuredEnd:   end,
		configuredZone:  zone,
	}
	s.active = mode == SeedModeHard
	return s
}

func (s *seedController) observeRange(
	start ddp.Network,
	end ddp.Network,
	authority bool,
	zone string,
	now time.Time,
) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.observedStart = start
	s.observedEnd = end
	if zone != "" {
		s.observedZone = zone
	}
	s.lastObservation = now
	if authority {
		s.externalAuthority = true
		s.lastAuthorityObservation = now
	}

	hasConfiguredRange := s.configuredStart != 0 && s.configuredEnd != 0
	if (hasConfiguredRange &&
		(start != s.configuredStart || end != s.configuredEnd)) ||
		(zone != "" && s.configuredZone != "" && zone != s.configuredZone) {
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

func (s *seedController) expireExternalAuthority(now time.Time, timeout time.Duration) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.externalAuthority || s.lastAuthorityObservation.IsZero() {
		return false
	}
	if now.Sub(s.lastAuthorityObservation) <= timeout {
		return false
	}
	s.externalAuthority = false
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
		Mode:                     s.mode,
		Effective:                effective,
		ConfiguredStart:          s.configuredStart,
		ConfiguredEnd:            s.configuredEnd,
		ConfiguredZone:           s.configuredZone,
		ObservedStart:            s.observedStart,
		ObservedEnd:              s.observedEnd,
		ObservedZone:             s.observedZone,
		ExternalAuthority:        s.externalAuthority,
		Conflict:                 s.conflict,
		LastObservation:          s.lastObservation,
		LastAuthorityObservation: s.lastAuthorityObservation,
	}
}

func (port *EtherTalkPort) seedAuthorityActive() bool {
	return port.seed != nil && port.seed.activeAuthority()
}

func (port *EtherTalkPort) seedEffectiveState() string {
	if port.seed == nil {
		return "unconfigured"
	}
	return port.seed.snapshot().Effective
}

func (port *EtherTalkPort) observeCableRange(
	start ddp.Network,
	end ddp.Network,
	source string,
	authority bool,
) {
	port.observeCableConfig(start, end, "", source, authority)
}

func (port *EtherTalkPort) observeCableConfig(
	start ddp.Network,
	end ddp.Network,
	zone string,
	source string,
	authority bool,
) {
	if port.seed == nil {
		return
	}
	before := port.seed.snapshot()
	port.seed.observeRange(start, end, authority, zone, time.Now())
	after := port.seed.snapshot()

	if !before.Conflict && after.Conflict {
		port.logger.Error(
			"AppleTalk seed configuration conflict; local seed authority disabled",
			"source", source,
			"configured-range", fmt.Sprintf("%d-%d", after.ConfiguredStart, after.ConfiguredEnd),
			"observed-range", fmt.Sprintf("%d-%d", after.ObservedStart, after.ObservedEnd),
			"configured-zone", after.ConfiguredZone,
			"observed-zone", after.ObservedZone,
			"seed-mode", after.Mode,
		)
		return
	}
	if authority && !before.ExternalAuthority && after.ExternalAuthority {
		port.logger.Info(
			"AppleTalk cable authority observed",
			"source", source,
			"range", fmt.Sprintf("%d-%d", start, end),
			"zone", zone,
			"seed-mode", after.Mode,
			"effective", after.Effective,
		)
	}
}

func (port *EtherTalkPort) activateConfiguredZones() error {
	start, _ := port.cableRange()
	if start == 0 || start >= phase2StartupStart {
		return nil
	}
	if err := port.router.RouteTable.AddZonesToNetwork(
		start,
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
//
//	only when none is observed during SoftSeedDelay.
//
// none: never becomes a cable/zone authority; a discovery probe is still sent
//
//	so status/conflict checks can validate the configured expectation.
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

	if port.seed.mode == SeedModeHard {
		return nil
	}

	probe := time.NewTicker(5 * time.Second)
	defer probe.Stop()

	promotionEligibleAt := time.Now().Add(port.seed.delay)
	authorityTimeout := max(3*port.seed.delay, 90*time.Second)
	promotionRequested := false
	operationalHandled := false

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()

		case <-probe.C:
			now := time.Now()
			if err := port.sendSeedDiscovery(); err != nil {
				port.logger.Warn("AppleTalk seed discovery probe failed", "error", err)
			}

			if port.seed.mode == SeedModeSoft &&
				port.seed.expireExternalAuthority(now, authorityTimeout) {
				promotionEligibleAt = now.Add(port.seed.delay)
				port.logger.Warn(
					"AppleTalk external seed authority expired; entering soft-seed takeover delay",
					"authority-timeout", authorityTimeout,
					"takeover-delay", port.seed.delay,
				)
			}

			state := port.seed.snapshot()
			if state.Conflict {
				continue
			}

			// A valid external ZIP authority gives soft/none ports their real
			// Phase 2 cable range. AARP performs the final address selection in
			// that range before we publish the direct route.
			if state.ExternalAuthority &&
				state.ObservedStart != 0 &&
				state.ObservedEnd != 0 {
				port.aarpMachine.Rebind(state.ObservedStart, state.ObservedEnd)
				promotionRequested = false
				promotionEligibleAt = time.Time{}
			}

			if !operationalHandled {
				select {
				case <-port.aarpMachine.Operational():
					state = port.seed.snapshot()
					var start, end ddp.Network
					switch {
					case state.ExternalAuthority:
						start, end = state.ObservedStart, state.ObservedEnd
					case port.seed.mode == SeedModeSoft && promotionRequested:
						start, end = state.ConfiguredStart, state.ConfiguredEnd
					default:
						continue
					}
					if err := port.activateCableRange(start, end); err != nil {
						return err
					}
					if port.seed.mode == SeedModeSoft &&
						promotionRequested &&
						!state.ExternalAuthority {
						if port.seed.promoteSoft() {
							if err := port.activateConfiguredZones(); err != nil {
								return err
							}
							port.logger.Warn(
								"AppleTalk soft seed promoted after discovery interval",
								"delay", port.seed.delay,
								"range", fmt.Sprintf("%d-%d", start, end),
								"default-zone", port.defaultZoneName,
							)
						}
					}
					operationalHandled = true
				default:
				}
			}

			state = port.seed.snapshot()
			if port.seed.mode != SeedModeSoft {
				continue
			}
			if state.ExternalAuthority {
				promotionEligibleAt = time.Time{}
				promotionRequested = false
				continue
			}
			if state.Effective == "soft-seed-active" {
				continue
			}
			if promotionRequested {
				continue
			}
			if promotionEligibleAt.IsZero() {
				promotionEligibleAt = now.Add(port.seed.delay)
				continue
			}
			if now.Before(promotionEligibleAt) {
				continue
			}

			// No external seed answered. Move out of the startup range first;
			// seed authority becomes active only after AARP owns the configured
			// cable-range address.
			if state.ConfiguredStart == 0 || state.ConfiguredEnd == 0 {
				continue
			}
			port.aarpMachine.Rebind(state.ConfiguredStart, state.ConfiguredEnd)
			promotionRequested = true
			promotionEligibleAt = time.Time{}
		}
	}
}

