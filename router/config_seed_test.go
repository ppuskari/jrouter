package router

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadConfigDefaultsSeedModeHard(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "jrouter.yaml")
	data := []byte(`ethertalk:
  - device: en0
    zone_name: Test
    net_start: 100
    net_end: 100
`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.EtherTalk[0].SeedMode; got != SeedModeHard {
		t.Fatalf("seed mode = %q, want hard", got)
	}
	if got := cfg.EtherTalk[0].SoftSeedDelay; got != 30*time.Second {
		t.Fatalf("soft seed delay = %v, want 30s", got)
	}
}

func TestLoadConfigAcceptsSoftAndNoneSeedModes(t *testing.T) {
	for _, tc := range []struct {
		name string
		mode SeedMode
	}{
		{name: "soft", mode: SeedModeSoft},
		{name: "none", mode: SeedModeNone},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "jrouter.yaml")
			data := []byte("ethertalk:\n" +
				"  - device: en0\n" +
				"    zone_name: Test\n" +
				"    net_start: 100\n" +
				"    net_end: 100\n" +
				"    seed_mode: " + tc.name + "\n" +
				"    soft_seed_delay: 45s\n")
			if err := os.WriteFile(path, data, 0o600); err != nil {
				t.Fatal(err)
			}
			cfg, err := LoadConfig(path)
			if err != nil {
				t.Fatal(err)
			}
			if got := cfg.EtherTalk[0].SeedMode; got != tc.mode {
				t.Fatalf("seed mode = %q, want %q", got, tc.mode)
			}
			if got := cfg.EtherTalk[0].SoftSeedDelay; got != 45*time.Second {
				t.Fatalf("soft seed delay = %v, want 45s", got)
			}
		})
	}
}

func TestLoadConfigRejectsUnknownSeedMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "jrouter.yaml")
	data := []byte(`ethertalk:
  - device: en0
    zone_name: Test
    net_start: 100
    net_end: 100
    seed_mode: surprise
`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(path); err == nil {
		t.Fatal("unknown seed mode was accepted")
	}
}

func TestLoadConfigDefaultsAURPTiming(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "jrouter.yaml")
	data := []byte("ethertalk:\n  - device: en0\n    zone_name: Test\n    net_start: 100\n    net_end: 100\n")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AURP.LastHeardFromTimeout != 90*time.Second ||
		cfg.AURP.RetryInterval != 10*time.Second ||
		cfg.AURP.SendRetryLimit != 5 ||
		cfg.AURP.TickleRetryLimit != 10 ||
		cfg.AURP.ZoneInfoRetryInterval != 10*time.Second {
		t.Fatalf("unexpected AURP defaults: %+v", cfg.AURP)
	}
}

func TestLoadConfigAcceptsAURPTimingOverrides(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "jrouter.yaml")
	data := []byte(`aurp:
  last_heard_from_timeout: 45s
  retry_interval: 3s
  send_retry_limit: 7
  tickle_retry_limit: 12
  zone_info_retry_interval: 4s
ethertalk:
  - device: en0
    zone_name: Test
    net_start: 100
    net_end: 100
`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AURP.LastHeardFromTimeout != 45*time.Second ||
		cfg.AURP.RetryInterval != 3*time.Second ||
		cfg.AURP.SendRetryLimit != 7 ||
		cfg.AURP.TickleRetryLimit != 12 ||
		cfg.AURP.ZoneInfoRetryInterval != 4*time.Second {
		t.Fatalf("unexpected AURP overrides: %+v", cfg.AURP)
	}
}

func TestLoadConfigRejectsAURPLHFTBelowRFCMinimum(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "jrouter.yaml")
	data := []byte(`aurp:
  last_heard_from_timeout: 29s
ethertalk:
  - device: en0
    zone_name: Test
    net_start: 100
    net_end: 100
`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(path); err == nil {
		t.Fatal("AURP LHFT below 30 seconds was accepted")
	}
}

