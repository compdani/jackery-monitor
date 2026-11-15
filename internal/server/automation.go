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
	"net/http"
	"strings"
	"sync"
)

type controllerRuntime struct {
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
		fired, e := s.controllers.engine.Evaluate(ctx, rule, sn, soc)
		if fired || e != nil {
			record := map[string]any{"rule_id": rule.ID, "device_sn": sn, "soc": soc, "fired": fired}
			if e != nil {
				record["error"] = e.Error()
			}
			if e = s.Store.Put("automation_decisions", store.ID(), sn, record); e != nil {
				s.Store.App.Logger().Error("persist automation decision", "error", e)
			}
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
				b["id"] = store.ID()
			}
			data, _ := json.Marshal(b)
			var rule automation.Rule
			if json.Unmarshal(data, &rule) != nil || rule.Validate() != nil {
				fail(w, 400, "invalid automation rule")
				return true
			}
			if e := s.Store.Put("automation_rules", rule.ID, rule.DeviceSN, b); e != nil {
				storageError(w, e)
			} else {
				jsonOut(w, 200, b)
			}
			return true
		}
	}
	if r.Method == "DELETE" && !strings.Contains(id, "/") {
		if e := s.Store.Delete("automation_rules", id); e != nil {
			storageError(w, e)
		} else {
			jsonOut(w, 200, map[string]any{"ok": true})
		}
		return true
	}
	return false
}
