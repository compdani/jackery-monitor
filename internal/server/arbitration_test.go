package server

import (
	"context"
	"testing"
)

func TestPlugPriorities(t *testing.T) {
	s, _ := testServer(t)
	f := &rescueSwitchFake{}
	if err := s.Store.Put("settings", "smart_charge", "", map[string]any{"mode": "active", "kasa_device_host": "HOST"}); err != nil {
		t.Fatal(err)
	}
	for _, owner := range []string{"rule", "solar"} {
		if err := (governedSwitch{s, owner, f}).SetPower(context.Background(), " host ", true); err == nil {
			t.Fatal(owner)
		}
	}
	if err := s.Store.Put("settings", "rescue_state", "", map[string]any{"by_device": map[string]any{"A": map[string]any{"fired": true, "kasa_host": "HOST"}}}); err != nil {
		t.Fatal(err)
	}
	for _, owner := range []string{"rule", "smart", "solar"} {
		if err := (governedSwitch{s, owner, f}).SetPower(context.Background(), "host", false); err == nil {
			t.Fatal(owner)
		}
	}
	if err := (governedSwitch{s, "rescue", f}).SetPower(context.Background(), "host", true); err != nil {
		t.Fatal(err)
	}
	if len(f.hosts) != 1 {
		t.Fatal(f.hosts)
	}
}
func TestACLeaseAndUserOff(t *testing.T) {
	s, _ := testServer(t)
	ctx, release, ok := s.acquireAC(context.Background(), "A")
	if !ok {
		t.Fatal("lease")
	}
	defer release()
	if _, _, ok := s.acquireAC(context.Background(), "A"); ok {
		t.Fatal("overlapping recovery")
	}
	done := s.watchdogUserOff("A")
	done()
	if ctx.Err() == nil {
		t.Fatal("user off failed to cancel")
	}
	// Even after a restart, the durable user OFF suppresses both recovery types.
	restart := &Server{Store: s.Store}
	if _, _, ok := restart.acquireAC(context.Background(), "A"); ok {
		t.Fatal("lost user off")
	}
}
