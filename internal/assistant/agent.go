package assistant

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

//go:embed contracts/tools.json
var toolsJSON []byte

// Tool is a provider-neutral function tool (Anthropic name/description/
// input_schema shape).
type Tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Schema      map[string]any `json:"input_schema"`
}

// Catalog is the assistant tool contract exported from assistant_agent.py.
type Catalog struct {
	Tools []Tool   `json:"tools"`
	Write []string `json:"write"`
	Read  []string `json:"read"`
	MIME  []string `json:"mime"`
}

var catalog = func() Catalog {
	var c Catalog
	if err := json.Unmarshal(toolsJSON, &c); err != nil {
		panic(err)
	}
	return c
}()

func Tools() []Tool { return append([]Tool(nil), catalog.Tools...) }
func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}
func IsWriteTool(name string) bool    { return contains(catalog.Write, name) }
func IsReadTool(name string) bool     { return contains(catalog.Read, name) }
func AllowedMIME(mime string) bool    { return contains(catalog.MIME, mime) }
func NeedsApproval(name string, args map[string]any) bool {
	if !IsWriteTool(name) {
		return false
	}
	// fit_device_param with apply=false is a preview and runs immediately.
	return !(name == "fit_device_param" && args["apply"] != true)
}

// Block is an attachment content block for the newest user message.
type Block struct {
	Type, MIME, Data, Filename string
}

// Turn is one history message.
type Turn struct {
	Role, Content string
	Blocks        []Block
}

// Call is one model tool call.
type Call struct {
	ID, Name string
	Args     map[string]any
}

// Action tells the loop how to proceed after a tool result.
type Action int

const (
	Continue Action = iota
	// StopAfterTurn sends this turn's results then ends (pending approvals).
	StopAfterTurn
	// StopNow ends immediately without sending results (final submission).
	StopNow
)

// Handler executes a tool call. The returned value is JSON-encoded and
// truncated to Limit bytes before it is returned to the model.
type Handler func(ctx context.Context, c Call) (any, Action)

// Agent runs a bounded multi-turn tool loop against Anthropic Messages or
// an OpenAI-compatible chat-completions endpoint.
type Agent struct {
	Provider, APIKey, Model, BaseURL string
	HTTP                             *http.Client
	MaxTokens, MaxTurns, Limit       int
	// Thinking adds Anthropic adaptive thinking with this effort.
	Thinking string
	Headers  map[string]string
}

// Result is the loop outcome.
type Result struct {
	Text  string
	Turns int
	Calls int
	Done  bool // a handler requested StopNow
}

func (a *Agent) client() *http.Client {
	if a.HTTP != nil {
		return a.HTTP
	}
	return &http.Client{Timeout: 10 * time.Minute}
}
func (a *Agent) post(ctx context.Context, url string, body any, headers map[string]string, out any) error {
	data, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	for k, v := range a.Headers {
		req.Header.Set(k, v)
	}
	resp, err := a.client().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg := strings.TrimSpace(string(raw))
		if len(msg) > 300 {
			msg = msg[:300]
		}
		return fmt.Errorf("AI provider returned HTTP %d: %s", resp.StatusCode, msg)
	}
	return json.Unmarshal(raw, out)
}
func (a *Agent) encode(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		b, _ = json.Marshal(map[string]any{"error": err.Error()})
	}
	limit := a.Limit
	if limit <= 0 {
		limit = 20000
	}
	if len(b) > limit {
		return string(b[:limit])
	}
	return string(b)
}

// Run executes the loop. history ends with the newest user message.
func (a *Agent) Run(ctx context.Context, system string, history []Turn, tools []Tool, h Handler) (Result, error) {
	if a.MaxTurns <= 0 {
		a.MaxTurns = 6
	}
	if a.MaxTokens <= 0 {
		a.MaxTokens = 4096
	}
	if a.Provider == "openai" {
		return a.runOpenAI(ctx, system, history, tools, h)
	}
	return a.runAnthropic(ctx, system, history, tools, h)
}

