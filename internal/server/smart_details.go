package server

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"github.com/compdani/jackery-monitor/internal/assistant"
	"github.com/compdani/jackery-monitor/internal/automation"
	"github.com/compdani/jackery-monitor/internal/forecast"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

//go:embed contracts/device_params.json
var deviceParamMetadata []byte

func (s *Server) smartNarration(ctx context.Context, cfg automation.SmartSettings, p *automation.ChargePlan) string {
	if !cfg.Narrate || p == nil {
		return ""
	}
	prefs := map[string]any{}
	_ = json.Unmarshal(aiPrefsJSON, &prefs)
	saved, _ := s.Store.Get("settings", "anthropic_prefs")
	for k, v := range saved {
		prefs[k] = v
	}
	active, _ := s.Store.Get("settings", "ai_provider")
	provider := str(active, "provider")
	if provider == "" {
		provider = str(prefs, "provider")
	}
	secret, err := s.Store.Secret(s.Key, provider)
	if err != nil && !isMissing(err) {
		return ""
	}
	key := str(secret, "api_key")
	if key == "" {
		key = os.Getenv(strings.ToUpper(provider) + "_API_KEY")
	}
	if key == "" {
		return ""
	}
	facts, _ := json.Marshal(p)
	system := "Explain this solar-battery automation decision in 1-2 plain English sentences for a homeowner. Lead with the action and reason. Mention relevant numbers only when they clarify the decision."
	url := "https://api.anthropic.com/v1/messages"
	body := map[string]any{"model": prefs["narrator_model"], "max_tokens": 200, "system": system, "messages": []assistant.Message{{Role: "user", Content: string(facts)}}}
	if provider == "openai" {
		url = assistant.ResolveBaseURL(compatPreset(prefs), str(prefs, "openai_base_url")) + "/chat/completions"
		body = map[string]any{"model": prefs["openai_narrator_model"], "max_completion_tokens": 200, "messages": []assistant.Message{{Role: "system", Content: system}, {Role: "user", Content: string(facts)}}}
	}
	data, _ := json.Marshal(body)
	bounded, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(bounded, "POST", url, bytes.NewReader(data))
	if err != nil {
		return ""
	}
	req.Header.Set("Content-Type", "application/json")
	if provider == "openai" {
		req.Header.Set("Authorization", "Bearer "+key)
	} else {
		req.Header.Set("x-api-key", key)
		req.Header.Set("anthropic-version", "2023-06-01")
	}
	client := s.AIHTTP
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return ""
	}
	var result struct {
		Content []struct{ Text string }
		Choices []struct{ Message struct{ Content string } }
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&result) != nil {
		return ""
	}
	text := ""
	if provider == "openai" {
		if len(result.Choices) > 0 {
			text = result.Choices[0].Message.Content
		}
	} else {
		for _, c := range result.Content {
			text += c.Text + " "
		}
	}
	runes := []rune(strings.TrimSpace(text))
	return string(runes[:min(512, len(runes))])
}

func (s *Server) smartResolvedParams(ctx context.Context, sn string) []map[string]any {
	metadata := map[string]map[string]any{}
	_ = json.Unmarshal(deviceParamMetadata, &metadata)
	h, w, capacity, _, offset, err := s.smartInputs(ctx, sn, time.Now().Unix())
	if err != nil {
		return []map[string]any{}
	}
	cached, _ := s.Store.Get("devices", sn)
	dev, _ := cached["device"].(map[string]any)
	packs := 0
	packRows, _ := s.Store.List("battery_packs", sn, 0, 0)
	if len(packRows) > 0 {
		items, _ := packRows[len(packRows)-1]["packs"].([]any)
		packs = len(items)
	}
	stored, _ := s.Store.List("forecast_models", sn, 0, 0)
	if forecast.Number(cached, "capacity_wh_override") <= 0 && forecast.Number(dev, "capacity_wh_override") <= 0 {
		capacity += float64(packs) * forecast.ExpansionCapacity(int(forecast.Number(dev, "model_code")))
	}
	for _, r := range stored {
		if r["key"] == "battery_capacity_wh" && r["source"] == "user" && forecast.Number(r, "value") > 0 {
			capacity = forecast.Number(r, "value")
		}
	}
	parasitic, pct, dn := forecast.FitDrainModel(h, capacity, packs)
	eff, en := forecast.FitChargeEfficiency(h, capacity, .9, 5)
	solar, n := forecast.FitSolarCoefficient(h, w)
	charge, cn := forecast.FitMaxCharge(h, w, offset, 100, 6, .95)
	source := func(n int) string {
		if n >= 5 {
			return "fit"
		}
		return "default"
	}
	values := map[string]map[string]any{
		"battery_capacity_wh":   {"value": capacity, "source": "catalog"},
		"max_charge_w":          {"value": 800.0, "source": "default", "n_samples": cn},
		"parasitic_w":           {"value": parasitic, "source": source(dn), "n_samples": dn},
		"inverter_overhead_pct": {"value": pct, "source": source(dn), "n_samples": dn},
		"charge_efficiency":     {"value": eff, "source": source(en), "n_samples": en},
		"solar_coefficient":     {"value": solar, "source": source(n), "n_samples": n},
	}
	if n >= 2 {
		values["solar_coefficient"]["source"] = "fit"
	}
	if forecast.Number(cached, "capacity_wh_override") > 0 || forecast.Number(dev, "capacity_wh_override") > 0 {
		values["battery_capacity_wh"]["source"] = "user"
	}
	if forecast.Number(dev, "model_code") == 0 && values["battery_capacity_wh"]["source"] != "user" {
		values["battery_capacity_wh"]["source"] = "default"
	}
	if charge != nil {
		values["max_charge_w"]["value"], values["max_charge_w"]["source"] = *charge, "fit"
	}
	raw, _ := s.Store.Get("settings", "smart_charge")
	if by, ok := raw["by_device"].(map[string]any); ok {
		raw, _ = by[sn].(map[string]any)
	}
	if forecast.Number(raw, "max_charge_w") > 0 {
		values["max_charge_w"] = map[string]any{"value": raw["max_charge_w"], "source": "user", "note": "from smart_charge config (Automation tab)"}
	}
	for _, r := range stored {
		if r["_legacy_table"] == "device_params" && r["source"] == "user" && r["value"] != nil {
			if _, ok := values[str(r, "key")]; ok {
				values[str(r, "key")] = r
			}
		}
	}
	out := []map[string]any{}
	for _, key := range []string{"battery_capacity_wh", "max_charge_w", "inverter_overhead_pct", "parasitic_w", "charge_efficiency", "solar_coefficient"} {
		row := map[string]any{"key": key}
		for k, v := range metadata[key] {
			row[k] = v
		}
		for k, v := range values[key] {
			row[k] = v
		}
		out = append(out, row)
	}
	return out
}
