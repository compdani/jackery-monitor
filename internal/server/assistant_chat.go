package server

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/compdani/jackery-monitor/internal/assistant"
	"github.com/compdani/jackery-monitor/internal/automation"
	"github.com/compdani/jackery-monitor/internal/forecast"
	"github.com/compdani/jackery-monitor/internal/store"
	"github.com/pocketbase/pocketbase/core"
)

const maxAttachmentBytes = 10 << 20

var secretKeyRE = regexp.MustCompile(`(?i)(password|passwd|secret|token|api[_-]?key|credential|authorization|cookie|session|smb|ssh)`)

// redactSecrets ports assistant_routes._redact_secrets.
func redactSecrets(v any, depth int) any {
	if depth > 8 {
		return nil
	}
	switch x := v.(type) {
	case map[string]any:
		out := map[string]any{}
		for k, val := range x {
			if secretKeyRE.MatchString(k) {
				if _, ok := val.(bool); ok || strings.HasPrefix(strings.ToLower(k), "has_") {
					out[k] = val
				} else {
					out[k] = "[redacted]"
				}
				continue
			}
			out[k] = redactSecrets(val, depth+1)
		}
		return out
	case []any:
		out := []any{}
		for i, val := range x {
			if i >= 500 {
				break
			}
			out = append(out, redactSecrets(val, depth+1))
		}
		return out
	case []map[string]any:
		list := make([]any, len(x))
		for i := range x {
			list[i] = x[i]
		}
		return redactSecrets(list, depth)
	default:
		if v == nil {
			return nil
		}
		data, err := json.Marshal(v)
		if err != nil {
			return v
		}
		var generic any
		if json.Unmarshal(data, &generic) != nil {
			return v
		}
		switch generic.(type) {
		case map[string]any, []any:
			return redactSecrets(generic, depth)
		}
		return v
	}
}

func (s *Server) systemPrompt(tab string, context any) string {
	if tab == "" {
		tab = "live"
	}
	base := "You are the Solar Pow Monitor AI Assistant. Help the user understand energy usage, forecast SOC, and plan loads to maximize solar.\nBe concise and concrete. When proposing writes, explain why.\nThe user is currently on the '" + tab + "' tab.\n"
	if tab == "forecast" || tab == "/forecast" {
		base += "Focus on the calendar load schedule vs solar forecast: suggest shifting heavy one-off or weekly events into high-GHI hours, and flag depletion risk. Use schedule_upsert/schedule_delete tools.\n"
	}
	if context != nil {
		data, _ := json.Marshal(context)
		text := string(data)
		if len(text) > 12000 {
			text = text[:12000]
		}
		base += "\nTab context JSON:\n" + text
	}
	return base
}

// patchSmart merges fields into the per-device smart-charge config.
func (s *Server) patchSmart(sn string, patch map[string]any) (any, error) {
	s.smart.mu.Lock()
	defer s.smart.mu.Unlock()
	raw, err := s.Store.Get("settings", "smart_charge")
	if err != nil && !isMissing(err) {
		return nil, err
	}
	by, _ := raw["by_device"].(map[string]any)
	if by == nil {
		by = map[string]any{}
		if len(raw) > 0 {
			by["__legacy__"] = raw
		}
	}
	cfg := map[string]any{}
	if cur, ok := by[sn].(map[string]any); ok {
		for k, v := range cur {
			cfg[k] = v
		}
	}
	for k, v := range patch {
		if v != nil {
			cfg[k] = v
		}
	}
	// Python's legacy `enabled` flag maps onto Go's mode field.
	if on, ok := patch["enabled"].(bool); ok && patch["mode"] == nil {
		if on && str(cfg, "mode") != "active" {
			cfg["mode"] = "test"
		} else if !on {
			cfg["mode"] = "off"
		}
	}
	by[sn] = automation.NormalizeSmart(cfg)
	if err = s.Store.Put("settings", "smart_charge", "", map[string]any{"by_device": by}); err != nil {
		return nil, err
	}
	return by[sn], nil
}
func (s *Server) patchSolar(sn string, patch map[string]any) (map[string]any, error) {
	s.solar.mu.Lock()
	defer s.solar.mu.Unlock()
	raw, err := s.Store.Get("settings", "solar_charge")
	if err != nil && !isMissing(err) {
		return nil, err
	}
	by, _ := raw["by_device"].(map[string]any)
	if by == nil {
		by = map[string]any{}
		if len(raw) > 0 {
			by["__legacy__"] = raw
		}
	}
	old, err := s.solarConfig(sn)
	if err != nil {
		return nil, err
	}
	for k, v := range patch {
		if k != "device_sn" && v != nil {
			old[k] = v
		}
	}
	by[sn] = automation.NormalizeSolar(old)
	if err = s.Store.Put("settings", "solar_charge", "", map[string]any{"by_device": by}); err != nil {
		return nil, err
	}
	return s.solarConfig(sn)
}

