package server

import (
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/compdani/jackery-monitor/internal/automation"
	"github.com/compdani/jackery-monitor/internal/store"
	"net/http"
	"strings"
)

func (s *Server) resources(w http.ResponseWriter, r *http.Request, b map[string]any) bool {
	path := r.URL.Path
	collection := "settings"
	key := ""
	switch path {
	case "/api/settings":
		return s.settingsRoute(w, r, b)
	case "/api/device_prefs":
		return s.devicePrefs(w, r, b)
	case "/api/cost/plan":
		key = "cost_plan"
	case "/api/forecast/load_schedule":
		key = "load_schedule:" + r.URL.Query().Get("device_sn")
		if b != nil && str(b, "device_sn") != "" {
			key = "load_schedule:" + str(b, "device_sn")
		}
	case "/api/anthropic/prefs":
		key = "anthropic_prefs"
	case "/api/ai/provider":
		key = "ai_provider"
	case "/api/location":
		if r.Method == "GET" {
			v, e := s.Store.Secret(s.Key, "location")
			if errors.Is(e, sql.ErrNoRows) {
				jsonOut(w, 200, map[string]any{"latitude": nil, "longitude": nil})
			} else if e != nil {
				storageError(w, e)
			} else {
				if path == "/api/cost/plan" {
					jsonOut(w, 200, map[string]any{"plan": v, "presets": costPresets()})
				} else {
					jsonOut(w, 200, v)
				}
			}
			return true
		}
		if r.Method != "POST" {
			return false
		}
		lat, ok := b["latitude"].(float64)
		lon, ok2 := b["longitude"].(float64)
		if !ok || !ok2 || lat < -90 || lat > 90 || lon < -180 || lon > 180 || (lat == 0 && lon == 0) {
			fail(w, 400, "invalid latitude/longitude")
			return true
		}
		if e := s.Store.SaveSecret(s.Key, "location", b); e != nil {
			storageError(w, e)
		} else {
			if path == "/api/cost/plan" {
				jsonOut(w, 200, map[string]any{"plan": b})
			} else {
				jsonOut(w, 200, b)
			}
		}
		return true
	default:
		// Configuration of controllers remains disabled until the decision engines
		// and actuator adapters are integrated; do not claim to enable automation.
		if strings.HasSuffix(path, "/config") {
			return false
		}
	}
	if key == "" {
		return false
	}
	if r.Method == "GET" {
		v, e := s.Store.Get(collection, key)
		if errors.Is(e, sql.ErrNoRows) {
			v = map[string]any{}
			e = nil
			if path == "/api/cost/plan" {
				v = map[string]any{"type": "flat", "rate_per_kwh": .30, "currency": "USD"}
			}
		}
		if e != nil {
			storageError(w, e)
		} else {
			if path == "/api/cost/plan" {
				jsonOut(w, 200, map[string]any{"plan": v, "presets": costPresets()})
			} else {
				jsonOut(w, 200, v)
			}
		}
		return true
	}
	if r.Method == "POST" || r.Method == "PUT" {
		if path == "/api/cost/plan" {
			data, _ := json.Marshal(b)
			var p automation.CostPlan
			if json.Unmarshal(data, &p) != nil || p.Validate() != nil {
				fail(w, 400, "invalid cost plan")
				return true
			}
		}
		if e := s.Store.Put(collection, key, "", b); e != nil {
			storageError(w, e)
		} else {
			if path == "/api/cost/plan" {
				jsonOut(w, 200, map[string]any{"plan": b})
			} else {
				jsonOut(w, 200, b)
			}
		}
		return true
	}
	return false
}
func (s *Server) conversations(w http.ResponseWriter, r *http.Request, b map[string]any) bool {
	const base = "/api/assistant/conversations"
	if !strings.HasPrefix(r.URL.Path, base) {
		return false
	}
	id := strings.TrimPrefix(r.URL.Path, base)
	id = strings.TrimPrefix(id, "/")
	if id == "" {
		if r.Method == "GET" {
			rows, e := s.Store.List("conversations", "", 0, 500)
			if e != nil {
				storageError(w, e)
			} else {
				jsonOut(w, 200, map[string]any{"conversations": rows})
			}
			return true
		}
		if r.Method == "POST" {
			id = store.ID()
			b["id"] = id
			if str(b, "title") == "" {
				b["title"] = "New chat"
			}
			if e := s.Store.Put("conversations", id, "", b); e != nil {
				storageError(w, e)
			} else {
				jsonOut(w, 200, b)
			}
			return true
		}
	} else if r.Method == "GET" {
		v, e := s.Store.Get("conversations", id)
		if e != nil {
			storageError(w, e)
		} else {
			jsonOut(w, 200, v)
		}
		return true
	} else if r.Method == "DELETE" {
		if e := s.Store.Delete("conversations", id); e != nil {
			storageError(w, e)
		} else {
			jsonOut(w, 200, map[string]any{"ok": true})
		}
		return true
	}
	return false
}
