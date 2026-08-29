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