func (a *Agent) runAnthropic(ctx context.Context, system string, history []Turn, tools []Tool, h Handler) (Result, error) {
	msgs := []any{}
	for i, t := range history {
		if t.Role != "user" && t.Role != "assistant" {
			continue
		}
		if t.Role == "user" && i == len(history)-1 && len(t.Blocks) > 0 {
			content := []any{}
			for _, b := range t.Blocks {
				if b.Type == "image" || b.Type == "document" {
					content = append(content, map[string]any{"type": b.Type, "source": map[string]any{"type": "base64", "media_type": b.MIME, "data": b.Data}})
				}
			}
			if t.Content != "" {
				content = append(content, map[string]any{"type": "text", "text": t.Content})
			}
			msgs = append(msgs, map[string]any{"role": "user", "content": content})
			continue
		}
		msgs = append(msgs, map[string]any{"role": t.Role, "content": t.Content})
	}
	base := strings.TrimRight(a.BaseURL, "/")
	if base == "" {
		base = "https://api.anthropic.com/v1"
	}
	res := Result{}
	for res.Turns < a.MaxTurns {
		res.Turns++
		body := map[string]any{"model": a.Model, "max_tokens": a.MaxTokens, "messages": msgs}
		if system != "" {
			body["system"] = system
		}
		if len(tools) > 0 {
			body["tools"] = tools
		}
		if a.Thinking != "" {
			body["thinking"] = map[string]any{"type": "adaptive"}
			body["output_config"] = map[string]any{"effort": a.Thinking}
		}
		var resp struct {
			Content []map[string]any `json:"content"`
		}
		if err := a.post(ctx, base+"/messages", body, map[string]string{"x-api-key": a.APIKey, "anthropic-version": "2023-06-01"}, &resp); err != nil {
			return res, err
		}
		calls, texts := []Call{}, []string{}
		for _, b := range resp.Content {
			switch b["type"] {
			case "text":
				if s, _ := b["text"].(string); s != "" {
					texts = append(texts, s)
				}
			case "tool_use":
				args, _ := b["input"].(map[string]any)
				if args == nil {
					args = map[string]any{}
				}
				id, _ := b["id"].(string)
				name, _ := b["name"].(string)
				calls = append(calls, Call{ID: id, Name: name, Args: args})
			}
		}
		if len(texts) > 0 {
			res.Text = strings.Join(texts, "\n")
		}
		if len(calls) == 0 {
			break
		}
		// Thinking/tool_use blocks are echoed unchanged, as the API requires.
		content := make([]any, len(resp.Content))
		for i, b := range resp.Content {
			content[i] = b
		}
		msgs = append(msgs, map[string]any{"role": "assistant", "content": content})
		results, stop := []any{}, Continue
		for _, c := range calls {
			res.Calls++
			out, act := h(ctx, c)
			if act == StopNow {
				res.Done = true
				return res, nil
			}
			stop = max(stop, act)
			results = append(results, map[string]any{"type": "tool_result", "tool_use_id": c.ID, "content": a.encode(out)})
		}
		msgs = append(msgs, map[string]any{"role": "user", "content": results})
		if stop == StopAfterTurn {
			break
		}
	}
	return res, nil
}

func (a *Agent) runOpenAI(ctx context.Context, system string, history []Turn, tools []Tool, h Handler) (Result, error) {
	msgs := []any{}
	if system != "" {
		msgs = append(msgs, map[string]any{"role": "system", "content": system})
	}
	for i, t := range history {
		if t.Role != "user" && t.Role != "assistant" {
			continue
		}
		if t.Role == "user" && i == len(history)-1 && len(t.Blocks) > 0 {
			content := []any{}
			if t.Content != "" {
				content = append(content, map[string]any{"type": "text", "text": t.Content})
			}
			for _, b := range t.Blocks {
				if b.Type == "image" {
					content = append(content, map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:" + b.MIME + ";base64," + b.Data}})
				}
			}
			msgs = append(msgs, map[string]any{"role": "user", "content": content})
			continue
		}
		msgs = append(msgs, map[string]any{"role": t.Role, "content": t.Content})
	}
	fns := []any{}
	for _, t := range tools {
		fns = append(fns, map[string]any{"type": "function", "function": map[string]any{"name": t.Name, "description": t.Description, "parameters": t.Schema}})
	}
	base := strings.TrimRight(a.BaseURL, "/")
	if base == "" {
		base = "https://api.openai.com/v1"
	}
	res := Result{}
	for res.Turns < a.MaxTurns {
		res.Turns++
		body := map[string]any{"model": a.Model, "messages": msgs, "max_completion_tokens": a.MaxTokens}
		if len(fns) > 0 {
			body["tools"] = fns
		}
		var resp struct {
			Choices []struct {
				Message struct {
					Content   *string `json:"content"`
					ToolCalls []struct {
						ID       string `json:"id"`
						Function struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"message"`
			} `json:"choices"`
		}
		if err := a.post(ctx, base+"/chat/completions", body, map[string]string{"Authorization": "Bearer " + a.APIKey}, &resp); err != nil {
			return res, err
		}
		if len(resp.Choices) == 0 {
			return res, fmt.Errorf("AI provider returned no choices")
		}
		m := resp.Choices[0].Message
		if m.Content != nil && *m.Content != "" {
			res.Text = *m.Content
		}
		if len(m.ToolCalls) == 0 {
			break
		}
		echo := []any{}
		for _, tc := range m.ToolCalls {
			echo = append(echo, map[string]any{"id": tc.ID, "type": "function", "function": map[string]any{"name": tc.Function.Name, "arguments": tc.Function.Arguments}})
		}
		msgs = append(msgs, map[string]any{"role": "assistant", "content": m.Content, "tool_calls": echo})
		stop := Continue
		for _, tc := range m.ToolCalls {
			res.Calls++
			args := map[string]any{}
			_ = json.Unmarshal([]byte(tc.Function.Arguments), &args)
			out, act := h(ctx, Call{ID: tc.ID, Name: tc.Function.Name, Args: args})
			if act == StopNow {
				res.Done = true
				return res, nil
			}
			stop = max(stop, act)
			msgs = append(msgs, map[string]any{"role": "tool", "tool_call_id": tc.ID, "content": a.encode(out)})
		}
		if stop == StopAfterTurn {
			break
		}
	}
	return res, nil
}