// assistantTool ports assistant_routes._dispatch_tool; results are redacted.
func (s *Server) assistantTool(ctx context.Context, name string, args map[string]any) (any, error) {
	sn := str(args, "device_sn")
	if sn == "" {
		sn = s.activeSerial()
	}
	now := time.Now().Unix()
	need := func() error {
		if sn == "" {
			return errors.New("device_sn required")
		}
		return nil
	}
	var out any
	switch name {
	case "get_tab_snapshot", "get_telemetry":
		st := s.status(&http.Request{URL: &url.URL{Path: "/"}, Header: http.Header{}})
		t, _ := st["telemetry"].(map[string]any)
		if t == nil {
			t = map[string]any{}
		}
		out = map[string]any{"device_sn": sn, "telemetry": t, "connection_status": st["connection_status"], "battery_percent": t["battery_percent"]}
	case "get_forecast":
		fc := s.buildForecast(ctx, sn, now, true)
		hours, _ := fc["forecast"].([]forecast.Row)
		if len(hours) == 0 && fc["ready"] != true {
			out = map[string]any{"error": "no forecast", "detail": fc["error"]}
			break
		}
		floor := 20.0
		if v, ok := numberArg(fc["low_battery_threshold"]); ok {
			floor = v
		}
		sample := []forecast.Row{}
		for i := 0; i < len(hours); i += 3 {
			sample = append(sample, hours[i])
		}
		if n := len(hours); n > 0 && (n-1)%3 != 0 {
			sample = append(sample, hours[n-1])
		}
		out = map[string]any{"ready": fc["ready"], "solar_coefficient": fc["solar_coefficient"], "solar_calibration": fc["solar_calibration"], "charge_ceiling_pct": fc["charge_ceiling_pct"], "capacity_wh": fc["capacity_wh"], "load_mode": fc["load_mode"], "overall_load_w": fc["overall_load_w"], "starting_soc_pct": fc["starting_soc_pct"], "daily": fc["daily"], "ev_divert": forecast.SummarizeEVDivert(hours, s.carLoad(sn), floor, s.tzOffset()), "forecast_sample": sample, "load_schedule": fc["load_schedule"]}
	case "get_load_schedule":
		if err := need(); err != nil {
			return map[string]any{"error": err.Error()}, nil
		}
		sched, err := s.loadSchedule(sn)
		if err != nil {
			return nil, err
		}
		learned := []map[string]any{}
		if h, _, _, err := s.energyInputs(sn, now, 14*24); err == nil {
			learned = learnedProfile(forecast.FitLoadProfile(h, now, s.carLoad(sn), 3600))
		}
		sched["device_sn"], sched["learned_profile"] = sn, learned
		out = sched
	case "get_energy_totals":
		minutes, err := s.Store.EnergyMinutes(sn, 0)
		if err != nil {
			return nil, err
		}
		t := store.EnergyTotals(minutes, float64(now), s.tzOffset())
		t["device_sn"] = sn
		out = t
	case "list_suggestions":
		rows, err := s.suggestions(s.Store, sn, "pending", 50)
		if err != nil {
			return nil, err
		}
		out = map[string]any{"suggestions": rows}
	case "get_charge_status":
		if err := need(); err != nil {
			return map[string]any{"error": err.Error()}, nil
		}
		smart, _ := s.smartConfig(sn)
		solar, _ := s.solarConfig(sn)
		history, _ := s.smartHistory(sn)
		out = map[string]any{"device_sn": sn, "smart_charge": smart, "solar_charge": solar, "recent_smart_decisions": history[:min(10, len(history))]}
	case "query_samples", "query_weather", "query_predictions", "query_decisions", "query_battery_packs":
		out = s.advisorQuery(sn, name, args)
	case "get_device_params":
		rows, err := s.resolveDeviceParams(ctx, sn, 14*24)
		if err != nil {
			return nil, err
		}
		out = map[string]any{"device_sn": sn, "params": rows, "tz_offset_seconds": s.tzOffset()}
	case "get_energy_daily":
		days := 90
		if v, ok := numberArg(args["days"]); ok {
			days = max(1, min(365, int(v)))
		}
		minutes, err := s.Store.EnergyMinutes(sn, float64(now-int64(days)*86400))
		if err != nil {
			return nil, err
		}
		out = map[string]any{"device_sn": sn, "days": days, "tz_offset_s": s.tzOffset(), "daily": store.EnergyDaily(minutes, s.tzOffset())}
	case "get_daily_summary":
		days := 7
		if v, ok := numberArg(args["days"]); ok {
			days = max(1, min(90, int(v)))
		}
		rows, err := s.Store.DailySummary(sn, now, days)
		if err != nil {
			return nil, err
		}
		out = map[string]any{"device_sn": sn, "days": days, "rows": rows}
	case "get_cost_plan":
		v, err := s.Store.Get("settings", "cost_plan")
		if isMissing(err) {
			v, err = map[string]any{"type": "flat", "rate_per_kwh": .30, "currency": "USD"}, nil
		}
		out = v
	case "get_location":
		loc, _, _ := s.savedLocation()
		out = map[string]any{"latitude": loc["latitude"], "longitude": loc["longitude"], "label": loc["label"], "timezone": loc["timezone"], "tz_offset_seconds": s.tzOffset()}
	case "get_solar_array":
		plane, ok, err := s.weatherStore().Plane(sn)
		if err != nil {
			return nil, err
		}
		var array any
		if ok {
			array = plane
		}
		out = map[string]any{"device_sn": sn, "array": array}
	case "list_automation_rules":
		rows, err := s.Store.List("automation_rules", "", 0, 0)
		if err != nil {
			return nil, err
		}
		out = map[string]any{"rules": rows}
	case "get_events":
		limit := 100
		if v, ok := numberArg(args["limit"]); ok {
			limit = max(1, min(500, int(v)))
		}
		since, _ := numberArg(args["since"])
		events, err := s.f7Events(since)
		if err != nil {
			return nil, err
		}
		if s.Bridge != nil {
			if res, err := s.Bridge.Call(ctx, "get_events", map[string]any{"limit": limit, "since": since}); err == nil {
				if rows, ok := res["events"].([]any); ok {
					for _, r := range rows {
						if m, ok := r.(map[string]any); ok {
							events = append(events, m)
						}
					}
				}
			}
		}
		sort.SliceStable(events, func(i, j int) bool { return forecast.Number(events[i], "ts") < forecast.Number(events[j], "ts") })
		out = map[string]any{"events": events[max(0, len(events)-limit):]}
	case "fit_device_param":
		key := str(args, "key")
		if key == "" {
			return map[string]any{"error": "key required"}, nil
		}
		res, status, detail := s.fitDeviceParam(ctx, sn, key, args["lookback_hours"], args["apply"] == true)
		if status != 200 {
			return map[string]any{"error": detail}, nil
		}
		out = res
	case "schedule_upsert":
		if err := need(); err != nil {
			return nil, err
		}
		rec, err := s.loadSchedule(sn)
		if err != nil {
			return nil, err
		}
		windows, _ := rec["windows"].([]any)
		kind := str(args, "kind")
		if kind == "" {
			kind = "once"
		}
		event := map[string]any{"id": args["id"], "label": str(args, "label"), "start": args["start"], "end": args["end"], "watts": args["watts"], "kind": kind, "date": args["date"], "weekdays": args["weekdays"]}
		replaced := false
		if id := str(args, "id"); id != "" {
			for i, w := range windows {
				if m, ok := w.(map[string]any); ok && str(m, "id") == id {
					for k, v := range event {
						if v != nil {
							m[k] = v
						}
					}
					windows[i], replaced = m, true
					break
				}
			}
		}
		if !replaced {
			windows = append(windows, event)
		}
		rec["mode"], rec["windows"] = "scheduled", windows
		saved := NormalizeLoadSchedule(rec)
		if err = s.Store.Put("settings", "load_schedule:"+sn, sn, saved); err != nil {
			return nil, err
		}
		out = map[string]any{"ok": true, "schedule": saved}
	case "schedule_delete":
		if err := need(); err != nil {
			return nil, err
		}
		id := str(args, "id")
		if id == "" {
			return nil, errors.New("id required")
		}
		rec, err := s.loadSchedule(sn)
		if err != nil {
			return nil, err
		}
		keep := []any{}
		list, _ := rec["windows"].([]any)
		for _, w := range list {
			if m, ok := w.(map[string]any); !ok || str(m, "id") != id {
				keep = append(keep, w)
			}
		}
		rec["windows"] = keep
		saved := NormalizeLoadSchedule(rec)
		if err = s.Store.Put("settings", "load_schedule:"+sn, sn, saved); err != nil {
			return nil, err
		}
		out = map[string]any{"ok": true, "schedule": saved}
	case "smart_charge_set":
		if err := need(); err != nil {
			return nil, err
		}
		patch := map[string]any{}
		for _, k := range []string{"enabled", "mode", "max_charge_w", "target_sunrise_soc_pct", "max_on_duration_minutes"} {
			if args[k] != nil {
				patch[k] = args[k]
			}
		}
		cfg, err := s.patchSmart(sn, patch)
		if err != nil {
			return nil, err
		}
		out = map[string]any{"ok": true, "config": cfg}
	case "solar_charge_set":
		if err := need(); err != nil {
			return nil, err
		}
		cfg, err := s.patchSolar(sn, args)
		if err != nil {
			return nil, err
		}
		out = map[string]any{"ok": true, "config": cfg}
	case "set_output":
		port := str(args, "port")
		if port != "ac" && port != "dc" && port != "usb" && port != "car" {
			return nil, errors.New("invalid port")
		}
		if s.Bridge == nil {
			return nil, errors.New("device bridge unavailable")
		}
		on := args["on"] == true
		if port == "ac" && !on {
			done := s.watchdogUserOff(sn)
			defer done()
		}
		if _, err := s.Bridge.Call(ctx, "set_output", map[string]any{"port": port, "on": on, "device_sn": sn}); err != nil {
			return nil, err
		}
		out = map[string]any{"ok": true, "port": port, "on": on}
	case "apply_suggestion":
		id, ok := numberArg(args["id"])
		if !ok {
			return nil, errors.New("id required")
		}
		res, err := s.applySuggestion(int64(id))
		if err != nil {
			return nil, err
		}
		out = res
	default:
		out = map[string]any{"error": "unknown tool " + name}
	}
	return redactSecrets(out, 0), nil
}

