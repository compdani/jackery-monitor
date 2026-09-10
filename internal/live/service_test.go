package live

import (
	"context"
	"errors"
	"jackery-monitor/internal/jackery"
	"sync"

	"testing"
	"time"
)

func settings(key string) int {
	switch key {
	case "cloud_poll_interval_s", "session_contested_cooldown_s":
		return 60
	case "low_battery_threshold":
		return 20
	default:
		return 2
	}
}
func mock(t *testing.T) *Service                       { t.Helper(); return New(t.TempDir(), Options{Mock: true}, settings) }
func telemetry(snapshot map[string]any) map[string]any { return snapshot["telemetry"].(map[string]any) }
func TestMockDeviceIsolationPauseAndSnapshots(t *testing.T) {
	s := mock(t)
	ctx := context.Background()
	if err := s.SetOutput(ctx, "MOCK-3000", "ac", false); err != nil {
		t.Fatal(err)
	}
	if telemetry(s.Snapshot("mock-3000"))["ac_on"] != false || telemetry(s.Snapshot("mock-5000"))["ac_on"] != true {
		t.Fatal("command crossed devices")
	}
	if err := s.SetOutput(ctx, "UNKNOWN", "ac", false); err == nil {
		t.Fatal("unknown serial accepted")
	}
	snapshot := s.Snapshot("mock-5000")
	telemetry(snapshot)["battery_percent"] = 0
	if telemetry(s.Snapshot("mock-5000"))["battery_percent"] == 0 {
		t.Fatal("snapshot mutated runtime")
	}
	s.Pause(600)
	if err := s.SetOutput(ctx, "MOCK-5000", "ac", false); err == nil {
		t.Fatal("paused output accepted")
	}
	if s.Snapshot("")["connection_status"] != "paused" {
		t.Fatal("pause not visible")
	}
	s.Resume()
	if s.Snapshot("")["connection_status"] != "connected" {
		t.Fatal("resume not visible")
	}
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 30; j++ {
				s.Snapshot("mock-3000")
				s.applyDelta(s.generation, "MOCK-5000", map[string]any{"ip": 200})
			}
		}()
	}
	wg.Wait()
}

type fakeBackend struct {
	calls    int
	err      error
	closed   bool
	delta    func(string, map[string]any)
	property func(string) (map[string]any, error)
}

func (f *fakeBackend) Devices(context.Context) ([]jackery.Device, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return []jackery.Device{{ID: "1", SN: "SN1"}, {ID: "2", SN: "SN2"}}, nil
}
func (f *fakeBackend) Properties(_ context.Context, id string) (map[string]any, error) {
	if f.property != nil {
		return f.property(id)
	}
	return map[string]any{"rb": 80, "ip": 400, "acip": 100, "cip": 50, "op": 200, "oac": 1}, nil
}
func (f *fakeBackend) Subscribe(_ context.Context, delta func(string, map[string]any), _ func(bool, string)) error {
	f.delta = delta
	return nil
}
func (f *fakeBackend) SetOutput(context.Context, string, string, bool) error { return nil }
func (f *fakeBackend) Close()                                                { f.closed = true }
func TestCloudDeltaMergeContentionAndBrokerAck(t *testing.T) {
	s := mock(t)
	s.opts.Mock = false
	s.devices = nil
	s.entries = map[string]*entry{}
	backend := &fakeBackend{}
	s.backend = backend
	now := time.Now()
	s.Poll(context.Background(), now)
	backend.delta("SN2", map[string]any{"ip": 900})
	if telemetry(s.Snapshot("2"))["solar_input_w"] != float64(750) || telemetry(s.Snapshot("2"))["battery_percent"] != float64(80) {
		t.Fatal("MQTT must merge into full HTTP properties")
	}
	if telemetry(s.Snapshot("1"))["solar_input_w"] != float64(250) {
		t.Fatal("MQTT delta crossed devices")
	}
	if err := s.SetOutput(context.Background(), "SN1", "ac", false); err != nil {
		t.Fatal(err)
	}
	if telemetry(s.Snapshot("1"))["ac_on"] != true {
		t.Fatal("PUBACK was mistaken for device confirmation")
	}
	backend.err = jackery.ErrContested
	s.Poll(context.Background(), now.Add(time.Second))
	calls := backend.calls
	s.Poll(context.Background(), now.Add(10*time.Second))
	if backend.calls != calls {
		t.Fatal("retried within contested cooldown")
	}
	backend.err = nil
	s.Resume()
	s.Poll(context.Background(), now.Add(11*time.Second))
	if backend.calls != calls+1 {
		t.Fatal("resume did not reclaim session")
	}
	old := backend.delta
	s.replace(nil, "")
	old("SN1", map[string]any{"ip": 999})
	if len(s.entries) != 0 {
		t.Fatal("old account callback repopulated cache")
	}
}
func TestRuntimeCancellation(t *testing.T) {
	s := mock(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("runtime did not stop")
	}
}
func TestAuthErrorDoesNotRetryEveryTick(t *testing.T) {
	s := mock(t)
	s.opts.Mock = false
	b := &fakeBackend{err: jackery.ErrCredentials}
	s.backend = b
	now := time.Now()
	s.Poll(context.Background(), now)
	s.Poll(context.Background(), now.Add(time.Minute))
	if b.calls != 1 {
		t.Fatal("invalid credentials repeatedly retried")
	}
	if !errors.Is(b.err, jackery.ErrCredentials) {
		t.Fatal("wrong error")
	}
}

