package server

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"github.com/compdani/jackery-monitor/internal/assistant"
	"github.com/compdani/jackery-monitor/internal/automation"
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
	rows, err := s.resolveDeviceParams(ctx, sn, 14*24)
	if err != nil {
		return []map[string]any{}
	}
	return rows
}
