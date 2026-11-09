package bridge

import (
	"context"
	"net"
	"testing"
	"time"
)

type echoClient struct{}

func (echoClient) Call(_ context.Context, method string, p map[string]any) (map[string]any, error) {
	return map[string]any{"method": method, "params": p}, nil
}
func TestNativeWireRoundTrip(t *testing.T) {
	l, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, l, echoClient{}) }()
	r := RPC{Address: l.Addr().String()}
	m, e := r.Call(ctx, "poll", map[string]any{"device_sn": "SN"})
	if e != nil || m["method"] != "poll" {
		t.Fatal(m, e)
	}
	cancel()
	select {
	case e := <-done:
		if e != nil {
			t.Fatal(e)
		}
	case <-time.After(time.Second):
		t.Fatal("server did not stop")
	}
}
