package assistant

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOpenAIToolLoopAndImageBlocks(t *testing.T) {
	calls := 0
	var bodies []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var b map[string]any
		_ = json.NewDecoder(r.Body).Decode(&b)
		bodies = append(bodies, b)
		if r.Header.Get("Authorization") != "Bearer k" || r.URL.Path != "/v1/chat/completions" {
			t.Error(r.URL.Path, r.Header)
		}
		if calls == 1 {
			_, _ = w.Write([]byte(`{"choices":[{"message":{"content":null,"tool_calls":[{"id":"c1","type":"function","function":{"name":"get_location","arguments":"{\"x\":1}"}}]}}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"Here."}}]}`))
	}))
	defer srv.Close()
	a := &Agent{Provider: "openai", APIKey: "k", Model: "m", BaseURL: srv.URL + "/v1"}
	got := []Call{}
	res, err := a.Run(context.Background(), "sys", []Turn{{Role: "user", Content: "hi", Blocks: []Block{{Type: "image", MIME: "image/png", Data: "AA=="}}}}, Tools(), func(_ context.Context, c Call) (any, Action) {
		got = append(got, c)
		return map[string]any{"secret": strings.Repeat("x", 30000)}, Continue
	})
	if err != nil || res.Text != "Here." || res.Turns != 2 || len(got) != 1 || got[0].Args["x"] != 1.0 {
		t.Fatal(res, err, got)
	}
	first, _ := json.Marshal(bodies[0])
	if !strings.Contains(string(first), "data:image/png;base64,AA==") || !strings.Contains(string(first), `"type":"function"`) {
		t.Fatal(string(first))
	}
	msgs := bodies[1]["messages"].([]any)
	tool := msgs[len(msgs)-1].(map[string]any)
	if tool["role"] != "tool" || len(tool["content"].(string)) != 20000 {
		t.Fatal(tool["role"], len(tool["content"].(string)))
	}
}

func TestStopNowEndsWithoutSendingResults(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_, _ = w.Write([]byte(`{"content":[{"type":"tool_use","id":"a","name":"submit","input":{"ok":true}}]}`))
	}))
	defer srv.Close()
	a := &Agent{APIKey: "k", Model: "m", BaseURL: srv.URL}
	res, err := a.Run(context.Background(), "", []Turn{{Role: "user", Content: "go"}}, nil, func(context.Context, Call) (any, Action) { return nil, StopNow })
	if err != nil || !res.Done || calls != 1 {
		t.Fatal(res, err, calls)
	}
	// HTTP errors surface with status.
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(401) }))
	defer bad.Close()
	a.BaseURL = bad.URL
	if _, err = a.Run(context.Background(), "", []Turn{{Role: "user", Content: "go"}}, nil, nil); err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatal(err)
	}
}
