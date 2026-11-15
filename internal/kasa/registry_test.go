package kasa

import (
	"path/filepath"
	"strings"
	"testing"
)

func newReg(t *testing.T) (*Registry, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "kasa.json")
	return NewRegistry(FileStore{Path: path}), path
}

func bp(b bool) *bool     { return &b }
func sp(s string) *string { return &s }

func TestStatusUnknownForNeverProbedDevice(t *testing.T) {
	reg, _ := newReg(t)
	d, _ := reg.Upsert("192.168.1.50", UpsertOpts{Alias: "Test"})
	if got := reg.StatusOf(d); got != "unknown" {
		t.Fatalf("status = %q", got)
	}
	if reg.IsOnline(d) {
		t.Fatal("never-probed device must not be online")
	}
	if reg.OfflineCount() != 0 { // unknown != offline
		t.Fatal("offline count != 0")
	}
}

func TestUpdateProbeSuccessMarksOnline(t *testing.T) {
	reg, _ := newReg(t)
	reg.Upsert("192.168.1.50", UpsertOpts{})
	d, ok := reg.UpdateProbe("192.168.1.50", ProbeResult{Success: true, IsOn: bp(true), Model: "EP10"})
	if !ok {
		t.Fatal("device not found")
	}
	if reg.StatusOf(d) != "online" || !reg.IsOnline(d) {
		t.Fatalf("status = %q", reg.StatusOf(d))
	}
	if d.ConsecutiveFails != 0 || d.LastSeenTS == nil {
		t.Fatalf("bad probe state %+v", d)
	}
	if d.LastKnownIsOn == nil || !*d.LastKnownIsOn {
		t.Fatal("last_known_is_on not set")
	}
	if d.Model == nil || *d.Model != "EP10" {
		t.Fatalf("model = %v", d.Model)
	}
	if d.LastError != nil {
		t.Fatal("last_error should be nil")
	}
}

func TestUpdateProbeFailureMarksOfflineAndIncrements(t *testing.T) {
	reg, _ := newReg(t)
	reg.Upsert("192.168.1.50", UpsertOpts{})
	reg.UpdateProbe("192.168.1.50", ProbeResult{Error: "conn refused"})
	d, _ := reg.UpdateProbe("192.168.1.50", ProbeResult{Error: "timeout after 15s"})
	if reg.StatusOf(d) != "offline" {
		t.Fatalf("status = %q", reg.StatusOf(d))
	}
	if d.ConsecutiveFails != 2 {
		t.Fatalf("fails = %d", d.ConsecutiveFails)
	}
	if d.LastError == nil || !strings.Contains(*d.LastError, "timeout") {
		t.Fatalf("last_error = %v", d.LastError)
	}
	if reg.OfflineCount() != 1 {
		t.Fatalf("offline = %d", reg.OfflineCount())
	}
}

func TestSuccessAfterFailureResetsCounter(t *testing.T) {
	reg, _ := newReg(t)
	reg.Upsert("192.168.1.50", UpsertOpts{})
	reg.UpdateProbe("192.168.1.50", ProbeResult{Error: "boom"})
	reg.UpdateProbe("192.168.1.50", ProbeResult{Error: "boom2"})
	d, _ := reg.UpdateProbe("192.168.1.50", ProbeResult{Success: true, IsOn: bp(false)})
	if d.ConsecutiveFails != 0 || d.LastError != nil {
		t.Fatalf("not reset: %+v", d)
	}
	if reg.StatusOf(d) != "online" || reg.OfflineCount() != 0 {
		t.Fatal("expected online and zero offline")
	}
}

func TestUpdateProbeReturnsNoneForUnknownHost(t *testing.T) {
	reg, _ := newReg(t)
	if _, ok := reg.UpdateProbe("10.0.0.1", ProbeResult{Success: true}); ok {
		t.Fatal("expected not found")
	}
}

func TestAliasIsNotOverwrittenByProbe(t *testing.T) {
	reg, _ := newReg(t)
	reg.Upsert("192.168.1.50", UpsertOpts{Alias: "Garage"})
	d, _ := reg.UpdateProbe("192.168.1.50", ProbeResult{Success: true, Alias: "HS103(US)"})
	if d.Alias != "Garage" {
		t.Fatalf("alias = %q", d.Alias)
	}
}

func TestErrorIsTruncated(t *testing.T) {
	reg, _ := newReg(t)
	reg.Upsert("192.168.1.50", UpsertOpts{})
	d, _ := reg.UpdateProbe("192.168.1.50", ProbeResult{Error: strings.Repeat("x", 1000)})
	if len(*d.LastError) > 240 {
		t.Fatalf("len = %d", len(*d.LastError))
	}
}

func TestOfflineCountAcrossMultipleDevices(t *testing.T) {
	reg, _ := newReg(t)
	for _, h := range []string{"192.168.1.50", "192.168.1.51", "192.168.1.52"} {
		reg.Upsert(h, UpsertOpts{})
	}
	reg.UpdateProbe("192.168.1.50", ProbeResult{Success: true})
	reg.UpdateProbe("192.168.1.51", ProbeResult{Error: "x"})
	// 192.168.1.52 never probed → unknown, NOT offline.
	if reg.OfflineCount() != 1 {
		t.Fatalf("offline = %d", reg.OfflineCount())
	}
}

func TestProbeStatePersistsAcrossReload(t *testing.T) {
	reg, path := newReg(t)
	reg.Upsert("192.168.1.50", UpsertOpts{})
	reg.UpdateProbe("192.168.1.50", ProbeResult{Error: "boom"})
	reg2 := NewRegistry(FileStore{Path: path})
	d, ok := reg2.Get("192.168.1.50")
	if !ok {
		t.Fatal("not reloaded")
	}
	if d.ConsecutiveFails != 1 || d.LastError == nil || *d.LastError != "boom" {
		t.Fatalf("bad reload %+v", d)
	}
	if reg2.StatusOf(d) != "offline" {
		t.Fatal("expected offline after reload")
	}
}

func TestListForJackeryIncludesUnassigned(t *testing.T) {
	reg, _ := newReg(t)
	reg.Upsert("1.1.1.1", UpsertOpts{})
	reg.Upsert("1.1.1.2", UpsertOpts{JackerySN: sp("A")})
	reg.Upsert("1.1.1.3", UpsertOpts{JackerySN: sp("B")})
	got := reg.ListForJackery("A")
	if len(got) != 2 {
		t.Fatalf("got %d", len(got))
	}
	// Empty string explicitly unassigns.
	reg.Upsert("1.1.1.3", UpsertOpts{JackerySN: sp("")})
	if len(reg.ListForJackery("A")) != 3 {
		t.Fatal("unassigned should be visible")
	}
	if _, err := reg.Upsert("  ", UpsertOpts{}); err == nil {
		t.Fatal("expected host-required error")
	}
	if !reg.Delete("1.1.1.1") || reg.Delete("1.1.1.1") {
		t.Fatal("delete semantics")
	}
}
