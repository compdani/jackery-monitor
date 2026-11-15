package bridge

import (
	"context"
	"encoding/json"
	"net"
	"testing"
	"time"
)

func TestRPC(t *testing.T) {
	l, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	defer l.Close()
	go func() {
		c, e := l.Accept()
		if e != nil {
			return
		}
		defer c.Close()
		var v map[string]any
		if json.NewDecoder(c).Decode(&v) == nil {
			_ = json.NewEncoder(c).Encode(map[string]any{"result": map[string]any{"method": v["method"]}})
		}
	}()
	c := &RPC{Address: l.Addr().String()}
	v, e := c.Call(context.Background(), "poll", nil)
	if e != nil || v["method"] != "poll" {
		t.Fatal(v, e)
	}
}
func TestRPCCancellation(t *testing.T) {
	l, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	defer l.Close()
	done := make(chan struct{})
	defer close(done)
	go func() {
		c, e := l.Accept()
		if e != nil {
			return
		}
		defer c.Close()
		<-done
	}()
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(20*time.Millisecond, cancel)
	start := time.Now()
	_, e = (&RPC{Address: l.Addr().String()}).Call(ctx, "poll", nil)
	if e == nil || time.Since(start) > time.Second {
		t.Fatal("cancellation did not stop read", e)
	}
}
