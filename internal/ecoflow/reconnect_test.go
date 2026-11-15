package ecoflow

import "testing"

func TestReconnectDefaultsAllowAnyTime(t *testing.T) {
	r := NewReconnect(nil)
	cfg := r.Get()
	if !cfg.AutoReconnect || cfg.ReconnectStart != nil || cfg.ReconnectEnd != nil {
		t.Fatalf("%+v", cfg)
	}
	if !r.AutoReconnectAllowed(12*3600, 0) {
		t.Fatal("should be allowed")
	}
}

func TestReconnectWindowGatesAutoReconnect(t *testing.T) {
	r := NewReconnect(nil)
	on := true
	saved, err := r.Set(ReconnectPatch{AutoReconnect: &on, SetStart: true, ReconnectStart: str("06:00"), SetEnd: true, ReconnectEnd: str("22:00")})
	if err != nil || *saved.ReconnectStart != "06:00" {
		t.Fatalf("%+v %v", saved, err)
	}
	noon, night := float64(12*3600), float64(3*3600)
	if !r.AutoReconnectAllowed(noon, 0) {
		t.Fatal("noon allowed")
	}
	if r.AutoReconnectAllowed(night, 0) {
		t.Fatal("night not allowed")
	}
	off := false
	r.Set(ReconnectPatch{AutoReconnect: &off})
	if r.AutoReconnectAllowed(noon, 0) {
		t.Fatal("flag off")
	}
}

func TestReconnectPartialWindowRejected(t *testing.T) {
	r := NewReconnect(nil)
	if _, err := r.Set(ReconnectPatch{SetStart: true, ReconnectStart: str("06:00"), SetEnd: true, ReconnectEnd: nil}); err == nil {
		t.Fatal("expected error")
	}
	if _, err := r.Set(ReconnectPatch{SetStart: true, ReconnectStart: str("25:00"), SetEnd: true, ReconnectEnd: str("10:00")}); err == nil {
		t.Fatal("expected HH:MM error")
	}
}

func TestReconnectTimezoneOffsetAndWrap(t *testing.T) {
	r := NewReconnect(nil)
	r.Set(ReconnectPatch{SetStart: true, ReconnectStart: str("22:00"), SetEnd: true, ReconnectEnd: str("06:00")})
	// 23:00 UTC with +2h offset → 01:00 local, inside the wrapped window.
	if !r.InAllowedWindow(23*3600, 2*3600) {
		t.Fatal("01:00 should be inside 22:00–06:00")
	}
	if r.InAllowedWindow(12*3600, 0) {
		t.Fatal("noon outside 22:00–06:00")
	}
}
