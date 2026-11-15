package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/compdani/jackery-monitor/internal/advisor"
	"github.com/compdani/jackery-monitor/internal/assistant"
	"github.com/compdani/jackery-monitor/internal/automation"
	"github.com/compdani/jackery-monitor/internal/forecast"
	"github.com/compdani/jackery-monitor/internal/store"
	"github.com/pocketbase/pocketbase/core"
)

const maxToolRows = 500

type advisorRuntime struct {
	mu      sync.Mutex
	jobs    map[string]map[string]any
	lastRun map[string]float64
}

func isoTS(v any) any {
	f, ok := v.(float64)
	if !ok {
		if i, ok2 := v.(int64); ok2 {
			f, ok = float64(i), true
		}
	}
	if !ok || f == 0 {
		return nil
	}
	return time.Unix(int64(f), 0).UTC().Format("2006-01-02T15:04:05+00:00")
}
func parseISO(v any) int64 {
	s, _ := v.(string)
	s = strings.TrimSpace(strings.TrimSuffix(s, "Z"))
	if s == "" {
		return 0
	}
	for _, layout := range []string{"2006-01-02T15:04:05-07:00", "2006-01-02T15:04:05", "2006-01-02T15:04", "2006-01-02"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.Unix()
		}
	}
	return 0
}
func capRows(rows []map[string]any) map[string]any {
	n := len(rows)
	return map[string]any{"rows": rows[:min(n, maxToolRows)], "row_count": n, "truncated": n > maxToolRows}
}

