package server

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/compdani/jackery-monitor/internal/automation"
	"github.com/compdani/jackery-monitor/internal/forecast"
)

// Port of load_schedule.py normalization. Weekdays use Python's Monday=0.
var legacyDays = map[string][]int{"all": {0, 1, 2, 3, 4, 5, 6}, "weekday": {0, 1, 2, 3, 4}, "weekend": {5, 6}}
var dateRE = regexp.MustCompile(`^(\d{4})-(\d{2})-(\d{2})$`)

func shortID() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
func parseHHMMValue(v any) (int, bool) {
	s, ok := v.(string)
	if !ok || s == "" {
		return 0, false
	}
	return automation.ParseHHMM(s)
}
func formatHHMM(m int) string {
	m = ((m % 1440) + 1440) % 1440
	return fmt.Sprintf("%02d:%02d", m/60, m%60)
}
func parseScheduleDate(v any) any {
	s, ok := v.(string)
	if !ok {
		return nil
	}
	m := dateRE.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return nil
	}
	y, _ := strconv.Atoi(m[1])
	mo, _ := strconv.Atoi(m[2])
	d, _ := strconv.Atoi(m[3])
	t := time.Date(y, time.Month(mo), d, 0, 0, 0, 0, time.UTC)
	if t.Year() != y || int(t.Month()) != mo || t.Day() != d {
		return nil
	}
	return fmt.Sprintf("%04d-%02d-%02d", y, mo, d)
}
func weekdaysList(v any) []int {
	raw, ok := v.([]any)
	if !ok {
		if ints, ok := v.([]int); ok {
			for _, i := range ints {
				raw = append(raw, float64(i))
			}
		} else {
			return nil
		}
	}
	seen := map[int]bool{}
	out := []int{}
	for _, x := range raw {
		var d int
		switch n := x.(type) {
		case float64:
			d = int(n)
		case int:
			d = n
		case string:
			var e error
			if d, e = strconv.Atoi(n); e != nil {
				continue
			}
		default:
			continue
		}
		if d >= 0 && d <= 6 && !seen[d] {
			seen[d] = true
			out = append(out, d)
		}
	}
	sort.Ints(out)
	if len(out) == 0 {
		return nil
	}
	return out
}
func normalizeWindow(raw any) map[string]any {
	w, ok := raw.(map[string]any)
	if !ok {
		return nil
	}
	if k := str(w, "kind"); k != "once" && k != "weekly" {
		days := str(w, "days")
		if _, ok := legacyDays[days]; !ok {
			days = "all"
		}
		c := map[string]any{}
		for k, v := range w {
			c[k] = v
		}
		c["kind"], c["weekdays"], c["date"] = "weekly", toAny(legacyDays[days]), nil
		w = c
	}
	start, ok1 := parseHHMMValue(w["start"])
	end, ok2 := parseHHMMValue(w["end"])
	if !ok1 || !ok2 {
		return nil
	}
	watts := 0.0
	switch v := w["watts"].(type) {
	case nil:
	case float64:
		watts = v
	case string:
		var e error
		if watts, e = strconv.ParseFloat(v, 64); e != nil {
			return nil
		}
	default:
		return nil
	}
	if watts < 0 || watts > 20000 || math.IsNaN(watts) {
		return nil
	}
	kind := str(w, "kind")
	var date any
	var weekdays any
	if kind == "once" {
		date = parseScheduleDate(w["date"])
		if date == nil {
			return nil
		}
	} else {
		days := weekdaysList(w["weekdays"])
		if days == nil {
			days = legacyDays["all"]
		}
		weekdays = toAny(days)
	}
	id := fmt.Sprint(w["id"])
	if w["id"] == nil || id == "" {
		id = shortID()
	}
	if len(id) > 16 {
		id = id[:16]
	}
	label := []rune(strings.TrimSpace(str(w, "label")))
	if len(label) > 80 {
		label = label[:80]
	}
	return map[string]any{"id": id, "label": string(label), "start": formatHHMM(start), "end": formatHHMM(end), "watts": math.Round(watts*10) / 10, "kind": kind, "date": date, "weekdays": weekdays}
}
func toAny(v []int) []any {
	out := make([]any, len(v))
	for i, x := range v {
		out[i] = x
	}
	return out
}

// NormalizeLoadSchedule ports load_schedule._normalize_device.
func NormalizeLoadSchedule(raw map[string]any) map[string]any {
	d := map[string]any{"mode": "scheduled", "sleep_start": nil, "sleep_end": nil, "windows": []any{}}
	if raw == nil {
		return d
	}
	if m := str(raw, "mode"); m == "historical" || m == "scheduled" {
		d["mode"] = m
	}
	if v, ok := parseHHMMValue(raw["sleep_start"]); ok {
		d["sleep_start"] = formatHHMM(v)
	}
	if v, ok := parseHHMMValue(raw["sleep_end"]); ok {
		d["sleep_end"] = formatHHMM(v)
	}
	windows := []any{}
	seen := map[string]bool{}
	list, _ := raw["windows"].([]any)
	for _, w := range list {
		n := normalizeWindow(w)
		if n == nil {
			continue
		}
		if seen[n["id"].(string)] {
			n["id"] = shortID()
		}
		seen[n["id"].(string)] = true
		windows = append(windows, n)
	}
	d["windows"] = windows
	return d
}
func (s *Server) loadSchedule(sn string) (map[string]any, error) {
	raw, err := s.Store.Get("settings", "load_schedule:"+sn)
	if err != nil && !isMissing(err) {
		return nil, err
	}
	return NormalizeLoadSchedule(raw), nil
}

// learnedProfile serializes FitLoadProfile like serialize_learned_profile.
func learnedProfile(profile map[forecast.LoadKey]float64) []map[string]any {
	keys := make([]forecast.LoadKey, 0, len(profile))
	for k := range profile {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].Hour != keys[j].Hour {
			return keys[i].Hour < keys[j].Hour
		}
		return keys[i].Weekend < keys[j].Weekend
	})
	out := []map[string]any{}
	for _, k := range keys {
		out = append(out, map[string]any{"hour": k.Hour, "weekend": k.Weekend != 0, "watts": math.Round(profile[k]*10) / 10})
	}
	return out
}
func windowsFromProfile(profile map[forecast.LoadKey]float64) []any {
	out := []any{}
	for _, r := range learnedProfile(profile) {
		h := r["hour"].(int) % 24
		weekend := r["weekend"].(bool)
		days, label := []int{0, 1, 2, 3, 4}, "weekday"
		if weekend {
			days, label = []int{5, 6}, "weekend"
		}
		out = append(out, map[string]any{"id": shortID(), "label": fmt.Sprintf("%02d:00 %s", h, label), "start": fmt.Sprintf("%02d:00", h), "end": fmt.Sprintf("%02d:00", (h+1)%24), "watts": math.Max(0, r["watts"].(float64)), "kind": "weekly", "date": nil, "weekdays": toAny(days)})
	}
	return out
}
