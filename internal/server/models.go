package server

import (
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/compdani/jackery-monitor/internal/assistant"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

func compatPreset(p map[string]any) string {
	if v := str(p, "openai_compat_preset"); v != "" {
		return v
	}
	if v := str(p, "openai_preset"); v != "" {
		return v
	}
	return "openai"
}
func (s *Server) modelRoutes(w http.ResponseWriter, r *http.Request) bool {
	path := r.URL.Path
	if path != "/api/openai/compat" && path != "/api/openai/models" && path != "/api/anthropic/models" {
		return false
	}
	if r.Method != "GET" {
		fail(w, 405, "method not allowed")
		return true
	}
	prefs, _ := s.Store.Get("settings", "anthropic_prefs")
	preset := compatPreset(prefs)
	base := assistant.ResolveBaseURL(preset, str(prefs, "openai_base_url"))
	if path == "/api/openai/compat" {
		var presets map[string]map[string]any
		_ = json.Unmarshal(compatJSON, &presets)
		list := []any{}
		for _, id := range []string{"openai", "openrouter", "groq", "ollama", "custom"} {
			p := presets[id]
			p["id"] = id
			if p["base_url"] == nil {
				p["base_url"] = ""
			}
			list = append(list, p)
		}
		jsonOut(w, 200, map[string]any{"presets": list, "preset": preset, "base_url": str(prefs, "openai_base_url"), "resolved_base_url": base})
		return true
	}
	provider := "openai"
	models := []map[string]any{}
	ids := []string{"o4-mini", "o3", "gpt-4.1", "gpt-4o", "gpt-4o-mini"}
	if path == "/api/anthropic/models" {
		provider = "anthropic"
		base = "https://api.anthropic.com/v1"
		ids = []string{"claude-opus-4-7", "claude-sonnet-4-7", "claude-haiku-4-5"}
	}
	for _, id := range ids {
		models = append(models, map[string]any{"id": id, "display_name": id})
	}
	secret, err := s.Store.Secret(s.Key, provider)
	if err != nil && !isMissing(err) {
		storageError(w, err)
		return true
	}
	key := str(secret, "api_key")
	if key == "" {
		key = os.Getenv(strings.ToUpper(provider) + "_API_KEY")
	}
	source := "fallback_no_key"
	if key != "" {
		source = "fallback_fetch_failed"
		req, err := http.NewRequestWithContext(r.Context(), "GET", strings.TrimRight(base, "/")+"/models", nil)
		if err == nil {
			req.Header.Set("Authorization", "Bearer "+key)
			if provider == "anthropic" {
				req.Header.Del("Authorization")
				req.Header.Set("x-api-key", key)
				req.Header.Set("anthropic-version", "2023-06-01")
			}
			client := s.AIHTTP
			if client == nil {
				client = &http.Client{Timeout: 15 * time.Second}
			}
			resp, err := client.Do(req)
			if err == nil {
				var result struct {
					Data []map[string]any `json:"data"`
				}
				err = json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(&result)
				resp.Body.Close()
				if err == nil && resp.StatusCode == 200 {
					live := []map[string]any{}
					for _, m := range result.Data {
						id := str(m, "id")
						if id == "" {
							continue
						}
						if provider == "openai" && preset == "openai" && !strings.HasPrefix(id, "gpt-") && !strings.HasPrefix(id, "o1") && !strings.HasPrefix(id, "o3") && !strings.HasPrefix(id, "o4") && !strings.HasPrefix(id, "chatgpt-") {
							continue
						}
						if str(m, "display_name") == "" {
							m["display_name"] = id
						}
						live = append(live, m)
						if len(live) >= 500 {
							break
						}
					}
					if len(live) > 0 {
						models = live
						source = "live"
					} else {
						source = "fallback_empty"
					}
				}
			}
		}
	}
	if provider == "anthropic" {
		patterns := os.Getenv("JACKERY_1M_MODEL_PATTERNS")
		if patterns == "" {
			patterns = "opus"
		}
		for _, m := range models {
			m["supports_1m"] = false
			for _, p := range strings.Split(patterns, ",") {
				if p = strings.TrimSpace(p); p != "" && strings.Contains(strings.ToLower(str(m, "id")), strings.ToLower(p)) {
					m["supports_1m"] = true
				}
			}
		}
	}
	out := map[string]any{"models": models, "source": source}
	if provider == "openai" {
		out["preset"] = preset
		out["base_url"] = base
	}
	jsonOut(w, 200, out)
	return true
}

func isMissing(err error) bool { return errors.Is(err, sql.ErrNoRows) }
