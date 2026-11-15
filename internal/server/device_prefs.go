package server

import (
	"database/sql"
	"errors"
	"net/http"
	"strings"
)

func (s *Server) devicePrefs(w http.ResponseWriter, r *http.Request, b map[string]any) bool {
	if r.Method == "GET" {
		rows, err := s.Store.App.FindRecordsByFilter("device_prefs", "", "", 0, 0)
		if err != nil {
			storageError(w, err)
			return true
		}
		prefs := map[string]any{}
		for _, row := range rows {
			v, err := s.Store.Get("device_prefs", row.GetString("key"))
			if err != nil {
				storageError(w, err)
				return true
			}
			prefs[row.GetString("key")] = v
		}
		jsonOut(w, 200, map[string]any{"prefs": prefs})
		return true
	}
	if r.Method != "POST" {
		fail(w, 405, "method not allowed")
		return true
	}
	id := strings.TrimSpace(str(b, "device_id"))
	if id == "" {
		fail(w, 400, "device_id is required")
		return true
	}
	row, err := s.Store.Get("device_prefs", id)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		storageError(w, err)
		return true
	}
	if row == nil {
		row = map[string]any{}
	}
	changed := false
	for _, key := range []string{"alias", "live_controls", "ignore_inverter_soc", "calc_grid", "solar_flow_unified", "load_sources"} {
		raw, ok := b[key]
		if !ok {
			continue
		}
		changed = true
		switch key {
		case "alias":
			if raw == nil {
				delete(row, key)
			} else {
				text, ok := raw.(string)
				if !ok {
					fail(w, 400, "alias must be text")
					return true
				}
				text = strings.TrimSpace(text)
				if text == "" {
					delete(row, key)
				} else {
					row[key] = text
				}
			}
		case "live_controls", "load_sources":
			if raw == nil {
				delete(row, key)
				continue
			}
			list, ok := raw.([]any)
			if !ok {
				fail(w, 400, key+" must be a list")
				return true
			}
			out := []string{}
			seen := map[string]bool{}
			for _, v := range list {
				text, ok := v.(string)
				if !ok {
					fail(w, 400, key+" must contain strings")
					return true
				}
				text = strings.TrimSpace(text)
				if text != "" && !seen[text] {
					out = append(out, text)
					seen[text] = true
				}
			}
			row[key] = out
		default:
			flag, ok := raw.(bool)
			if !ok {
				fail(w, 400, key+" must be boolean")
				return true
			}
			if flag {
				row[key] = true
			} else {
				delete(row, key)
			}
		}
	}
	if !changed {
		fail(w, 400, "preference field required")
		return true
	}
	if len(row) == 0 {
		err = s.Store.Delete("device_prefs", id)
		if errors.Is(err, sql.ErrNoRows) {
			err = nil
		}
	} else {
		err = s.Store.Put("device_prefs", id, id, row)
	}
	if err != nil {
		storageError(w, err)
	} else {
		jsonOut(w, 200, map[string]any{"ok": true, "device_id": id, "prefs": row})
	}
	return true
}

func (s *Server) deviceAlias(device map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range device {
		out[k] = v
	}
	for _, id := range []string{str(device, "device_id"), str(device, "device_sn")} {
		if id == "" {
			continue
		}
		pref, err := s.Store.Get("device_prefs", id)
		if err != nil {
			continue
		}
		if alias := str(pref, "alias"); alias != "" {
			if out["portal_name"] == nil {
				out["portal_name"] = out["name"]
			}
			out["name"] = alias
		}
		break
	}
	return out
}
