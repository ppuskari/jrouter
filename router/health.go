package router

import (
	"encoding/json"
	"net/http"

	"drjosh.dev/jrouter/meta"
)

type AURPPolicySummary struct {
	HiddenNetworks       int   `json:"hidden_networks"`
	HiddenImportNetworks int   `json:"hidden_import_networks"`
	HiddenDevices        int   `json:"hidden_devices"`
	RemapRules           int   `json:"remap_rules"`
	Clusters             int   `json:"clusters"`
	BackupPeers          int   `json:"backup_peers"`
	HopCountReduction    bool  `json:"hop_count_reduction"`
	HopCountWeight       uint8 `json:"hop_count_weight"`
}

type RouterHealthSnapshot struct {
	Status           string                 `json:"status"`
	Ready            bool                   `json:"ready"`
	Version          string                 `json:"version"`
	Build            string                 `json:"build"`
	EtherTalkPorts   int                    `json:"ethertalk_ports"`
	OperationalPorts int                    `json:"operational_ports"`
	Routes           int                    `json:"routes"`
	AURP             aurpOperationalSummary `json:"aurp"`
	Policies         AURPPolicySummary      `json:"policies"`
}

func (rtr *Router) HealthSnapshot() RouterHealthSnapshot {
	snapshot := RouterHealthSnapshot{
		Status:         "ok",
		Version:        meta.Version,
		Build:          meta.Build,
		EtherTalkPorts: len(rtr.Ports),
	}
	if rtr.RouteTable != nil {
		snapshot.Routes = len(rtr.RouteTable.Dump())
	}
	if rtr.AURPPeers != nil {
		snapshot.AURP = rtr.AURPPeers.operationalSummary()
	}
	for _, port := range rtr.Ports {
		if port == nil || port.aarpMachine == nil {
			continue
		}
		if _, _, operational := port.aarpMachine.OperationalRange(); operational {
			snapshot.OperationalPorts++
		}
	}
	if rtr.Config != nil {
		cfg := rtr.Config.AURP
		snapshot.Policies = AURPPolicySummary{
			HiddenNetworks:       len(cfg.HiddenNetworks),
			HiddenImportNetworks: len(cfg.HiddenImportNetworks),
			HiddenDevices:        len(cfg.HiddenDevices),
			RemapRules:           len(cfg.RemapRules),
			Clusters:             len(cfg.Clusters),
			BackupPeers:          len(cfg.BackupPeers),
			HopCountReduction:    cfg.HopCountReduction,
			HopCountWeight:       cfg.HopCountWeight,
		}
	}

	snapshot.Ready = snapshot.EtherTalkPorts > 0 &&
		snapshot.OperationalPorts > 0 &&
		snapshot.AURP.LoopDisabled == 0
	if !snapshot.Ready {
		snapshot.Status = "not-ready"
	}
	return snapshot
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func (rtr *Router) HealthHandler(w http.ResponseWriter, _ *http.Request) {
	snapshot := rtr.HealthSnapshot()
	writeJSON(w, http.StatusOK, snapshot)
}

func (rtr *Router) ReadyHandler(w http.ResponseWriter, _ *http.Request) {
	snapshot := rtr.HealthSnapshot()
	status := http.StatusOK
	if !snapshot.Ready {
		status = http.StatusServiceUnavailable
	}
	writeJSON(w, status, snapshot)
}

func (rtr *Router) AURPSummaryHandler(w http.ResponseWriter, _ *http.Request) {
	snapshot := rtr.HealthSnapshot()
	writeJSON(w, http.StatusOK, struct {
		Version  string                 `json:"version"`
		Build    string                 `json:"build"`
		AURP     aurpOperationalSummary `json:"aurp"`
		Policies AURPPolicySummary      `json:"policies"`
	}{
		Version:  snapshot.Version,
		Build:    snapshot.Build,
		AURP:     snapshot.AURP,
		Policies: snapshot.Policies,
	})
}