// advisorQuery ports _make_advisor_query_fn; bad inputs return {"error"}.
func (s *Server) advisorQuery(sn, name string, args map[string]any) map[string]any {
	now := time.Now().Unix()
	main, pack, _, _ := s.capacityHints(sn)
	switch name {
	case "query_samples":
		start, end := parseISO(args["start_iso"]), parseISO(args["end_iso"])
		if start == 0 || end == 0 {
			return map[string]any{"error": "start_iso/end_iso required (ISO 8601)"}
		}
		if end <= start {
			return map[string]any{"error": "end_iso must be after start_iso"}
		}
		requested := 3600
		if v, ok := numberArg(args["bucket_s"]); ok {
			requested = int(v)
		}
		bucket := max(60, requested)
		hours := min(max(1, int((now-start+3599)/3600)+1), 45*24)
		minutes, err := s.Store.EnergyMinutes(sn, float64(now-int64(hours)*3600))
		if err != nil {
			return map[string]any{"error": err.Error()}
		}
		hist := store.EnergyHistory(minutes, float64(now-int64(hours)*3600), bucket)
		bh := float64(bucket) / 3600
		out := []map[string]any{}
		for _, r := range hist {
			ts := forecast.Number(r, "ts")
			if ts < float64(start) || ts >= float64(end) {
				continue
			}
			avg := func(k string) int { return int(forecast.Number(r, k) / bh) }
			out = append(out, map[string]any{"ts": isoTS(ts), "soc": r["battery_pct"], "in_w_avg": avg("input_wh"), "out_w_avg": avg("output_wh"), "solar_w_avg": avg("solar_wh"), "ac_input_w_avg": avg("ac_input_wh"), "in_w_instant": r["input_w"], "out_w_instant": r["output_w"], "solar_w_instant": r["solar_w"]})
		}
		res := capRows(out)
		res["returned_rows"] = min(len(out), maxToolRows)
		res["bucket_s"], res["requested_bucket_s"] = bucket, requested
		res["window_start"], res["window_end"], res["lookback_hours"] = isoTS(float64(start)), isoTS(float64(end)), hours
		res["expected_buckets"] = max(0, int((end-1)/int64(bucket)-start/int64(bucket)+1))
		res["lookback_clamped"] = hours >= 45*24
		return res
	case "query_predictions":
		start, end := parseISO(args["start_iso"]), parseISO(args["end_iso"])
		rows, err := s.Store.PredictionAccuracy(sn, store.AccuracyOptions{Now: now, MainCapacity: main, PackCapacity: pack})
		if err != nil {
			return map[string]any{"error": err.Error()}
		}
		maxLead, hasLead := numberArg(args["max_lead_h"])
		out := []map[string]any{}
		for _, p := range rows {
			target := forecast.Number(p, "target")
			if (start != 0 && target < float64(start)) || (end != 0 && target >= float64(end)) || (hasLead && forecast.Number(p, "lead_time_h") > maxLead) {
				continue
			}
			r1 := func(k string) float64 { return math.Round(forecast.Number(p, k)*10) / 10 }
			out = append(out, map[string]any{"made_at": isoTS(p["made_at"]), "target": isoTS(target), "lead_time_h": p["lead_time_h"], "predicted_soc": r1("predicted_soc"), "actual_soc": r1("actual_soc"), "error_pp": r1("error")})
		}
		return capRows(out)
	case "query_decisions":
		start, end := parseISO(args["start_iso"]), parseISO(args["end_iso"])
		history, err := s.smartHistory(sn)
		if err != nil {
			return map[string]any{"error": err.Error()}
		}
		out := []map[string]any{}
		for _, d := range history {
			at := forecast.Number(d, "decided_at")
			if (start != 0 && at < float64(start)) || (end != 0 && at >= float64(end)) {
				continue
			}
			var actual any
			if sun := forecast.Number(d, "sunrise_ts"); sun > 0 && sun <= float64(now) {
				if v, _ := s.Store.SystemSOCAt(sn, sun, main, pack); v != nil {
					actual = *v
				}
			}
			out = append(out, map[string]any{"decided_at": isoTS(at), "action": d["action"], "mode": d["mode"], "predicted_sunrise_soc_pct": d["predicted_sunrise_soc_pct"], "actual_sunrise_soc_pct": actual, "target_sunrise_soc_pct": d["target_sunrise_soc_pct"], "reason": d["reason"]})
		}
		sort.Slice(out, func(i, j int) bool { return fmt.Sprint(out[i]["decided_at"]) < fmt.Sprint(out[j]["decided_at"]) })
		return capRows(out)
	case "query_weather":
		start, end := parseISO(args["start_iso"]), parseISO(args["end_iso"])
		if end == 0 {
			end = now
		}
		obs, err := s.Store.List("weather_observations", "", float64(start), 2000)
		if err != nil {
			return map[string]any{"error": err.Error()}
		}
		out := []map[string]any{}
		for _, w := range obs {
			if forecast.Number(w, "ts") >= float64(end) {
				continue
			}
			out = append(out, map[string]any{"hour": isoTS(w["ts"]), "ghi_w_m2": w["ghi_w_m2"], "cloud_cover_pct": w["cloud_cover_pct"]})
		}
		return capRows(out)
	case "query_battery_packs":
		rows, err := s.Store.List("battery_packs", sn, 0, 0)
		if err != nil {
			return map[string]any{"error": err.Error()}
		}
		out := []map[string]any{}
		if len(rows) > 0 {
			sort.SliceStable(rows, func(i, j int) bool { return forecast.Number(rows[i], "ts") < forecast.Number(rows[j], "ts") })
			items, _ := rows[len(rows)-1]["packs"].([]any)
			for _, it := range items {
				if m, ok := it.(map[string]any); ok {
					c := map[string]any{}
					for k, v := range m {
						c[k] = v
					}
					c["internal_temp_c"] = nil
					out = append(out, c)
				}
			}
		}
		return map[string]any{"rows": out, "row_count": len(out)}
	}
	return map[string]any{"error": "unknown tool: " + name}
}

