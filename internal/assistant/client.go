// Package assistant implements the OpenAI-compatible text transport.
// Streaming, attachments, and the approval/tool loop are not yet ported.
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

type Client struct {
	BaseURL, APIKey, Model string
	HTTP                   *http.Client
}
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

func (c *Client) Complete(ctx context.Context, messages []Message) (string, error) {
	body, e := json.Marshal(map[string]any{"model": c.Model, "messages": messages, "stream": false})
	if e != nil {
		return "", e
	}
	req, e := http.NewRequestWithContext(ctx, "POST", strings.TrimRight(c.BaseURL, "/")+"/chat/completions", bytes.NewReader(body))
	if e != nil {
		return "", e
	}
	req.Header.Set("Authorization", "Bearer "+c.APIKey)
	req.Header.Set("Content-Type", "application/json")
	client := c.HTTP
	if client == nil {
		client = &http.Client{Timeout: 90 * time.Second}
	}
	resp, e := client.Do(req)
	if e != nil {
		return "", e
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return "", fmt.Errorf("AI provider returned HTTP %d", resp.StatusCode)
	}
	var result struct {
		Choices []struct {
			Message Message `json:"message"`
		} `json:"choices"`
	}
	if e = json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&result); e != nil {
		return "", e
	}
	if len(result.Choices) == 0 {
		return "", fmt.Errorf("AI provider returned no choices")
	}
	return result.Choices[0].Message.Content, nil
}
