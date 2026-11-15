package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"sync"

	"github.com/compdani/jackery-monitor/internal/automation"
	"github.com/compdani/jackery-monitor/internal/store"
	"github.com/pocketbase/pocketbase/core"
)

type smartRuntime struct {
	mu       sync.Mutex
	evaluate sync.Mutex
}

func (s *Server) smartConfig(sn string) (automation.SmartSettings, error) {
	raw, err := s.Store.Get("settings", "smart_charge")
	if err != nil && !isMissing(err) {
		return automation.SmartSettings{}, err
	}
	if by, ok := raw["by_device"].(map[string]any); ok {
		raw, _ = by[sn].(map[string]any)
	}
	return automation.NormalizeSmart(raw), nil
}

// runSmart persists intent before issuing any command, then commits the result
// and audit atomically. An unresolved intent prevents subsequent commands after
// restart: the plug's actual state must be reconciled by the future supervisor.
func (s *Server) runSmart(ctx context.Context, sn string, cfg automation.SmartSettings, plan automation.ChargePlan, sw automation.Switch) error {
	s.smart.mu.Lock()
	defer s.smart.mu.Unlock()
	key := "smart_charge_state:" + sn
	raw, err := s.Store.Get("settings", key)
	if err != nil && !isMissing(err) {
		return err
	}
	if raw["pending"] == true {
		return fmt.Errorf("smart-charge command requires reconciliation")
	}
	var state automation.SmartState
	if raw != nil {
		data, err := json.Marshal(raw["state"])
		if err != nil {
			return err
		}
		if err = json.Unmarshal(data, &state); err != nil {
			return err
		}
	}
	if err = s.Store.Put("settings", key, sn, map[string]any{"state": state, "pending": true, "plan": plan}); err != nil {
		return err
	}
	state, decision := automation.RunSmart(ctx, governedSwitch{s, "smart", sw}, cfg, state, sn, plan)
	decision.Narration = s.smartNarration(ctx, cfg, &decision.ChargePlan)
	return s.Store.App.RunInTransaction(func(app core.App) error {
		st := &store.Store{App: app}
		if err := st.Put("automation_decisions", fmt.Sprintf("smart_charge:%s:%d", sn, plan.DecidedAt), sn, decision.Payload()); err != nil {
			return err
		}
		return st.Put("settings", key, sn, map[string]any{"state": state, "pending": false})
	})
}

func (s *Server) smartChargeRoutes(w http.ResponseWriter, r *http.Request, b map[string]any) bool {
	if s.smartLiveRoutes(w, r) {
		return true
	}
	path := r.URL.Path
	if path != "/api/smart_charge/config" {
		return false
	}
	if r.Method != "GET" && r.Method != "POST" {
		fail(w, 405, "method not allowed")
		return true
	}
	sn := r.URL.Query().Get("device_sn")
	if sn == "" {
		device, _ := s.status(r)["device"].(map[string]any)
		sn = str(device, "device_sn")
	}
	s.smart.mu.Lock()
	defer s.smart.mu.Unlock()
	if r.Method == "POST" {
		if sn == "" {
			fail(w, 400, "no active device — pass device_sn explicitly")
			return true
		}
		raw, err := s.Store.Get("settings", "smart_charge")
		if err != nil && !isMissing(err) {
			storageError(w, err)
			return true
		}
		by, _ := raw["by_device"].(map[string]any)
		if by == nil {
			by = map[string]any{}
			if len(raw) > 0 {
				by["__legacy__"] = raw
			}
		}
		by[sn] = automation.NormalizeSmart(b)
		if err = s.Store.Put("settings", "smart_charge", "", map[string]any{"by_device": by}); err != nil {
			storageError(w, err)
			return true
		}
	}
	cfg, err := s.smartConfig(sn)
	if err != nil {
		storageError(w, err)
		return true
	}
	rows, err := s.Store.List("automation_rules", "", 0, 0)
	if err != nil {
		storageError(w, err)
		return true
	}
	conflicts := []map[string]any{}
	for _, row := range rows {
		if row["enabled"] == false || !sameHost(str(row, "kasa_host"), cfg.Host) || cfg.Host == "" {
			continue
		}
		scope := str(row, "jackery_device_sn")
		if scope != "" && scope != sn {
			continue
		}
		conflicts = append(conflicts, row)
	}
	sort.Slice(conflicts, func(i, j int) bool { return str(conflicts[i], "id") < str(conflicts[j], "id") })
	var serial any
	if sn != "" {
		serial = sn
	}
	jsonOut(w, 200, map[string]any{"device_sn": serial, "config": cfg, "conflicting_rules": conflicts})
	return true
}
