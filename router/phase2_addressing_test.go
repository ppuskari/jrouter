package router

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sfiera/multitalk/pkg/ddp"
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

func TestSet24TrueNonSeedMayDiscoverRangeAndZone(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "jrouter.yaml")
	cfg := `ethertalk:
  device: enp0s3
  seed_mode: none
`
	if err := os.WriteFile(path, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	port := got.EtherTalk[0]
	if port.NetStart != 0 || port.NetEnd != 0 || port.DefaultZoneName != "" {
		t.Fatalf("dynamic non-seed unexpectedly retained configured cable data: %+v", port)
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

func TestSet24DynamicRangeReplacementRemovesOldDirectRoute(t *testing.T) {
	rtr := &Router{
		Logger:     slog.Default(),
		RouteTable: NewRouteTable(context.Background()),
	}
	port := &EtherTalkPort{
		router: rtr,
		device: "test0",
		seed:   newSeedController(SeedModeNone, 30*time.Second, 0, 0, "Petar's Place"),
	}
	port.setCableRange(phase2StartupStart, phase2StartupEnd)

	port.seed.observeRange(1000, 1009, true, "Petar's Place", time.Now())
	if err := port.activateCableRange(1000, 1009); err != nil {
		t.Fatal(err)
	}
	if route := rtr.RouteTable.Lookup(1000); route.Zero() {
		t.Fatal("first learned direct route is not valid")
	}

	port.seed.observeRange(2000, 2009, true, "Petar's Place", time.Now())
	if err := port.activateCableRange(2000, 2009); err != nil {
		t.Fatal(err)
	}
	if route := rtr.RouteTable.find(port, 1000); !route.Zero() {
		t.Fatalf("obsolete direct route remained after range replacement: %v", route)
	}
	if route := rtr.RouteTable.Lookup(2000); route.Zero() {
		t.Fatal("replacement learned direct route is not valid")
	}
}

func TestSet24SoftTakeoverCanUseAlreadyAdoptedRange(t *testing.T) {
	s := newSeedController(SeedModeSoft, 30*time.Second, 1000, 1009, "Petar's Place")
	now := time.Now()
	s.observeRange(1000, 1009, true, "Petar's Place", now)
	if !s.expireExternalAuthority(now.Add(91*time.Second), 90*time.Second) {
		t.Fatal("external authority did not expire")
	}
	if !s.promoteSoft() {
		t.Fatal("soft seed could not take over an already adopted cable range")
	}
	if got := s.snapshot().Effective; got != "soft-seed-active" {
		t.Fatalf("effective state = %q, want soft-seed-active", got)
	}
}

func TestSet24AARPRerollChangesNodeOnSingleNetwork(t *testing.T) {
	a := &AARPMachine{
		rangeStart: 1000,
		rangeEnd:   1000,
	}
	a.myAddr.Proto.Network = 1000
	a.myAddr.Proto.Node = 1
	a.reroll()
	if a.myAddr.Proto.Network != 1000 {
		t.Fatalf("reroll changed single-network range to %d", a.myAddr.Proto.Network)
	}
	if a.myAddr.Proto.Node == 1 {
		t.Fatal("reroll retained colliding node number")
	}
}

func TestSet24RTMPAdvertisesLocalTupleWhenNoOtherRoutesExist(t *testing.T) {
	rtr := &Router{
		Logger:     slog.Default(),
		RouteTable: NewRouteTable(context.Background()),
	}
	port := &EtherTalkPort{
		router: rtr,
		device: "test0",
		myAddr: ddp.Addr{Network: 1000, Node: 1},
	}
	port.setCableRange(1000, 1009)
	if _, err := rtr.RouteTable.UpsertRoute(port, true, 1000, 1009, 0); err != nil {
		t.Fatal(err)
	}
	if err := rtr.RouteTable.AddZonesToNetwork(1000, "Petar's Place"); err != nil {
		t.Fatal(err)
	}

	packets := port.rtmpDataPackets(true)
	if len(packets) != 1 {
		t.Fatalf("RTMP packet count = %d, want 1", len(packets))
	}
	if len(packets[0].NetworkTuples) != 1 {
		t.Fatalf("RTMP tuple count = %d, want 1", len(packets[0].NetworkTuples))
	}
	first := packets[0].NetworkTuples[0]
	if first.RangeStart != 1000 || first.RangeEnd != 1009 || first.Distance != 0 {
		t.Fatalf("first RTMP tuple = %+v, want local 1000-1009 distance 0", first)
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