func (s *Server) createToolRun(st *store.Store, conv, name string, args map[string]any, message, status string) (map[string]any, error) {
	id := store.ID()
	now := float64(time.Now().Unix())
	row := map[string]any{"id": id, "conversation_id": conv, "message_id": message, "tool_name": name, "args": args, "status": status, "result": nil, "created_at": now, "decided_at": nil, "ts": now}
	return map[string]any{"id": id, "tool_name": name, "args": args, "status": status}, st.Put("tool_runs", id, conv, row)
}
func (s *Server) finishToolRun(id, status string, result any) error {
	row, err := s.Store.Get("tool_runs", id)
	if err != nil {
		return err
	}
	row["status"], row["result"], row["decided_at"] = status, result, float64(time.Now().Unix())
	return s.Store.Put("tool_runs", id, str(row, "conversation_id"), row)
}

// attachmentBlocks ports _attachment_blocks (max five per turn).
func (s *Server) attachmentBlocks(ids []string) ([]assistant.Block, []string) {
	blocks, notes := []assistant.Block{}, []string{}
	for i, id := range ids {
		if i >= 5 {
			break
		}
		meta, err := s.Store.Get("attachments", id)
		if err != nil {
			continue
		}
		data, err := os.ReadFile(str(meta, "path"))
		if err != nil {
			continue
		}
		mime, name := str(meta, "mime"), str(meta, "filename")
		b64 := base64.StdEncoding.EncodeToString(data)
		switch {
		case strings.HasPrefix(mime, "image/"):
			blocks = append(blocks, assistant.Block{Type: "image", MIME: mime, Data: b64, Filename: name})
		case strings.HasPrefix(mime, "text/"):
			text := strings.ToValidUTF8(string(data), "�")
			if utf8.RuneCountInString(text) > 20000 {
				text = string([]rune(text)[:20000])
			}
			notes = append(notes, "[file "+name+"]\n"+text)
		case mime == "application/pdf":
			blocks = append(blocks, assistant.Block{Type: "document", MIME: mime, Data: b64, Filename: name})
			notes = append(notes, "[PDF attached: "+name+"]")
		}
	}
	return blocks, notes
}