func TestOfflineDeviceDoesNotBlockFleet(t *testing.T) {
	s := mock(t)
	s.opts.Mock = false
	s.devices = nil
	s.entries = map[string]*entry{}
	b := &fakeBackend{property: func(id string) (map[string]any, error) {
		if id == "1" {
			return nil, errors.New("device offline")
		}
		return map[string]any{"rb": 70, "ip": 900}, nil
	}}
	s.backend = b
	s.Poll(context.Background(), time.Now())
	if telemetry(s.Snapshot("2"))["battery_percent"] != float64(70) {
		t.Fatal("offline first device blocked the second")
	}
	if s.Snapshot("2")["connection_status"] != "degraded" {
		t.Fatal("partial failure hidden")
	}
}
func TestNewerMQTTPortStateSurvivesInflightHTTP(t *testing.T) {
	s := mock(t)
	s.opts.Mock = false
	s.devices = nil
	s.entries = map[string]*entry{}
	b := &fakeBackend{}
	s.backend = b
	now := time.Now()
	s.Poll(context.Background(), now)
	b.property = func(id string) (map[string]any, error) {
		if id == "1" {
			b.delta("SN1", map[string]any{"oac": 0, "ip": 1000})
		}
		return map[string]any{"rb": 80, "oac": 1, "ip": 300}, nil
	}
	s.Poll(context.Background(), now.Add(time.Minute))
	tele := telemetry(s.Snapshot("1"))
	if tele["ac_on"] != false || tele["input_power_w"] != float64(1000) {
		t.Fatal("late HTTP overwrote newer MQTT fields")
	}
}
func TestContentionBackoffGrows(t *testing.T) {
	s := mock(t)
	s.opts.Mock = false
	b := &fakeBackend{err: jackery.ErrContested}
	s.backend = b
	now := time.Now()
	s.Poll(context.Background(), now)
	s.Poll(context.Background(), now.Add(61*time.Second))
	if s.contestedConsecutive != 2 || s.contestedUntil.Sub(now.Add(61*time.Second)) != 120*time.Second {
		t.Fatal("contention backoff did not double")
	}
}

func TestMockSystemSOCAndPacks(t *testing.T) {
	s := mock(t)
	snap := s.Snapshot("mock-5000")
	tele := telemetry(snap)
	if tele["system_soc_pct"] != float64(80) || tele["main_soc_pct"] != float64(76) || tele["capacity_wh"] != float64(15120) {
		t.Fatalf("incorrect capacity weighting: %v", tele)
	}
	if len(snap["battery_packs"].([]any)) != 2 {
		t.Fatal("missing mock packs")
	}
	if telemetry(s.Snapshot("mock-3000"))["system_soc_pct"] != float64(63) {
		t.Fatal("pack data crossed devices")
	}
}
