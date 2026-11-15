package server

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"strings"
	"time"

	"github.com/compdani/jackery-monitor/internal/advisor"
	"github.com/compdani/jackery-monitor/internal/assistant"
	"github.com/compdani/jackery-monitor/internal/automation"
)

// aiPrefs merges defaults, saved preferences and the active provider.
func (s *Server) aiPrefs() map[string]any {
	prefs := map[string]any{}
	_ = json.Unmarshal(aiPrefsJSON, &prefs)
	saved, _ := s.Store.Get("settings", "anthropic_prefs")
	for k, v := range saved {
		prefs[k] = v
	}
	if active, _ := s.Store.Get("settings", "ai_provider"); str(active, "provider") != "" {
		prefs["provider"] = active["provider"]
	}
	if p := str(prefs, "provider"); p != "anthropic" && p != "openai" {
		prefs["provider"] = "anthropic"
	}
	return prefs
}

// providerKey ports _resolve_key: the saved key wins, then the env var.
func (s *Server) providerKey(provider string) string {
	secret, err := s.Store.Secret(s.Key, provider)
	if err == nil && strings.TrimSpace(str(secret, "api_key")) != "" {
		return strings.TrimSpace(str(secret, "api_key"))
	}
	return os.Getenv(strings.ToUpper(provider) + "_API_KEY")
}

// aiAgent resolves anthropic_prefs.get_model(role) for the active provider.
// role is "advisor" (assistant chat and advisor reviews) or "narrator".
func (s *Server) aiAgent(role, model string) (*assistant.Agent, string) {
	prefs := s.aiPrefs()
	provider := str(prefs, "provider")
	key := s.providerKey(provider)
	if model == "" {
		if provider == "openai" {
			model = str(prefs, "openai_"+role+"_model")
		} else {
			model = str(prefs, role+"_model")
		}
	}
	a := &assistant.Agent{Provider: provider, APIKey: key, Model: model, HTTP: s.AIHTTP}
	if provider == "openai" {
		a.BaseURL = assistant.ResolveBaseURL(compatPreset(prefs), str(prefs, "openai_base_url"))
	} else if s.AnthropicBaseURL != "" {
		a.BaseURL = s.AnthropicBaseURL
	}
	return a, key
}

// validateKey ports claude_narrator.validate_key: prefix checks, then a
// one-token call against the narrator model.
func (s *Server) validateKey(ctx context.Context, provider, key string) error {
	prefs := s.aiPrefs()
	if provider == "anthropic" && !strings.HasPrefix(key, "sk-ant-") {
		return fmt.Errorf("Anthropic API keys start with `sk-ant-`. Check the value you pasted.")
	}
	official := compatPreset(prefs) == "openai" && strings.TrimSpace(str(prefs, "openai_base_url")) == ""
	if provider == "openai" && official && !strings.HasPrefix(key, "sk-") {
		return fmt.Errorf("OpenAI API keys start with `sk-`. Check the value you pasted.")
	}
	model := str(prefs, "narrator_model")
	a := &assistant.Agent{Provider: provider, APIKey: key, HTTP: s.AIHTTP, MaxTokens: 1, MaxTurns: 1}
	if provider == "openai" {
		model = str(prefs, "openai_narrator_model")
		a.BaseURL = assistant.ResolveBaseURL(compatPreset(prefs), str(prefs, "openai_base_url"))
	} else if s.AnthropicBaseURL != "" {
		a.BaseURL = s.AnthropicBaseURL
	}
	a.Model = model
	call, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	_, err := a.Run(call, "", []assistant.Turn{{Role: "user", Content: "ok"}}, nil, nil)
	if err != nil {
		msg := err.Error()
		if len(msg) > 200 {
			msg = msg[:200] + "…"
		}
		return fmt.Errorf("%s", msg)
	}
	return nil
}

// planPrompt ports claude_narrator._plan_to_prompt.
func planPrompt(p *automation.ChargePlan) string {
	d := map[string]any{}
	data, _ := json.Marshal(p)
	_ = json.Unmarshal(data, &d)
	action := strings.ToUpper(str(d, "action"))
	if action == "" {
		action = "?"
	}
	mode := str(d, "mode")
	if mode == "" {
		mode = "?"
	}
	reason := str(d, "reason")
	if reason == "" {
		reason = "n/a"
	}
	lines := []string{"Action chosen: " + action, "Mode: " + mode, "Engine reason: " + reason}
	r1 := func(v any) string { f, _ := v.(float64); return fmt.Sprint(math.Round(f*10) / 10) }
	if d["current_soc_pct"] != nil {
		lines = append(lines, "Current SOC: "+r1(d["current_soc_pct"])+"%")
	}
	if d["predicted_sunrise_soc_pct"] != nil {
		lines = append(lines, "Predicted SOC at next sunrise: "+r1(d["predicted_sunrise_soc_pct"])+"%")
	}
	if d["target_sunrise_soc_pct"] != nil {
		lines = append(lines, "Target SOC at sunrise: "+r1(d["target_sunrise_soc_pct"])+"%")
	}
	if v, _ := d["deficit_kwh"].(float64); v != 0 {
		lines = append(lines, fmt.Sprintf("Energy deficit: %v kWh", math.Round(v*100)/100))
	}
	if v, ok := d["cheapest_rate"].(float64); ok {
		lines = append(lines, fmt.Sprintf("Cheapest rate in charge window: $%.3f/kWh", v))
	}
	return strings.Join(lines, "\n")
}

// narrate ports claude_narrator.narrate_smart_charge. Any failure returns ""
// because narration is purely additive.
func (s *Server) narrate(ctx context.Context, p *automation.ChargePlan) string {
	a, key := s.aiAgent("narrator", "")
	if key == "" || p == nil {
		return ""
	}
	a.MaxTokens, a.MaxTurns = 200, 1
	call, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	res, err := a.Run(call, advisor.Prompts().NarratorSystem, []assistant.Turn{{Role: "user", Content: planPrompt(p)}}, nil, nil)
	if err != nil {
		return ""
	}
	runes := []rune(strings.TrimSpace(res.Text))
	return string(runes[:min(512, len(runes))])
}
