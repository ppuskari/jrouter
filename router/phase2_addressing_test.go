package router

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSet24Phase2StartupRange(t *testing.T) {
	if !isPhase2StartupRange(phase2StartupStart, phase2StartupEnd) {
		t.Fatal("full Phase 2 startup range not recognized")
	}
	if isPhase2StartupRange(1000, 1009) {
		t.Fatal("ordinary cable range misclassified as startup range")
	}
}

func TestSet24NonSeedMayDiscoverRangeWithoutConfiguredRange(t *testing.T) {
	s := newSeedController(SeedModeNone, 30*time.Second, 0, 0, "Petar's Place")
	s.observeRange(1000, 1009, true, "Petar's Place", time.Now())
	st := s.snapshot()
	if st.Conflict {
		t.Fatalf("dynamic non-seed range discovery caused conflict: %+v", st)
	}
	if !st.ExternalAuthority {
		t.Fatal("external authority was not recorded")
	}
	if st.ObservedStart != 1000 || st.ObservedEnd != 1009 {
		t.Fatalf("observed range = %d-%d, want 1000-1009", st.ObservedStart, st.ObservedEnd)
	}
	if st.Effective != "non-seed-external" {
		t.Fatalf("effective state = %q, want non-seed-external", st.Effective)
	}
}

func TestSet24NoneConfigMayOmitCableRange(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "jrouter.yaml")
	cfg := `ethertalk:
  device: enp0s3
  seed_mode: none
  zone_name: Petar's Place
`
	if err := os.WriteFile(path, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if len(got.EtherTalk) != 1 {
		t.Fatalf("EtherTalk count = %d, want 1", len(got.EtherTalk))
	}
	if got.EtherTalk[0].NetStart != 0 || got.EtherTalk[0].NetEnd != 0 {
		t.Fatalf("dynamic non-seed unexpectedly acquired configured range %d-%d",
			got.EtherTalk[0].NetStart, got.EtherTalk[0].NetEnd)
	}
}

func TestSet24SoftStillRequiresFallbackCableRange(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "jrouter.yaml")
	cfg := `ethertalk:
  device: enp0s3
  seed_mode: soft
  zone_name: Petar's Place
`
	if err := os.WriteFile(path, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(path); err == nil {
		t.Fatal("soft seed without fallback cable range unexpectedly validated")
	}
}

func TestSet24NumericSoftSeedDelayIsSeconds(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "jrouter.yaml")
	cfg := `ethertalk:
  device: enp0s3
  seed_mode: soft
  soft_seed_delay: 15
  zone_name: Petar's Place
  net_start: 1000
  net_end: 1009
`
	if err := os.WriteFile(path, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if gotDelay := time.Duration(got.EtherTalk[0].SoftSeedDelay); gotDelay != 15*time.Second {
		t.Fatalf("soft seed delay = %v, want 15s", gotDelay)
	}
}

func TestSet24CableRangeAtomics(t *testing.T) {
	port := new(EtherTalkPort)
	port.setCableRange(1000, 1009)
	start, end := port.cableRange()
	if start != 1000 || end != 1009 {
		t.Fatalf("cable range = %d-%d, want 1000-1009", start, end)
	}
}
