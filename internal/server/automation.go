package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/compdani/jackery-monitor/internal/automation"
	"github.com/compdani/jackery-monitor/internal/devices"
	"github.com/compdani/jackery-monitor/internal/kasa"
	"github.com/compdani/jackery-monitor/internal/store"
	"github.com/pocketbase/pocketbase/core"
	"net/http"
	"strings"
	"sync"
	"time"
)

type controllerRuntime struct {
	mu     sync.Mutex
	once   sync.Once
	engine automation.Engine
	client *kasa.Client
}

func (s *Server) initControllers() {
	s.controllers.once.Do(func() {
		client := kasa.NewClient(func() *kasa.Credentials {
			m, e := s.Store.Secret(s.Key, "kasa")
			if e != nil {
				return nil
			}
			return &kasa.Credentials{Email: str(m, "email"), Password: str(m, "password")}
		})
		s.controllers.client = client
		s.controllers.engine.Switch = devices.KasaSwitch{Client: client}
	})
}
func (s *Server) evaluateRules(ctx context.Context, sn string, tele map[string]any) {
	s.controllers.mu.Lock()
	defer s.controllers.mu.Unlock()
	soc, ok := tele["battery_percent"].(float64)
	if !ok {
		return
	}
	s.initControllers()
	rows, e := s.Store.List("automation_rules", "", 0, 1000)
	if e != nil {
		return
	}
	for _, row := range rows {
		b, e := json.Marshal(row)
		if e != nil {
			continue
		}
		var rule automation.Rule
		if json.Unmarshal(b, &rule) != nil {
			continue
		}
		if rule.Validate() != nil {
			continue
		}
		if s.smartOwnsHost(rule.Host) || s.solarOwnsHost(rule.Host) {
			continue
		}
		if !rule.Enabled {
			row["last_state"] = nil
			_ = s.Store.Put("automation_rules", rule.ID, rule.DeviceSN, row)
			continue
		}
		if rule.DeviceSN == "" {
			s.mu.RLock()
			d, _ := s.snapshot["device"].(map[string]any)
			active := str(d, "device_sn")
			s.mu.RUnlock()
			if active != sn {
				continue
			}
		}
		if rule.DeviceSN != "" && rule.DeviceSN != sn {
			continue
		}
		match := rule.Matches(soc)
		if !match {
			row["last_state"] = false
		} else if row["last_state"] != true {
			e := (governedSwitch{s, "rule", s.controllers.engine.Switch}).SetPower(ctx, rule.Host, rule.Action == "on")
			if e != nil {
				row["last_error"] = e.Error()
			} else {
				now := float64(time.Now().Unix())
				row["last_state"], row["last_fired"], row["last_error"] = true, now, nil
				record := map[string]any{"id": store.ID(), "ts": now, "fired_at": now, "rule_id": rule.ID, "rule_name": rule.Name, "action": rule.Action, "kasa_host": rule.Host, "jackery_sn": sn, "soc_at_fire": soc, "trigger": "soc", "operator": rule.Operator, "threshold": rule.Value}
				if err := s.Store.Put("automation_decisions", str(record, "id"), sn, record); err != nil {
					s.Store.App.Logger().Error("persist rule firing", "error", err)
				}
			}
		}
		if err := s.Store.Put("automation_rules", rule.ID, rule.DeviceSN, row); err != nil {
			s.Store.App.Logger().Error("persist rule edge", "error", err)
		}
	}
}
func (s *Server) automationRoutes(w http.ResponseWriter, r *http.Request, b map[string]any) bool {
	if r.URL.Path == "/api/kasa/credentials" {
		switch r.Method {
		case "GET":
			m, e := s.Store.Secret(s.Key, "kasa")
			if e != nil && !errors.Is(e, sql.ErrNoRows) {
				storageError(w, e)
			} else {
				jsonOut(w, 200, map[string]any{"email": str(m, "email"), "has_password": str(m, "password") != ""})
			}
		case "POST":
			m, e := s.Store.Secret(s.Key, "kasa")
			if e != nil && !errors.Is(e, sql.ErrNoRows) {
				storageError(w, e)
				return true
			}
			if m == nil {
				m = map[string]any{}
			}
			if email := str(b, "email"); email != "" {
				m["email"] = strings.TrimSpace(email)
			}
			if password := str(b, "password"); password != "" {
				m["password"] = password
			}
			if str(m, "email") == "" || str(m, "password") == "" {
				fail(w, 400, "email and password required")
			} else if e = s.Store.SaveSecret(s.Key, "kasa", m); e != nil {
				storageError(w, e)
			} else {
				jsonOut(w, 200, map[string]any{"ok": true})
			}
		case "DELETE":
			e := s.Store.Delete("credentials", "kasa")
			if e != nil && !errors.Is(e, sql.ErrNoRows) {
				storageError(w, e)
			} else {
				jsonOut(w, 200, map[string]any{"ok": true})
			}
		default:
			fail(w, 405, "method not allowed")
		}
		return true
	}
	if (r.URL.Path == "/api/kasa/test" && r.Method == "POST") || (r.URL.Path == "/api/kasa/status" && r.Method == "GET") {
		host := str(b, "host")
		if host == "" {
			host = r.URL.Query().Get("host")
		}
		if host == "" {
			fail(w, 400, "host required")
			return true
		}
		s.initControllers()
		info, e := s.controllers.client.Status(r.Context(), host)
		if e != nil {
			fail(w, 502, e.Error())
		} else {
			jsonOut(w, 200, info)
		}
		return true
	}
	const base = "/api/automation/rules"
	if r.URL.Path != base && !strings.HasPrefix(r.URL.Path, base+"/") {
		return false
	}
	s.controllers.mu.Lock()
	defer s.controllers.mu.Unlock()
	if r.URL.Path == base+"/disable" && r.Method == "POST" {
		ids, ok := b["rule_ids"].([]any)
		if !ok {
			fail(w, 400, "rule_ids must be a list")
			return true
		}
		disabled := []string{}
		err := s.Store.App.RunInTransaction(func(app core.App) error {
			st := &store.Store{App: app}
			for _, value := range ids {
				id, ok := value.(string)
				if !ok {
					continue
				}
				row, err := st.Get("automation_rules", id)
				if isMissing(err) {
					continue
				}
				if err != nil {
					return err
				}
				if row["enabled"] == false {
					continue
				}
				row["enabled"], row["last_state"] = false, nil
				if err = st.Put("automation_rules", id, str(row, "jackery_device_sn"), row); err != nil {
					return err
				}
				disabled = append(disabled, id)
			}
			return nil
		})
		if err != nil {
			storageError(w, err)
		} else {
			jsonOut(w, 200, map[string]any{"ok": true, "disabled": disabled})
		}
		return true
	}
	if strings.HasSuffix(r.URL.Path, "/history") && r.Method == "GET" {
		s.ruleHistory(w, r, strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, base+"/"), "/history"))
		return true
	}
	id := strings.TrimPrefix(r.URL.Path, base+"/")
	if r.URL.Path == base {
		switch r.Method {
		case "GET":
			rows, e := s.Store.List("automation_rules", "", 0, 1000)
			if e != nil {
				storageError(w, e)
			} else {
				jsonOut(w, 200, map[string]any{"rules": rows})
			}
			return true
		case "POST":
			if trigger := str(b, "trigger"); trigger != "" && trigger != "battery_percent" {
				fail(w, 400, "unsupported trigger")
				return true
			}
			if _, ok := b["value"].(float64); !ok {
				fail(w, 400, "numeric value required")
				return true
			}
			if _, exists := b["enabled"]; !exists {
				b["enabled"] = true
			}
			b["kasa_host"] = strings.TrimSpace(str(b, "kasa_host"))
			if str(b, "name") == "" {
				b["name"] = "Unnamed rule"
			}
			b["trigger"] = "battery_percent"
			if str(b, "id") == "" {
				b["id"] = store.ID()[:8]
			}
			data, _ := json.Marshal(b)
			var rule automation.Rule
			if json.Unmarshal(data, &rule) != nil || rule.Validate() != nil {
				fail(w, 400, "invalid automation rule")
				return true
			}
			if old, err := s.Store.Get("automation_rules", rule.ID); err == nil && b["last_fired"] == nil {
				b["last_fired"] = old["last_fired"]
			}
			if e := s.Store.Put("automation_rules", rule.ID, rule.DeviceSN, b); e != nil {
				storageError(w, e)
			} else {
				jsonOut(w, 200, map[string]any{"ok": true, "rule": b})
			}
			return true
		}
	}
	if r.Method == "DELETE" && !strings.Contains(id, "/") {
		_, lookupErr := s.Store.Get("automation_rules", id)
		existed := lookupErr == nil
		if e := s.Store.Delete("automation_rules", id); e != nil && !isMissing(e) {
			storageError(w, e)
		} else {
			jsonOut(w, 200, map[string]any{"ok": true, "deleted": existed})
		}
		return true
	}
	return false
}
