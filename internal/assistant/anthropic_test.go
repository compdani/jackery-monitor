package assistant

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAnthropicTextTransport(t *testing.T) {
	h := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") != "test-key" || r.Header.Get("anthropic-version") == "" {
			t.Error("missing auth/version")
		}
		var b map[string]any
		if e := json.NewDecoder(r.Body).Decode(&b); e != nil {
			t.Error(e)
		}
		if b["system"] != "Be concise" || len(b["messages"].([]any)) != 1 {
			t.Error(b)
		}
		w.Write([]byte(`{"content":[{"type":"thinking","thinking":"internal"},{"type":"text","text":"Solar looks good."}]}`))
	}))
	defer h.Close()
	c := Anthropic{BaseURL: h.URL, APIKey: "test-key", Model: "fixture"}
	got, e := c.Complete(context.Background(), []Message{{"system", "Be concise"}, {"user", "Forecast?"}})
	if e != nil || got != "Solar looks good." {
		t.Fatal(got, e)
	}
}
func TestResolveGateway(t *testing.T) {
	for _, c := range []struct{ Preset, URL, Want string }{{"openai", "", "https://api.openai.com/v1"}, {"groq", "", "https://api.groq.com/openai/v1"}, {"ollama", "", "http://host.docker.internal:11434/v1"}, {"groq", " https://example.test/v1/ ", "https://example.test/v1"}} {
		if got := ResolveBaseURL(c.Preset, c.URL); got != c.Want {
			t.Fatal(got, c)
		}
	}
}
