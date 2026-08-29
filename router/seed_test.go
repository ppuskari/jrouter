package router

import (
	"testing"
	"time"

	"github.com/sfiera/multitalk/pkg/ddp"
)

func TestSeedControllerHardStartsActive(t *testing.T) {
	s := newSeedController(SeedModeHard, 30*time.Second, 1000, 1009)
	if !s.activeAuthority() {
		t.Fatal("hard seed did not start active")
	}
	if got := s.snapshot().Effective; got != "hard-seed" {
		t.Fatalf("effective state = %q, want hard-seed", got)
	}
}

func TestSeedControllerNoneNeverPromotes(t *testing.T) {
	s := newSeedController(SeedModeNone, 30*time.Second, 1000, 1009)
	if s.activeAuthority() {
		t.Fatal("non-seed unexpectedly active")
	}
	if s.promoteSoft() {
		t.Fatal("non-seed promoted through soft promotion path")
	}
}

func TestSeedControllerSoftPromotesWithoutAuthority(t *testing.T) {
	s := newSeedController(SeedModeSoft, 30*time.Second, 1000, 1009)
	if s.activeAuthority() {
		t.Fatal("soft seed started active")
	}
	if !s.promoteSoft() {
		t.Fatal("soft seed did not promote when no authority was observed")
	}
	if !s.activeAuthority() {
		t.Fatal("soft seed not active after promotion")
	}
}

func TestSeedControllerSoftYieldsToExternalAuthority(t *testing.T) {
	s := newSeedController(SeedModeSoft, 30*time.Second, 1000, 1009)
	s.observeRange(1000, 1009, true, time.Now())
	if s.promoteSoft() {
		t.Fatal("soft seed promoted despite external authority")
	}
	st := s.snapshot()
	if st.Effective != "soft-standby" || !st.ExternalAuthority {
		t.Fatalf("state = %+v, want soft standby with external authority", st)
	}
}

func TestSeedControllerConflictFailsClosed(t *testing.T) {
	for _, mode := range []SeedMode{SeedModeHard, SeedModeSoft, SeedModeNone} {
		t.Run(string(mode), func(t *testing.T) {
			s := newSeedController(mode, 30*time.Second, 1000, 1009)
			s.observeRange(2000, 2009, true, time.Now())
			if s.activeAuthority() {
				t.Fatal("conflicting cable range left seed authority active")
			}
			st := s.snapshot()
			if !st.Conflict || st.Effective != "conflict-fail-closed" {
				t.Fatalf("state = %+v, want conflict fail closed", st)
			}
		})
	}
}

func TestSeedControllerRTMPObservationDoesNotSuppressSoftPromotion(t *testing.T) {
	s := newSeedController(SeedModeSoft, 30*time.Second, 1000, 1009)
	s.observeRange(ddp.Network(1000), ddp.Network(1009), false, time.Now())
	if !s.promoteSoft() {
		t.Fatal("ordinary RTMP range observation was treated as ZIP seed authority")
	}
}


func TestSeedControllerSoftAuthorityExpiryAllowsTakeover(t *testing.T) {
	s := newSeedController(SeedModeSoft, 30*time.Second, 1000, 1009)
	now := time.Now()
	s.observeRange(1000, 1009, true, now)
	if !s.snapshot().ExternalAuthority {
		t.Fatal("external authority was not recorded")
	}
	if s.expireExternalAuthority(now.Add(89*time.Second), 90*time.Second) {
		t.Fatal("authority expired before timeout")
	}
	if !s.expireExternalAuthority(now.Add(91*time.Second), 90*time.Second) {
		t.Fatal("authority did not expire after timeout")
	}
	if !s.promoteSoft() {
		t.Fatal("soft seed could not promote after authority loss")
	}
}

func TestSeedControllerMatchingAuthorityRefreshesLease(t *testing.T) {
	s := newSeedController(SeedModeSoft, 30*time.Second, 1000, 1009)
	now := time.Now()
	s.observeRange(1000, 1009, true, now)
	s.observeRange(1000, 1009, true, now.Add(60*time.Second))
	if s.expireExternalAuthority(now.Add(120*time.Second), 90*time.Second) {
		t.Fatal("refreshed authority lease expired too early")
	}
}
