package server

import (
	"context"
	"encoding/json"
	"github.com/compdani/jackery-monitor/internal/automation"
	"io"
	"net/http"
	"strings"
	"testing"
)

type narrationTransport func(*http.Request) (*http.Response, error)

func (f narrationTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestSmartNarrationOptionalAndProviderAware(t *testing.T) {
	for _, provider := range []string{"anthropic", "openai"} {
		t.Run(provider, func(t *testing.T) {
			s, _ := testServer(t)
			t.Setenv("ANTHROPIC_API_KEY", "")
			t.Setenv("OPENAI_API_KEY", "")
			s.Store.Put("settings", "ai_provider", "", map[string]any{"provider": provider})
			calls := 0
			s.AIHTTP = &http.Client{Transport: narrationTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				var body map[string]any
				json.NewDecoder(r.Body).Decode(&body)
				key := "max_tokens"
				if provider == "openai" {
					key = "max_completion_tokens"
					if r.Header.Get("Authorization") != "Bearer fake" {
						t.Fatal(r.Header)
					}
				} else if r.Header.Get("x-api-key") != "fake" {
					t.Fatal(r.Header)
				}
				if body[key] != 200.0 {
					t.Fatal(body)
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"content":[{"type":"text","text":"Charging now."}],"choices":[{"message":{"content":"Charging now."}}]}`))}, nil
			})}
			cfg := automation.NormalizeSmart(map[string]any{"claude_enabled": true})
			p := &automation.ChargePlan{Action: "on"}
			if s.smartNarration(context.Background(), cfg, p) != "" || calls != 0 {
				t.Fatal("missing key")
			}
			s.Store.SaveSecret(s.Key, provider, map[string]any{"api_key": "fake"})
			cfg.Narrate = false
			if s.smartNarration(context.Background(), cfg, p) != "" || calls != 0 {
				t.Fatal("disabled")
			}
			cfg.Narrate = true
			if got := s.smartNarration(context.Background(), cfg, p); got != "Charging now." || calls != 1 {
				t.Fatal(got, calls)
			}
			s.AIHTTP.Transport = narrationTransport(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: 500, Body: io.NopCloser(strings.NewReader("error"))}, nil
			})
			if got := s.smartNarration(context.Background(), cfg, p); got != "" {
				t.Fatal(got)
			}
		})
	}
}
func TestResolvedSmartParameters(t *testing.T) {
	s, _ := testServer(t)
	s.Store.Put("devices", "A", "A", map[string]any{"device": map[string]any{"device_sn": "A", "model_code": 13.0}, "capacity_wh_override": 9000.0})
	s.Store.Put("settings", "smart_charge", "", map[string]any{"by_device": map[string]any{"A": map[string]any{"max_charge_w": 1500.0}}})
	s.Store.Put("forecast_models", "user", "A", map[string]any{"_legacy_table": "device_params", "key": "parasitic_w", "source": "user", "value": 77.0})
	rows := s.smartResolvedParams(context.Background(), "A")
	if len(rows) != 6 {
		t.Fatal(rows)
	}
	by := map[string]map[string]any{}
	for _, r := range rows {
		by[str(r, "key")] = r
		if r["label"] == nil || r["unit"] == nil {
			t.Fatal(r)
		}
	}
	for key, want := range map[string]float64{"battery_capacity_wh": 9000, "max_charge_w": 1500, "parasitic_w": 77} {
		if by[key]["source"] != "user" || by[key]["value"] != want {
			t.Fatal(by[key])
		}
	}
	if by["charge_efficiency"]["source"] != "default" {
		t.Fatal(by)
	}
}
