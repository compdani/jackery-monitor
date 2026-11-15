package server

import (
	_ "embed"
	"encoding/json"
	"github.com/compdani/jackery-monitor/internal/assistant"
	"net/http"
	"os"
	"strings"
)

//go:embed contracts/ai_prefs.json
var aiPrefsJSON []byte

func (s *Server) aiPreferences(w http.ResponseWriter, r *http.Request, b map[string]any, providerOnly bool) bool {
	prefs := map[string]any{}
	_ = json.Unmarshal(aiPrefsJSON, &prefs)
	saved, err := s.Store.Get("settings", "anthropic_prefs")
	if err != nil && !isMissing(err) {
		storageError(w, err)
		return true
	}
	for k, v := range saved {
		prefs[k] = v
	}
	active, err := s.Store.Get("settings", "ai_provider")
	if err != nil && !isMissing(err) {
		storageError(w, err)
		return true
	}
	if str(active, "provider") != "" {
		prefs["provider"] = active["provider"]
	}
	if r.Method == "POST" {
		known := false
		for k, v := range b {
			if _, ok := prefs[k]; ok && v != nil {
				known = true
			}
		}
		if !providerOnly && !known {
			fail(w, 400, "at least one preference field required")
			return true
		}
		if providerOnly && str(b, "provider") != "openai" && str(b, "provider") != "anthropic" {
			fail(w, 400, "invalid provider")
			return true
		}
		for k, v := range b {
			if providerOnly && k != "provider" {
				continue
			}
			if _, known := prefs[k]; !known {
				continue
			}
			switch k {
			case "provider":
				if v != "anthropic" && v != "openai" {
					continue
				}
			case "openai_compat_preset":
				if v != "openai" && v != "openrouter" && v != "groq" && v != "ollama" && v != "custom" {
					continue
				}
			case "advisor_thinking_effort", "openai_advisor_effort":
				if v != "low" && v != "medium" && v != "high" {
					continue
				}
			case "advisor_1m_context":
				if _, ok := v.(bool); !ok {
					continue
				}
			default:
				if _, ok := v.(string); !ok {
					continue
				}
			}
			prefs[k] = v
		}
		if err := s.Store.Put("settings", "anthropic_prefs", "", prefs); err != nil {
			storageError(w, err)
			return true
		}
		if err := s.Store.Put("settings", "ai_provider", "", map[string]any{"provider": prefs["provider"]}); err != nil {
			storageError(w, err)
			return true
		}
	} else if r.Method != "GET" {
		fail(w, 405, "method not allowed")
		return true
	}
	if !providerOnly {
		jsonOut(w, 200, prefs)
		return true
	}
	out := map[string]any{"provider": prefs["provider"], "openai_compat_preset": compatPreset(prefs), "openai_base_url": str(prefs, "openai_base_url"), "openai_resolved_base_url": assistant.ResolveBaseURL(compatPreset(prefs), str(prefs, "openai_base_url"))}
	for _, provider := range []string{"anthropic", "openai"} {
		secret, err := s.Store.Secret(s.Key, provider)
		if err != nil && !isMissing(err) {
			storageError(w, err)
			return true
		}
		out[provider+"_has_key"] = str(secret, "api_key") != "" || os.Getenv(strings.ToUpper(provider)+"_API_KEY") != ""
	}
	if r.Method == "POST" {
		out["ok"] = true
	}
	jsonOut(w, 200, out)
	return true
}