// Suggestions keep Python's integer ids; imported rows are matched by their
// preserved payload id.
func (s *Server) suggestions(st *store.Store, sn, status string, limit int) ([]map[string]any, error) {
	rows, err := st.List("suggestions", "", 0, 0)
	if err != nil {
		return nil, err
	}
	out := []map[string]any{}
	for _, r := range rows {
		if sn != "" && r["device_sn"] != nil && str(r, "device_sn") != sn {
			continue
		}
		if status != "" && str(r, "status") != status {
			continue
		}
		out = append(out, r)
	}
	sort.SliceStable(out, func(i, j int) bool {
		return forecast.Number(out[i], "created_at") > forecast.Number(out[j], "created_at")
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}
func findByID(st *store.Store, collection string, id int64) (map[string]any, string, error) {
	recs, err := st.App.FindRecordsByFilter(collection, "", "", 0, 0)
	if err != nil {
		return nil, "", err
	}
	for _, rec := range recs {
		p, err := st.Get(collection, rec.GetString("key"))
		if err != nil {
			return nil, "", err
		}
		if int64(forecast.Number(p, "id")) == id {
			return p, rec.GetString("key"), nil
		}
	}
	return nil, "", nil
}
func nextID(st *store.Store, collection string) (int64, error) {
	rows, err := st.List(collection, "", 0, 0)
	if err != nil {
		return 0, err
	}
	id := int64(0)
	for _, r := range rows {
		id = max(id, int64(forecast.Number(r, "id")))
	}
	return id + 1, nil
}
func (s *Server) insertSuggestion(st *store.Store, sn string, row map[string]any) (int64, error) {
	id, err := nextID(st, "suggestions")
	if err != nil {
		return 0, err
	}
	now := float64(time.Now().Unix())
	row["id"], row["created_at"], row["ts"], row["status"], row["decided_at"], row["decided_by"] = id, now, now, "pending", nil, nil
	var dev any
	if sn != "" {
		dev = sn
	}
	row["device_sn"] = dev
	return id, st.Put("suggestions", fmt.Sprintf("suggestion:%d", id), sn, row)
}

// expireSuggestions ports expire_old_suggestions (7 days).
func expireSuggestions(st *store.Store, now float64) error {
	rows, err := st.App.FindRecordsByFilter("suggestions", "", "", 0, 0)
	if err != nil {
		return err
	}
	for _, rec := range rows {
		p, err := st.Get("suggestions", rec.GetString("key"))
		if err != nil {
			return err
		}
		if str(p, "status") == "pending" && forecast.Number(p, "created_at") < now-7*86400 {
			p["status"], p["decided_at"], p["decided_by"] = "dismissed", now, "auto-expired"
			if err = st.Put("suggestions", rec.GetString("key"), rec.GetString("device_sn"), p); err != nil {
				return err
			}
		}
	}
	return nil
}

var errSuggestion = errors.New("suggestion")

type httpErr struct {
	code int
	msg  string
}

func (e httpErr) Error() string { return e.msg }

// applySuggestion ports the apply route: whitelist and range re-validated at
// apply time, then config write, audit row and status flip in one transaction.
func (s *Server) applySuggestion(id int64) (map[string]any, error) {
	var out map[string]any
	s.smart.mu.Lock()
	defer s.smart.mu.Unlock()
	err := s.Store.App.RunInTransaction(func(app core.App) error {
		st := &store.Store{App: app}
		sug, key, err := findByID(st, "suggestions", id)
		if err != nil {
			return err
		}
		if sug == nil {
			return httpErr{404, "suggestion not found"}
		}
		if status := str(sug, "status"); status != "pending" {
			return httpErr{400, "suggestion is " + status + ", not pending"}
		}
		if str(sug, "kind") != "config" {
			return httpErr{400, "anomalies are not directly applicable; use dismiss/acknowledge"}
		}
		target := str(sug, "target")
		rule, ok := advisor.AllowedTarget(target)
		if !ok {
			return httpErr{400, fmt.Sprintf("target %q no longer in whitelist", target)}
		}
		proposed, ok := numberArg(sug["proposed_value"])
		if !ok {
			return httpErr{400, "proposed_value not numeric"}
		}
		if proposed < rule.Min || proposed > rule.Max {
			return httpErr{400, fmt.Sprintf("proposed value out of safe range [%g, %g]", rule.Min, rule.Max)}
		}
		if !strings.HasPrefix(target, "smart_charge.") {
			return httpErr{400, fmt.Sprintf("applying %q is not yet supported", target)}
		}
		sn := str(sug, "device_sn")
		if rule.Scope == "device" && sn == "" {
			return httpErr{400, "device-scoped suggestion missing device_sn"}
		}
		field := strings.TrimPrefix(target, "smart_charge.")
		raw, err := st.Get("settings", "smart_charge")
		if err != nil && !isMissing(err) {
			return err
		}
		by, _ := raw["by_device"].(map[string]any)
		if by == nil {
			by = map[string]any{}
			if len(raw) > 0 {
				by["__legacy__"] = raw
			}
		}
		cur, _ := by[sn].(map[string]any)
		if cur == nil {
			cur = toSolarMap(automation.NormalizeSmart(nil))
		}
		cfg := map[string]any{}
		for k, v := range cur {
			cfg[k] = v
		}
		old := toSolarMap(automation.NormalizeSmart(cur))[field]
		var value any = proposed
		if field != "target_sunrise_soc_pct" {
			value = math.Trunc(proposed)
		}
		cfg[field] = value
		by[sn] = automation.NormalizeSmart(cfg)
		if err = st.Put("settings", "smart_charge", "", map[string]any{"by_device": by}); err != nil {
			return err
		}
		applied := toSolarMap(by[sn])[field]
		cid, err := nextID(st, "changes")
		if err != nil {
			return err
		}
		now := float64(time.Now().Unix())
		if err = st.Put("changes", fmt.Sprintf("change:%d", cid), sn, map[string]any{"id": cid, "suggestion_id": id, "applied_at": now, "ts": now, "device_sn": sn, "target": target, "old_value": old, "new_value": applied, "reasoning": sug["reasoning"]}); err != nil {
			return err
		}
		sug["status"], sug["decided_at"], sug["decided_by"] = "applied", now, "user"
		if err = st.Put("suggestions", key, sn, sug); err != nil {
			return err
		}
		out = map[string]any{"ok": true, "applied": map[string]any{target: applied}, "previous": old}
		return nil
	})
	return out, err
}

// advisorBundle gathers the starter context for one device. The Go bundle
// carries the same facts as _build_advisor_bundle but is rendered as JSON.
func (s *Server) advisorBundle(ctx context.Context, sn string) map[string]any {
	now := time.Now().Unix()
	main, pack, capacity, _ := s.capacityHints(sn)
	cached, _ := s.Store.Get("devices", sn)
	dev, _ := cached["device"].(map[string]any)
	tele, _ := cached["telemetry"].(map[string]any)
	cfg, _ := s.smartConfig(sn)
	params, _ := s.resolveDeviceParams(ctx, sn, 14*24)
	accuracy, _ := s.Store.PredictionAccuracy(sn, store.AccuracyOptions{Now: now, MainCapacity: main, PackCapacity: pack})
	post := []map[string]any{}
	for _, r := range accuracy {
		if forecast.Number(r, "made_at") >= 1790184600 {
			post = append(post, r)
		}
	}
	decisions := s.advisorQuery(sn, "query_decisions", map[string]any{"start_iso": isoTS(float64(now - 2*86400)), "end_iso": isoTS(float64(now + 1))})
	daily, _ := s.Store.DailySummary(sn, now, 7)
	changes, _ := s.Store.List("changes", sn, 0, 0)
	if len(changes) > 20 {
		changes = changes[len(changes)-20:]
	}
	totals := map[string]any{}
	if minutes, err := s.Store.EnergyMinutes(sn, float64(now-8*86400)); err == nil {
		totals = store.EnergyTotals(minutes, float64(now), s.tzOffset())
		delete(totals, "lifetime")
	}
	tunables := map[string]any{}
	for _, t := range []string{"smart_charge.max_charge_w", "smart_charge.target_sunrise_soc_pct", "smart_charge.max_on_duration_minutes"} {
		if r, ok := advisor.AllowedTarget(t); ok {
			tunables[t] = r
		}
	}
	return map[string]any{"device_sn": sn, "generated_at": isoTS(float64(now)), "device": dev, "main_soc_pct": tele["battery_percent"], "capacity_wh": s.resolvedCapacity(sn, capacity), "pack_count": s.packCount(sn), "smart_charge_config": cfg, "device_params": params, "accuracy_14d": forecast.BucketAccuracy(accuracy), "accuracy_post_fix": forecast.BucketAccuracy(post), "recent_decisions": decisions["rows"], "daily_summary": daily, "energy_totals": totals, "recent_changes": changes, "tunables": tunables, "tz_offset_seconds": s.tzOffset()}
}
func renderBundle(b map[string]any) string {
	data, _ := json.MarshalIndent(b, "", " ")
	return "Starter context for today's review (JSON). Use the query_* tools for detail, then call submit_algorithm_review exactly once.\n\n" + string(data)
}

// runReview ports claude_advisor.review plus _run_advisor_review persistence.
func (s *Server) runReview(ctx context.Context, sn string) (map[string]any, error) {
	agent, key := s.aiAgent("advisor", "")
	if key == "" {
		return map[string]any{"ok": false, "reason": "no_api_key"}, nil
	}
	prefs := s.aiPrefs()
	c := advisor.Prompts()
	agent.MaxTurns, agent.MaxTokens, agent.Limit = c.MaxTurns, c.MaxTokens, 80000
	if agent.Provider != "openai" {
		agent.Thinking = str(prefs, "advisor_thinking_effort")
		if prefs["advisor_1m_context"] != false {
			agent.Headers = map[string]string{"anthropic-beta": "context-1m-2025-08-07"}
		}
	}
	var final map[string]any
	tools := append(append([]assistant.Tool{}, c.QueryTools...), c.ReviewTool)
	res, err := agent.Run(ctx, c.System, []assistant.Turn{{Role: "user", Content: renderBundle(s.advisorBundle(ctx, sn))}}, tools, func(_ context.Context, call assistant.Call) (any, assistant.Action) {
		if call.Name == c.ReviewTool.Name {
			final = call.Args
			return nil, assistant.StopNow
		}
		return s.advisorQuery(sn, call.Name, call.Args), assistant.Continue
	})
	if err != nil {
		msg := err.Error()
		if len(msg) > 300 {
			msg = msg[:300] + "…"
		}
		return map[string]any{"ok": false, "reason": "api_error: " + msg}, nil
	}
	if final == nil {
		final = map[string]any{"summary": res.Text}
	}
	ids := []int64{}
	err = s.Store.App.RunInTransaction(func(app core.App) error {
		st := &store.Store{App: app}
		if err := expireSuggestions(st, float64(time.Now().Unix())); err != nil {
			return err
		}
		list, _ := final["config_suggestions"].([]any)
		for _, raw := range list {
			sg, _ := raw.(map[string]any)
			if sg == nil || advisor.ValidateProposal(str(sg, "target"), sg["proposed_value"]) != nil {
				continue
			}
			id, err := s.insertSuggestion(st, sn, map[string]any{"kind": "config", "target": sg["target"], "current_value": sg["current_value"], "proposed_value": sg["proposed_value"], "reasoning": sg["reasoning"], "confidence": sg["confidence"], "severity": nil})
			if err != nil {
				return err
			}
			ids = append(ids, id)
		}
		anomalies, _ := final["anomalies"].([]any)
		for _, raw := range anomalies {
			a, _ := raw.(map[string]any)
			if a == nil {
				continue
			}
			id, err := s.insertSuggestion(st, sn, map[string]any{"kind": "anomaly", "target": nil, "current_value": nil, "proposed_value": nil, "reasoning": str(a, "description"), "confidence": nil, "severity": a["severity"]})
			if err != nil {
				return err
			}
			ids = append(ids, id)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "summary": str(final, "summary"), "new_suggestion_ids": ids, "model": agent.Model, "turns": res.Turns, "tool_calls": res.Calls}, nil
}
func (s *Server) advisorJob(sn string) {
	rt := &s.advisor
	rt.mu.Lock()
	if rt.jobs == nil {
		rt.jobs = map[string]map[string]any{}
	}
	job := map[string]any{"status": "running", "started_at": float64(time.Now().UnixNano()) / 1e9, "finished_at": nil, "result": nil, "error": nil}
	rt.jobs[sn] = job
	rt.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	result, err := s.runReview(ctx, sn)
	rt.mu.Lock()
	defer rt.mu.Unlock()
	job["finished_at"] = float64(time.Now().UnixNano()) / 1e9
	switch {
	case err != nil:
		job["status"], job["error"] = "error", err.Error()
	case result["ok"] == true:
		job["status"], job["result"] = "done", result
	default:
		job["status"], job["error"] = "error", fmt.Sprint(result["reason"])
	}
}

// AdvisorLoop runs one review per device per day at advisor_trigger_hour
// local time (hourly ticks, 23 h minimum spacing).
func (s *Server) AdvisorLoop(ctx context.Context) {
	select {
	case <-ctx.Done():
		return
	case <-time.After(60 * time.Second):
	}
	for {
		s.advisorTick(ctx, time.Now())
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Hour):
		}
	}
}
func (s *Server) advisorTick(ctx context.Context, now time.Time) int {
	if _, key := s.aiAgent("advisor", ""); key == "" {
		return 0
	}
	values, _, err := s.settingValues()
	if err != nil {
		return 0
	}
	hour := int(((now.Unix()+int64(s.tzOffset()))%86400+86400)%86400) / 3600
	if float64(hour) != forecast.Number(values, "advisor_trigger_hour") {
		return 0
	}
	rows, _ := s.Store.List("devices", "", 0, 0)
	ran := 0
	for _, row := range rows {
		d, _ := row["device"].(map[string]any)
		sn := str(d, "device_sn")
		if sn == "" {
			continue
		}
		s.advisor.mu.Lock()
		if s.advisor.lastRun == nil {
			s.advisor.lastRun = map[string]float64{}
		}
		last := s.advisor.lastRun[sn]
		s.advisor.mu.Unlock()
		if float64(now.Unix())-last < 23*3600 {
			continue
		}
		if _, err := s.runReview(ctx, sn); err != nil {
			s.Store.App.Logger().Warn("advisor review", "device", sn, "error", err)
		}
		s.advisor.mu.Lock()
		s.advisor.lastRun[sn] = float64(now.Unix())
		s.advisor.mu.Unlock()
		ran++
	}
	return ran
}
func (s *Server) advisorRoutes(w http.ResponseWriter, r *http.Request) bool {
	p, m := r.URL.Path, r.Method
	if !strings.HasPrefix(p, "/api/algorithm/") {
		return false
	}
	sn := r.URL.Query().Get("device_sn")
	if sn == "" {
		sn = s.activeSerial()
	}
	need := func() bool {
		if sn == "" {
			fail(w, 400, "no active device")
			return false
		}
		return true
	}
	switch {
	case p == "/api/algorithm/suggestions" && m == "GET":
		status := "pending"
		if q, ok := r.URL.Query()["status"]; ok {
			status = q[0]
			if status == "null" || status == "all" {
				status = ""
			}
		}
		rows, err := s.suggestions(s.Store, sn, status, 100)
		if err != nil {
			storageError(w, err)
			return true
		}
		jsonOut(w, 200, map[string]any{"device_sn": nilEmpty(sn), "status_filter": nilEmpty(status), "suggestions": rows})
	case p == "/api/algorithm/review_now" && m == "POST":
		if !need() {
			return true
		}
		s.advisor.mu.Lock()
		job := s.advisor.jobs[sn]
		running := job != nil && job["status"] == "running"
		s.advisor.mu.Unlock()
		if running {
			jsonOut(w, 202, map[string]any{"status": "running", "device_sn": sn, "started_at": job["started_at"], "already_running": true})
			return true
		}
		started := float64(time.Now().UnixNano()) / 1e9
		go s.advisorJob(sn)
		jsonOut(w, 202, map[string]any{"status": "running", "device_sn": sn, "started_at": started, "already_running": false})
	case p == "/api/algorithm/review_status" && m == "GET":
		if !need() {
			return true
		}
		s.advisor.mu.Lock()
		job := s.advisor.jobs[sn]
		out := map[string]any{"device_sn": sn, "status": "idle"}
		for k, v := range job {
			out[k] = v
		}
		if job != nil && job["status"] == "running" {
			out["elapsed_s"] = math.Round((float64(time.Now().UnixNano())/1e9-forecast.Number(job, "started_at"))*10) / 10
		}
		s.advisor.mu.Unlock()
		jsonOut(w, 200, out)
	case strings.HasPrefix(p, "/api/algorithm/suggestions/") && m == "POST":
		rest := strings.TrimPrefix(p, "/api/algorithm/suggestions/")
		idText, action, _ := strings.Cut(rest, "/")
		id, err := strconv.ParseInt(idText, 10, 64)
		if err != nil {
			fail(w, 422, "suggestion_id must be an integer")
			return true
		}
		switch action {
		case "apply":
			out, err := s.applySuggestion(id)
			var he httpErr
			if errors.As(err, &he) {
				fail(w, he.code, he.msg)
			} else if err != nil {
				storageError(w, err)
			} else {
				jsonOut(w, 200, out)
			}
		case "dismiss":
			var out map[string]any
			err := s.Store.App.RunInTransaction(func(app core.App) error {
				st := &store.Store{App: app}
				sug, key, err := findByID(st, "suggestions", id)
				if err != nil {
					return err
				}
				if sug == nil {
					return httpErr{404, "suggestion not found"}
				}
				if str(sug, "status") != "pending" {
					out = map[string]any{"ok": true, "already": sug["status"]}
					return nil
				}
				now := float64(time.Now().Unix())
				sug["status"], sug["decided_at"], sug["decided_by"] = "dismissed", now, "user"
				out = map[string]any{"ok": true}
				return st.Put("suggestions", key, str(sug, "device_sn"), sug)
			})
			var he httpErr
			if errors.As(err, &he) {
				fail(w, he.code, he.msg)
			} else if err != nil {
				storageError(w, err)
			} else {
				jsonOut(w, 200, out)
			}
		default:
			fail(w, 404, "not found")
		}
	case p == "/api/algorithm/preview" && m == "GET":
		if !need() {
			return true
		}
		bundle := s.advisorBundle(r.Context(), sn)
		agent, _ := s.aiAgent("advisor", "")
		prefs := s.aiPrefs()
		effort := prefs["advisor_thinking_effort"]
		if agent.Provider == "openai" {
			effort = prefs["openai_advisor_effort"]
		}
		jsonOut(w, 200, map[string]any{"device_sn": sn, "rendered": renderBundle(bundle), "provider": agent.Provider, "model": agent.Model, "thinking_budget": 16000, "reasoning_effort": effort, "raw_bundle": bundle})
	case p == "/api/algorithm/changes" && m == "GET":
		rows, err := s.Store.List("changes", "", 0, 0)
		if err != nil {
			storageError(w, err)
			return true
		}
		out := []map[string]any{}
		for _, r := range rows {
			if sn == "" || r["device_sn"] == nil || str(r, "device_sn") == sn || str(r, "device_sn") == "" {
				out = append(out, r)
			}
		}
		sort.SliceStable(out, func(i, j int) bool {
			return forecast.Number(out[i], "applied_at") > forecast.Number(out[j], "applied_at")
		})
		jsonOut(w, 200, map[string]any{"device_sn": nilEmpty(sn), "changes": out[:min(50, len(out))]})
	default:
		return false
	}
	return true
}
