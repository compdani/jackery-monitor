// Package bridge provides a bounded, context-aware adapter to the existing
// bridge wire protocol, and narrow interfaces for native device integrations.
package bridge

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"time"
)

type Client interface {
	Call(context.Context, string, map[string]any) (map[string]any, error)
}
type Device interface {
	Poll(context.Context) (map[string]any, error)
	SetOutput(context.Context, string, bool) error
}
type Switch interface {
	SetPower(context.Context, string, bool) error
}
type RPC struct {
	Address string
	Timeout time.Duration
}

func (c *RPC) Call(ctx context.Context, method string, params map[string]any) (map[string]any, error) {
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	conn, e := (&net.Dialer{}).DialContext(ctx, "tcp", c.Address)
	if e != nil {
		return nil, e
	}
	defer conn.Close()
	deadline, _ := ctx.Deadline()
	if e = conn.SetDeadline(deadline); e != nil {
		return nil, e
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	if e = json.NewEncoder(conn).Encode(map[string]any{"method": method, "params": params}); e != nil {
		return nil, e
	}
	scanner := bufio.NewScanner(conn)
	scanner.Buffer(make([]byte, 4096), 8<<20)
	if !scanner.Scan() {
		if e = scanner.Err(); e != nil {
			return nil, e
		}
		return nil, errors.New("bridge closed without response")
	}
	var response struct {
		Result map[string]any `json:"result"`
		Error  string         `json:"error"`
	}
	if e = json.Unmarshal(scanner.Bytes(), &response); e != nil {
		return nil, e
	}
	if response.Error != "" {
		return nil, errors.New(response.Error)
	}
	return response.Result, nil
}
