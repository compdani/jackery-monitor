package bridge

import (
	"context"
	"testing"
)

func TestNativeEventContract(t *testing.T) {
	n := &Native{initialized: true}
	n.event("connected")
	n.event("paused")
	out, err := n.Call(context.Background(), "get_events", map[string]any{"limit": "1", "since": "0"})
	if err != nil {
		t.Fatal(err)
	}
	events := out["events"].([]map[string]any)
	if len(events) != 1 {
		t.Fatal(events)
	}
	for _, key := range []string{"ts", "level", "category", "message", "extra"} {
		if events[0][key] == nil {
			t.Fatal(key, events)
		}
	}
	out, err = n.Call(context.Background(), "get_events", map[string]any{"since": "999999999999"})
	if err != nil || len(out["events"].([]map[string]any)) != 0 {
		t.Fatal(out, err)
	}
}
