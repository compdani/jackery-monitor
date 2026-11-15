package assistant

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCompletionTransport(t *testing.T) {
	for _, status := range []int{200, 429, 500} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer private-key" {
					t.Error("incorrect provider request")
				}
				var p map[string]any
				if e := json.NewDecoder(r.Body).Decode(&p); e != nil || p["model"] != "test" {
					t.Error(p, e)
				}
				w.WriteHeader(status)
				if status == 200 {
					_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"hello"}}]}`))
				} else {
					_, _ = w.Write([]byte("provider private-key internal error"))
				}
			}))
			defer srv.Close()
			c := Client{BaseURL: srv.URL + "/v1", APIKey: "private-key", Model: "test"}
			out, e := c.Complete(context.Background(), []Message{{Role: "user", Content: "hi"}})
			if status == 200 {
				if e != nil || out != "hello" {
					t.Fatal(out, e)
				}
			} else if e == nil || strings.Contains(e.Error(), "private-key") {
				t.Fatal("missing or unsafe provider error", e)
			}
		})
	}
}