func TestLoadConfigParsesAURPHiddenNetworks(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "jrouter.yaml")
	data := []byte(`aurp:
  hidden_networks:
    - 1000
    - 2000-2009
ethertalk:
  - device: en0
    zone_name: Test
    net_start: 100
    net_end: 100
`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.AURP.networkHidden(1000) ||
		!cfg.AURP.networkHidden(2005) ||
		cfg.AURP.networkHidden(2010) {
		t.Fatalf("hidden network policy parsed incorrectly: %+v", cfg.AURP.HiddenNetworks)
	}
}

func TestLoadConfigRejectsInvalidAURPHiddenRange(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "jrouter.yaml")
	data := []byte(`aurp:
  hidden_networks:
    - 2009-2000
ethertalk:
  - device: en0
    zone_name: Test
    net_start: 100
    net_end: 100
`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(path); err == nil {
		t.Fatal("reversed AURP hidden network range was accepted")
	}
}

func TestLoadConfigAcceptsHopCountReductionWithLoopProbeEnforcement(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "jrouter.yaml")
	data := []byte(`aurp:
  hop_count_reduction: true
ethertalk:
  - device: en0
    zone_name: Test
    net_start: 100
    net_end: 100
`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.AURP.HopCountReduction {
		t.Fatal("hop-count reduction config was not enabled")
	}
}

func TestLoadConfigAcceptsAURPHopCountWeight(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "jrouter.yaml")
	data := []byte(`aurp:
  hop_count_weight: 3
ethertalk:
  - device: en0
    zone_name: Test
    net_start: 100
    net_end: 100
`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AURP.HopCountWeight != 3 {
		t.Fatalf("hop-count weight = %d, want 3", cfg.AURP.HopCountWeight)
	}
}

func TestLoadConfigAcceptsStaticAURPRemap(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "jrouter.yaml")
	data := []byte(`aurp:
  remap:
    - peer: cfg:remote.example
      remote_start: 100
      remote_end: 109
      local_start: 5000
      local_end: 5009
ethertalk:
  - device: en0
    zone_name: Test
    net_start: 1000
    net_end: 1009
`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.AURP.RemapRules) != 1 {
		t.Fatalf("remap rules = %d, want 1", len(cfg.AURP.RemapRules))
	}
}

func TestLoadConfigRejectsOverlappingStaticAURPRemap(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "jrouter.yaml")
	data := []byte(`aurp:
  remap:
    - remote_start: 100
      remote_end: 109
      local_start: 1000
      local_end: 1009
ethertalk:
  - device: en0
    zone_name: Test
    net_start: 1000
    net_end: 1009
`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(path); err == nil {
		t.Fatal("remap range overlapping local EtherTalk range was accepted")
	}
}

func TestLoadConfigRejectsInvalidDeviceHideDirection(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "jrouter.yaml")
	data := []byte(`aurp:
  hidden_devices:
    - type: LaserWriter
      direction: sideways
ethertalk:
  - device: en0
    zone_name: Test
    net_start: 100
    net_end: 100
`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(path); err == nil {
		t.Fatal("invalid device-hiding direction was accepted")
	}
}

func TestLoadConfigAcceptsStaticAURPCluster(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "jrouter.yaml")
	data := []byte(`aurp:
  remap:
    - remote_start: 100
      remote_end: 109
      local_start: 5000
      local_end: 5009
  clusters:
    - start: 5000
      end: 5009
ethertalk:
  - device: en0
    zone_name: Test
    net_start: 1000
    net_end: 1009
`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.AURP.Clusters) != 1 {
		t.Fatalf("clusters = %d, want 1", len(cfg.AURP.Clusters))
	}
}

func TestLoadConfigAcceptsBackupPeerPolicy(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "jrouter.yaml")
	data := []byte(`aurp:
  backup_peers:
    - peer: cfg:backup.example
      penalty: 6
ethertalk:
  - device: en0
    zone_name: Test
    net_start: 100
    net_end: 100
`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.AURP.BackupPeers) != 1 ||
		cfg.AURP.BackupPeers[0].Penalty != 6 {
		t.Fatalf("backup policy = %+v, want one penalty-6 peer", cfg.AURP.BackupPeers)
	}
}
