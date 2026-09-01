package router

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSet26HealthEndpointCarriesBuildAndPolicySummary(t *testing.T) {
	rtr := &Router{
		Config: &Config{AURP: AURPConfig{
			HopCountReduction: true,
			HopCountWeight:    2,
			RemapRules:        []AURPRemapRule{{}},
			Clusters:          []AURPClusterRule{{}},
			BackupPeers:       []AURPBackupPeerRule{{}},
		}},
		RouteTable: NewRouteTable(t.Context()),
		AURPPeers: &AURPPeerTable{
			peersByIP:         make(map[[4]byte]*AURPPeer),
			peersByConfigured: make(map[string]*AURPPeer),
			dnsByConfigured:   make(map[string]configuredDNSState),
		},
	}
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	resp := httptest.NewRecorder()
	rtr.HealthHandler(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("health status = %d, want 200", resp.Code)
	}
	var snapshot RouterHealthSnapshot
	if err := json.Unmarshal(resp.Body.Bytes(), &snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.Version == "" || snapshot.Build == "" {
		t.Fatalf("missing provenance: version=%q build=%q", snapshot.Version, snapshot.Build)
	}
	if snapshot.Policies.RemapRules != 1 ||
		snapshot.Policies.Clusters != 1 ||
		snapshot.Policies.BackupPeers != 1 ||
		!snapshot.Policies.HopCountReduction ||
		snapshot.Policies.HopCountWeight != 2 {
		t.Fatalf("unexpected policy summary: %+v", snapshot.Policies)
	}
}

func TestSet26ReadyEndpointFailsWithoutOperationalEtherTalk(t *testing.T) {
	rtr := &Router{
		Config:     &Config{},
		RouteTable: NewRouteTable(t.Context()),
		AURPPeers: &AURPPeerTable{
			peersByIP:         make(map[[4]byte]*AURPPeer),
			peersByConfigured: make(map[string]*AURPPeer),
			dnsByConfigured:   make(map[string]configuredDNSState),
		},
	}
	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	resp := httptest.NewRecorder()
	rtr.ReadyHandler(resp, req)
	if resp.Code != http.StatusServiceUnavailable {
		t.Fatalf("ready status = %d, want 503", resp.Code)
	}
}
