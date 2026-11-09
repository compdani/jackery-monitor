package assistant

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type Anthropic struct {
	BaseURL, APIKey, Model string
	HTTP                   *http.Client
}

func (c *Anthropic) Complete(ctx context.Context, messages []Message) (string, error) {
	system := []string{}
	chat := []Message{}
	for _, m := range messages {
		if m.Role == "system" {
			system = append(system, m.Content)
		} else {
			chat = append(chat, m)
		}
	}
	b, e := json.Marshal(map[string]any{"model": c.Model, "max_tokens": 4096, "system": strings.Join(system, "\n"), "messages": chat})
	if e != nil {
		return "", e
	}
	base := c.BaseURL
	if base == "" {
		base = "https://api.anthropic.com/v1"
	}
	req, e := http.NewRequestWithContext(ctx, "POST", strings.TrimRight(base, "/")+"/messages", bytes.NewReader(b))
	if e != nil {
		return "", e
	}
	req.Header.Set("x-api-key", c.APIKey)
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("Content-Type", "application/json")
	h := c.HTTP
	if h == nil {
		h = &http.Client{Timeout: 90 * time.Second}
	}
	resp, e := h.Do(req)
	if e != nil {
		return "", e
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("AI provider returned HTTP %d", resp.StatusCode)
	}
	var out struct{ Content []struct{ Type, Text string } }
	if e = json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&out); e != nil {
		return "", e
	}
	parts := []string{}
	for _, p := range out.Content {
		if p.Type == "text" {
			parts = append(parts, p.Text)
		}
	}
	if len(parts) == 0 {
		return "", fmt.Errorf("AI provider returned no text")
	}
	return strings.Join(parts, "\n"), nil
}
func ResolveBaseURL(preset, explicit string) string {
	if s := strings.TrimRight(strings.TrimSpace(explicit), "/"); s != "" {
		return s
	}
	switch preset {
	case "openrouter":
		return "https://openrouter.ai/api/v1"
	case "groq":
		return "https://api.groq.com/openai/v1"
	case "ollama":
		return "http://host.docker.internal:11434/v1"
	default:
		return "https://api.openai.com/v1"
	}
}
