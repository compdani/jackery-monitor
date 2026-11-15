package server

import (
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/compdani/jackery-monitor/internal/automation"
	"github.com/compdani/jackery-monitor/internal/store"
	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	"net/http"
	"sort"
	"strings"
	"time"
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
		return s.aiPreferences(w, r, b, false)
	case "/api/ai/provider":
		return s.aiPreferences(w, r, b, true)
	case "/api/location":
		if r.Method == "GET" {
			v, e := s.Store.Secret(s.Key, "location")
			if errors.Is(e, sql.ErrNoRows) {
				jsonOut(w, 200, map[string]any{"latitude": nil, "longitude": nil})
			} else if e != nil {
				storageError(w, e)
			} else {
				jsonOut(w, 200, v)
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
			jsonOut(w, 200, b)
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
				b["messages"] = []any{}
				b["attachments"] = []any{}
				b["tool_runs"] = []any{}
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
				sort.SliceStable(rows, func(i, j int) bool {
					a, _ := rows[i]["updated_at"].(float64)
					b, _ := rows[j]["updated_at"].(float64)
					return a > b
				})
				if len(rows) > 50 {
					rows = rows[:50]
				}
				for _, row := range rows {
					messages, err := s.Store.List("messages", str(row, "id"), 0, 0)
					if err != nil {
						storageError(w, err)
						return true
					}
					snippet := ""
					for i := len(messages) - 1; i >= 0; i-- {
						text := strings.TrimSpace(str(messages[i], "content"))
						if text != "" {
							snippet = strings.ReplaceAll(text, "\n", " ")
							break
						}
					}
					runes := []rune(snippet)
					if len(runes) > 80 {
						snippet = string(runes[:80]) + "…"
					}
					row["snippet"] = snippet
				}
				jsonOut(w, 200, map[string]any{"conversations": rows})
			}
			return true
		}
		if r.Method == "POST" {
			id = store.ID()
			b["id"] = id
			stamp := float64(time.Now().Unix())
			b["created_at"] = stamp
			b["updated_at"] = stamp
			b["ts"] = stamp
			for _, key := range []string{"model", "provider"} {
				if _, ok := b[key]; !ok {
					b[key] = nil
				}
			}
			if str(b, "title") == "" {
				b["title"] = "New chat"
			}
			if e := s.Store.Put("conversations", id, "", b); e != nil {
				storageError(w, e)
			} else {
				b["messages"] = []any{}
				b["attachments"] = []any{}
				b["tool_runs"] = []any{}
				jsonOut(w, 200, b)
			}
			return true
		}
	} else if r.Method == "GET" {
		v, err := s.Store.Get("conversations", id)
		if err != nil {
			storageError(w, err)
			return true
		}
		for _, collection := range []string{"messages", "tool_runs", "attachments"} {
			rows, err := s.Store.List(collection, id, 0, 0)
			if err != nil {
				storageError(w, err)
				return true
			}
			v[collection] = rows
		}
		jsonOut(w, 200, v)
		return true
	} else if r.Method == "DELETE" {
		err := s.Store.App.RunInTransaction(func(app core.App) error {
			st := &store.Store{App: app}
			if _, err := st.Get("conversations", id); err != nil {
				return err
			}
			for _, collection := range []string{"messages", "tool_runs", "attachments"} {
				rows, err := app.FindRecordsByFilter(collection, "device_sn = {:id}", "", 0, 0, dbx.Params{"id": id})
				if err != nil {
					return err
				}
				for _, row := range rows {
					if err := app.Delete(row); err != nil {
						return err
					}
				}
			}
			return st.Delete("conversations", id)
		})
		if err != nil {
			storageError(w, err)
		} else {
			jsonOut(w, 200, map[string]any{"ok": true})
		}
		return true
	}

	return false
}