// chatTurn ports run_chat_turn: write tools become pending unless autopilot.
func (s *Server) chatTurn(ctx context.Context, conv string, b map[string]any) (map[string]any, error) {
	s.assistant.mu.Lock()
	defer s.assistant.mu.Unlock()
	agent, key := s.aiAgent("advisor", str(b, "model"))
	text := str(b, "message")
	if text == "" {
		text = str(b, "text")
	}
	ids := stringList(b["attachment_ids"])
	blocks, notes := s.attachmentBlocks(ids)
	content := strings.TrimSpace(text)
	if len(notes) > 0 {
		content = strings.TrimSpace(content + "\n\n" + strings.Join(notes, "\n\n"))
	}
	stamp := float64(time.Now().UnixNano()) / 1e9
	uid := store.ID()
	var parts any
	if len(ids) > 0 {
		parts = map[string]any{"attachment_ids": ids}
	}
	err := s.Store.App.RunInTransaction(func(app core.App) error {
		st := &store.Store{App: app}
		if err := st.Put("messages", uid, conv, map[string]any{"id": uid, "conversation_id": conv, "role": "user", "content": content, "parts": parts, "created_at": stamp, "ts": stamp}); err != nil {
			return err
		}
		for _, id := range ids {
			if att, err := st.Get("attachments", id); err == nil && str(att, "conversation_id") == conv {
				att["message_id"] = uid
				if err = st.Put("attachments", id, conv, att); err != nil {
					return err
				}
			}
		}
		c, err := st.Get("conversations", conv)
		if err != nil {
			return err
		}
		if t := str(c, "title"); (t == "" || t == "New chat") && content != "" {
			r := []rune(content)
			c["title"] = string(r[:min(60, len(r))])
		}
		c["updated_at"] = stamp
		return st.Put("conversations", conv, "", c)
	})
	if err != nil {
		return nil, err
	}
	rows, err := s.Store.List("messages", conv, 0, 0)
	if err != nil {
		return nil, err
	}
	sort.SliceStable(rows, func(i, j int) bool { return forecast.Number(rows[i], "created_at") < forecast.Number(rows[j], "created_at") })
	history := []assistant.Turn{}
	for _, r := range rows {
		history = append(history, assistant.Turn{Role: str(r, "role"), Content: str(r, "content")})
	}
	if len(history) > 0 {
		history[len(history)-1].Blocks = blocks
	}
	autopilot := b["autopilot"] == true
	pending := []map[string]any{}
	reply := ""
	if key == "" {
		if agent.Provider == "openai" {
			reply = "No OpenAI API key configured. Add one in Settings → AI."
		} else {
			reply = "No Anthropic API key configured. Add one in Settings → AI."
		}
	} else {
		agent.MaxTurns, agent.MaxTokens = 6, 4096
		res, err := agent.Run(ctx, s.systemPrompt(str(b, "tab"), b["context"]), history, assistant.Tools(), func(ctx context.Context, c assistant.Call) (any, assistant.Action) {
			if assistant.NeedsApproval(c.Name, c.Args) && !autopilot {
				run, err := s.createToolRun(s.Store, conv, c.Name, c.Args, uid, "pending")
				if err != nil {
					return map[string]any{"error": err.Error()}, assistant.Continue
				}
				pending = append(pending, run)
				return map[string]any{"status": "pending_approval", "run_id": run["id"], "message": "Waiting for user to Approve/Deny in the UI."}, assistant.StopAfterTurn
			}
			out, err := s.assistantTool(ctx, c.Name, c.Args)
			if err != nil {
				out = map[string]any{"error": err.Error()}
			}
			if assistant.IsWriteTool(c.Name) {
				if run, err := s.createToolRun(s.Store, conv, c.Name, c.Args, uid, "applied"); err == nil {
					_ = s.finishToolRun(str(run, "id"), "applied", out)
				}
			}
			return out, assistant.Continue
		})
		reply = res.Text
		if err != nil {
			reply = "Sorry — the assistant hit an error: " + err.Error()
		} else if len(pending) > 0 && reply == "" {
			reply = "I have actions ready — review and Approve or Deny below."
		} else if reply == "" {
			reply = "Done."
		}
	}
	aid := store.ID()
	at := float64(time.Now().UnixNano()) / 1e9
	msg := map[string]any{"id": aid, "conversation_id": conv, "role": "assistant", "content": reply, "parts": nil, "created_at": at, "ts": at}
	err = s.Store.App.RunInTransaction(func(app core.App) error {
		st := &store.Store{App: app}
		if err := st.Put("messages", aid, conv, msg); err != nil {
			return err
		}
		c, err := st.Get("conversations", conv)
		if err != nil {
			return err
		}
		c["updated_at"], c["model"], c["provider"] = at, agent.Model, agent.Provider
		return st.Put("conversations", conv, "", c)
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"conversation_id": conv, "text": reply, "message": msg, "pending_tools": pending}, nil
}

// assistantRoutes covers chat, tool approval and attachment endpoints.
func (s *Server) assistantRoutes(w http.ResponseWriter, r *http.Request, b map[string]any) bool {
	p, m := r.URL.Path, r.Method
	switch {
	case p == "/api/assistant/chat" && m == "POST":
		conv := str(b, "conversation_id")
		if conv == "" {
			fail(w, 400, "conversation_id required")
			return true
		}
		if _, err := s.Store.Get("conversations", conv); err != nil {
			fail(w, 400, "conversation_id required")
			return true
		}
		text := str(b, "message")
		if text == "" {
			text = str(b, "text")
		}
		if strings.TrimSpace(text) == "" && len(stringList(b["attachment_ids"])) == 0 {
			fail(w, 400, "message or attachments required")
			return true
		}
		out, err := s.chatTurn(r.Context(), conv, b)
		if err != nil {
			storageError(w, err)
			return true
		}
		jsonOut(w, 200, out)
	case strings.HasPrefix(p, "/api/assistant/tools/") && m == "POST":
		rest := strings.TrimPrefix(p, "/api/assistant/tools/")
		id, action, _ := strings.Cut(rest, "/")
		run, err := s.Store.Get("tool_runs", id)
		if isMissing(err) {
			fail(w, 404, "tool run not found")
			return true
		} else if err != nil {
			storageError(w, err)
			return true
		}
		if status := str(run, "status"); status != "pending" {
			fail(w, 400, "run is "+status)
			return true
		}
		args, _ := run["args"].(map[string]any)
		switch action {
		case "approve":
			out, err := s.assistantTool(r.Context(), str(run, "tool_name"), args)
			if err != nil {
				_ = s.finishToolRun(id, "error", map[string]any{"error": err.Error()})
				fail(w, 400, err.Error())
				return true
			}
			if err = s.finishToolRun(id, "applied", out); err != nil {
				storageError(w, err)
				return true
			}
			jsonOut(w, 200, map[string]any{"ok": true, "run_id": id, "result": out})
		case "deny":
			if err := s.finishToolRun(id, "denied", map[string]any{"denied": true}); err != nil {
				storageError(w, err)
				return true
			}
			jsonOut(w, 200, map[string]any{"ok": true})
		default:
			fail(w, 404, "not found")
		}
	case strings.HasPrefix(p, "/api/assistant/attachments/") && (m == "GET" || m == "DELETE"):
		id := strings.TrimPrefix(p, "/api/assistant/attachments/")
		meta, err := s.Store.Get("attachments", id)
		if isMissing(err) {
			fail(w, 404, "not found")
			return true
		} else if err != nil {
			storageError(w, err)
			return true
		}
		if m == "GET" {
			f, err := os.Open(str(meta, "path"))
			if err != nil {
				fail(w, 404, "not found")
				return true
			}
			defer f.Close()
			w.Header().Set("Content-Type", str(meta, "mime"))
			w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", str(meta, "filename")))
			_, _ = io.Copy(w, f)
			return true
		}
		if err := s.Store.Delete("attachments", id); err != nil {
			storageError(w, err)
			return true
		}
		s.removeUnreferenced(meta)
		jsonOut(w, 200, map[string]any{"ok": true})
	default:
		return false
	}
	return true
}

// removeUnreferenced deletes a Go-uploaded content file once no attachment
// references it. Imported legacy attachments are never removed.
func (s *Server) removeUnreferenced(meta map[string]any) {
	path := str(meta, "path")
	if path == "" || meta["legacy_path"] != nil || filepath.Dir(path) != s.attachmentDir() {
		return
	}
	rows, err := s.Store.List("attachments", "", 0, 0)
	if err != nil {
		return
	}
	for _, r := range rows {
		if str(r, "path") == path {
			return
		}
	}
	_ = os.Remove(path)
}
func (s *Server) attachmentDir() string {
	return filepath.Join(s.Store.App.DataDir(), "assistant_attachments")
}

// uploadAttachment ports POST /api/assistant/attachments (multipart form).
func (s *Server) uploadAttachment(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxAttachmentBytes+1<<20)
	if err := r.ParseMultipartForm(1 << 20); err != nil {
		fail(w, 400, "multipart form with conversation_id and file required")
		return
	}
	conv := r.FormValue("conversation_id")
	if _, err := s.Store.Get("conversations", conv); err != nil {
		fail(w, 404, "conversation not found")
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		fail(w, 400, "file required")
		return
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxAttachmentBytes+1))
	if err != nil {
		fail(w, 400, "could not read upload")
		return
	}
	if len(data) > maxAttachmentBytes {
		fail(w, 400, fmt.Sprintf("file too large (max %d bytes)", maxAttachmentBytes))
		return
	}
	mime := strings.TrimSpace(strings.Split(header.Header.Get("Content-Type"), ";")[0])
	if mime == "" {
		mime = "application/octet-stream"
	}
	if !assistant.AllowedMIME(mime) {
		fail(w, 400, "unsupported type: "+mime)
		return
	}
	dir := s.attachmentDir()
	if err = os.MkdirAll(dir, 0700); err != nil {
		storageError(w, err)
		return
	}
	sum := sha256.Sum256(data)
	digest := hex.EncodeToString(sum[:])
	dest := filepath.Join(dir, digest)
	if _, err := os.Stat(dest); os.IsNotExist(err) {
		tmp, err := os.CreateTemp(dir, ".upload-*")
		if err != nil {
			storageError(w, err)
			return
		}
		_, werr := tmp.Write(data)
		cerr := tmp.Close()
		if werr == nil && cerr == nil {
			werr = os.Link(tmp.Name(), dest)
			if os.IsExist(werr) {
				werr = nil
			}
		}
		_ = os.Remove(tmp.Name())
		if werr != nil || cerr != nil {
			storageError(w, errors.Join(werr, cerr))
			return
		}
	}
	name := header.Filename
	if name == "" {
		name = "upload"
	}
	if r := []rune(name); len(r) > 200 {
		name = string(r[:200])
	}
	id := store.ID()
	now := float64(time.Now().Unix())
	row := map[string]any{"id": id, "conversation_id": conv, "message_id": nil, "filename": name, "mime": mime, "size": len(data), "path": dest, "sha256": digest, "created_at": now, "ts": now}
	if err = s.Store.Put("attachments", id, conv, row); err != nil {
		storageError(w, err)
		return
	}
	jsonOut(w, 200, map[string]any{"id": id, "conversation_id": conv, "filename": name, "mime": mime, "size": len(data), "created_at": now})
}
