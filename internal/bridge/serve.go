package bridge

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"sync"
	"time"
)

// Serve exposes the legacy one-request-per-connection protocol. Bind only to
// loopback or a trusted network; the legacy protocol has no authentication.
func Serve(ctx context.Context, l net.Listener, client Client) error {
	var workers sync.WaitGroup
	stop := context.AfterFunc(ctx, func() { _ = l.Close() })
	defer stop()
	defer workers.Wait()
	slots := make(chan struct{}, 32)
	for {
		conn, err := l.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		select {
		case slots <- struct{}{}:
		case <-ctx.Done():
			conn.Close()
			return nil
		}
		workers.Add(1)
		go func() {
			defer workers.Done()
			defer func() { <-slots }()
			defer conn.Close()
			conn.SetDeadline(time.Now().Add(30 * time.Second))
			stop := context.AfterFunc(ctx, func() { conn.Close() })
			defer stop()
			scan := bufio.NewScanner(conn)
			scan.Buffer(make([]byte, 4096), 1<<20)
			if !scan.Scan() {
				return
			}
			var req struct {
				Method string         `json:"method"`
				Params map[string]any `json:"params"`
			}
			response := map[string]any{}
			if err := json.Unmarshal(scan.Bytes(), &req); err != nil {
				response["error"] = "invalid JSON request"
			} else {
				callCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
				result, err := client.Call(callCtx, req.Method, req.Params)
				cancel()
				if err != nil {
					response["error"] = err.Error()
				} else {
					response["result"] = result
				}
			}
			json.NewEncoder(conn).Encode(response)
		}()
	}
}
