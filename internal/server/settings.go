package server

import (
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"os"
	"sort"
	"strconv"
	"time"
)

// Static compatibility metadata ported from settings.py and cost.py.
//
//go:embed contracts/settings.json
var settingsJSON []byte

//go:embed contracts/cost.json
var costJSON []byte

//go:embed contracts/compat.json
var compatJSON []byte

func settingNumber(raw any, spec map[string]any) (int, bool) {
	f, err := strconv.ParseFloat(fmt.Sprint(raw), 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, false
	}
	return int(math.Max(spec["min"].(float64), math.Min(spec["max"].(float64), f))), true
}
func (s *Server) settingValues() (map[string]any, []map[string]any, error) {
	var specs map[string]map[string]any
	_ = json.Unmarshal(settingsJSON, &specs)
	saved, err := s.Store.Get("settings", "settings")
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, nil, err
	}
	keys := make([]string, 0, len(specs))
	for k := range specs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	values := map[string]any{}
	entries := []map[string]any{}
	for _, k := range keys {
		spec := specs[k]
		v := int(spec["default"].(float64))
		if n, ok := settingNumber(os.Getenv(spec["env"].(string)), spec); ok {
			v = n
		}
		if n, ok := settingNumber(saved[k], spec); ok {
			v = n
		}
		delete(spec, "env")
		spec["key"] = k
		spec["value"] = v
		values[k] = v
		entries = append(entries, spec)
	}
	return values, entries, nil
}
func (s *Server) settingsRoute(w http.ResponseWriter, r *http.Request, b map[string]any) bool {
	values, entries, err := s.settingValues()
	if err != nil {
		storageError(w, err)
		return true
	}
	if r.Method == "GET" {
		jsonOut(w, 200, map[string]any{"settings": entries})
		return true
	}
	if r.Method != "POST" {
		fail(w, 405, "method not allowed")
		return true
	}
	saved, err := s.Store.Get("settings", "settings")
	if err != nil && !isMissing(err) {
		storageError(w, err)
		return true
	}
	if saved == nil {
		saved = map[string]any{}
	}
	for _, spec := range entries {
		k := spec["key"].(string)
		if raw, ok := b[k]; ok {
			if n, ok := settingNumber(raw, spec); ok {
				values[k] = n
				saved[k] = n
			}
		}
	}
	if err := s.Store.Put("settings", "settings", "", saved); err != nil {
		storageError(w, err)
	} else {
		jsonOut(w, 200, map[string]any{"ok": true, "settings": values})
	}
	return true
}
func (s *Server) pollInterval(fallback time.Duration) time.Duration {
	v, _, err := s.settingValues()
	if err != nil {
		return fallback
	}
	return time.Duration(v["poll_interval_s"].(int)) * time.Second
}
func costPresets() []map[string]any {
	var presets map[string]map[string]any
	_ = json.Unmarshal(costJSON, &presets)
	keys := []string{}
	for k := range presets {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := []map[string]any{}
	for _, k := range keys {
		v := presets[k]
		v["id"] = k
		out = append(out, v)
	}
	return out
}
