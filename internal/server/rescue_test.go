package server

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/compdani/jackery-monitor/internal/automation"
	"testing"
	"time"
)

const rt = 1700000000.0

func rp(v float64) *float64 { return &v }

type rescueSwitchFake struct {
	hosts []string
	fail  bool
}

func (f *rescueSwitchFake) SetPower(_ context.Context, host string, on bool) error {
	if !on {
		return errors.New("rescue must only turn ON")
	}
	f.hosts = append(f.hosts, host)
	if f.fail {
		return errors.New("synthetic plug failure")
	}
	return nil
}
func rescuePacket(ts, soc float64) map[string]any {
	return map[string]any{"device": map[string]any{"device_sn": "SN-A"}, "last_update_ts": ts, "telemetry": map[string]any{"battery_percent": soc, "system_soc": 90.0}}
}
func rescueRule(t *testing.T, s *Server, id, scope, host, action string, enabled bool) {
	t.Helper()
	err := s.Store.Put("automation_rules", id, scope, map[string]any{"id": id, "jackery_device_sn": scope, "kasa_host": host, "action": action, "enabled": enabled, "operator": "<", "value": -1.0})
	if err != nil {
		t.Fatal(err)
	}
}
func TestRescueRunnerRetryLatchAuditAndRestart(t *testing.T) {
	s, _ := testServer(t)
	fake := &rescueSwitchFake{fail: true}
	s.RescueSwitch = fake
	rescueRule(t, s, "r1", "SN-A", "plug-A", "on", true)
	s.rescueTick(context.Background(), rt, rescuePacket(rt, 3))
	st := s.rescue.states["SN-A"]
	if st.Fired || st.LastError == nil || len(fake.hosts) != 1 {
		t.Fatal(st, fake)
	}
	s.rescueTick(context.Background(), rt+59, nil)
	if st.Fired || len(fake.hosts) != 2 {
		t.Fatal(st, fake)
	}
	fake.fail = false
	s.rescueTick(context.Background(), rt+60, nil)
	if !st.Fired || *st.LastFireReason != "deadman" || st.LastFireTS != rt+60 || len(fake.hosts) != 3 {
		t.Fatal(st, fake)
	}
	audits, err := s.Store.List("automation_decisions", "SN-A", 0, 100)
	if err != nil || len(audits) != 1 {
		t.Fatal(audits, err)
	}
	audit := audits[0]
	for key, want := range map[string]any{"rule_id": "rescue-watchdog", "action": "on", "kasa_host": "plug-A", "jackery_sn": "SN-A", "soc_at_fire": 3.0, "threshold": 5.0, "reason": "deadman", "fired": true} {
		if audit[key] != want {
			t.Fatal(key, audit)
		}
	}
	restarted := &Server{Store: s.Store, Key: s.Key, RescueSwitch: fake}
	restarted.rescueTick(context.Background(), rt+120, nil)
	if len(fake.hosts) != 3 || !restarted.rescue.states["SN-A"].Fired {
		t.Fatal("restart lost latch")
	}
	restarted.rescueTick(context.Background(), rt+121, rescuePacket(rt, 90))
	if len(fake.hosts) != 3 || !restarted.rescue.states["SN-A"].Fired {
		t.Fatal("frozen timestamp rearmed")
	}
	restarted.rescueTick(context.Background(), rt+180, rescuePacket(rt+180, 20))
	if restarted.rescue.states["SN-A"].Fired {
		t.Fatal("fresh recovery failed")
	}
	restarted.rescueTick(context.Background(), rt+181, rescuePacket(rt+181, 5))
	if len(fake.hosts) != 4 {
		t.Fatal("next incident failed")
	}
}
func TestRescuePlugSelectionAndFleet(t *testing.T) {
	s, _ := testServer(t)
	fake := &rescueSwitchFake{}
	s.RescueSwitch = fake
	rescueRule(t, s, "a", "SN-A", "disabled", "on", false)
	rescueRule(t, s, "b", "SN-A", "off-plug", "off", true)
	rescueRule(t, s, "c", "OTHER", "wrong-device", "on", true)
	rescueRule(t, s, "d", "SN-A", "plug-A", "on", true)
	rescueRule(t, s, "e", "", "global-plug", "on", true)
	p := map[string]any{"cloud": map[string]any{"devices_telemetry": map[string]any{
		"SN-A": map[string]any{"ts": rt, "telemetry": map[string]any{"battery_percent": 4.0, "system_soc": 80.0}},
		"SN-B": map[string]any{"ts": rt, "telemetry": map[string]any{"battery_percent": 2.0}},
		"SN-C": map[string]any{"ts": rt, "telemetry": map[string]any{"battery_percent": 8.0, "system_soc": 1.0}},
	}}}
	s.rescueTick(context.Background(), rt, p)
	if len(fake.hosts) != 2 || !s.rescue.states["SN-A"].Fired || !s.rescue.states["SN-B"].Fired || s.rescue.states["SN-C"].Fired {
		t.Fatal(fake, s.rescue.states)
	}
	if s.rescue.states["SN-A"].Host != "plug-A" || s.rescue.states["SN-B"].Host != "global-plug" {
		t.Fatal(s.rescue.states)
	}
}
func TestRescueNoHostAndNoSourceTimestamp(t *testing.T) {
	s, _ := testServer(t)
	fake := &rescueSwitchFake{}
	s.RescueSwitch = fake
	p := rescuePacket(rt, 2)
	delete(p, "last_update_ts")
	s.rescueTick(context.Background(), rt, p)
	if len(s.rescue.states) != 0 || len(fake.hosts) != 0 {
		t.Fatal("invented source timestamp")
	}
	s.rescueTick(context.Background(), rt, rescuePacket(rt, 2))
	if s.rescue.states["SN-A"].Fired || s.rescue.states["SN-A"].LastError == nil {
		t.Fatal("missing host latched")
	}
	rescueRule(t, s, "r", "SN-A", "plug-A", "on", true)
	s.rescueTick(context.Background(), rt+61, nil)
	if len(fake.hosts) != 1 {
		t.Fatal("no-host did not retry")
	}
}

type failingPoll struct{}

func (failingPoll) Call(context.Context, string, map[string]any) (map[string]any, error) {
	return nil, errors.New("synthetic cloud outage")
}
func TestFailedPollStillRunsRescue(t *testing.T) {
	s, _ := testServer(t)
	fake := &rescueSwitchFake{fail: true}
	s.RescueSwitch = fake
	s.Bridge = failingPoll{}
	now := float64(time.Now().UnixNano()) / 1e9
	rescueRule(t, s, "r", "SN-A", "plug-A", "on", true)
	s.rescueTick(context.Background(), now-61, rescuePacket(now-61, 2))
	fake.fail = false
	s.pollOnce(context.Background())
	if len(fake.hosts) != 2 || !s.rescue.states["SN-A"].Fired || *s.rescue.states["SN-A"].LastFireReason != "deadman" {
		t.Fatal("failed poll skipped watchdog", fake, s.rescue.states)
	}
}
func TestRescueConfigurationAndStatus(t *testing.T) {
	s, h := testServer(t)
	token := contractToken(t, h)
	fake := &rescueSwitchFake{}
	s.RescueSwitch = fake
	contractJSON(t, h, "GET", "/api/rescue/status", "", "", 401)
	cfg := contractJSON(t, h, "GET", "/api/rescue/config", "", token, 200)
	if cfg["enabled"] != true || cfg["stale_fire_s"] != 60.0 {
		t.Fatal(cfg)
	}
	contractJSON(t, h, "POST", "/api/rescue/config", `{"enabled":false}`, token, 200)
	rescueRule(t, s, "r", "SN-A", "plug-A", "on", true)
	s.rescueTick(context.Background(), rt, rescuePacket(rt, 2))
	if len(fake.hosts) != 0 {
		t.Fatal("disabled rescue actuated")
	}
	contractJSON(t, h, "POST", "/api/rescue/config", `{"enabled":true,"stale_fire_s":1}`, token, 400)
	contractJSON(t, h, "POST", "/api/rescue/config", `{"enabled":"true"}`, token, 400)
	contractJSON(t, h, "DELETE", "/api/rescue/status", "", token, 405)
	status := contractJSON(t, h, "GET", "/api/rescue/status", "", token, 200)
	if status["enabled"] != false || status["by_device"].(map[string]any)["SN-A"] == nil {
		t.Fatal(status)
	}
	// State's absent values are JSON null, consistent with the Python dataclass.
	data, _ := json.Marshal(automation.RescueState{})
	var v map[string]any
	json.Unmarshal(data, &v)
	if v["last_fresh_main"] != nil || v["last_fire_reason"] != nil {
		t.Fatal(v)
	}
}

type blockedRescuePoll struct{ started chan struct{} }

func (b blockedRescuePoll) Call(ctx context.Context, _ string, _ map[string]any) (map[string]any, error) {
	close(b.started)
	<-ctx.Done()
	return nil, ctx.Err()
}

type rescueNotifySwitch struct{ called chan string }

func (f rescueNotifySwitch) SetPower(_ context.Context, host string, on bool) error {
	if !on {
		return errors.New("unexpected OFF")
	}
	f.called <- host
	return nil
}
func TestRescueTimerRunsWhileCloudPollIsBlocked(t *testing.T) {
	s, _ := testServer(t)
	rescueRule(t, s, "r", "SN-A", "plug-A", "on", true)
	stamp := float64(time.Now().UnixNano())/1e9 - 61
	main := 2.0
	if err := s.Store.Put("settings", "rescue_state", "", map[string]any{"by_device": map[string]any{"SN-A": rescueDevice{RescueState: automation.RescueState{LastFreshTS: stamp, LastFreshMain: &main}}}}); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	called := make(chan string, 1)
	s.Bridge = blockedRescuePoll{started}
	s.RescueSwitch = rescueNotifySwitch{called}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pollDone := make(chan struct{})
	rescueDone := make(chan struct{})
	go func() { s.Poll(ctx, time.Hour); close(pollDone) }()
	<-started
	go func() { s.RescueLoop(ctx); close(rescueDone) }()
	select {
	case host := <-called:
		if host != "plug-A" {
			t.Error(host)
		}
	case <-time.After(4 * time.Second):
		t.Error("blocked poll stalled rescue timer")
	}
	cancel()
	<-pollDone
	<-rescueDone
}
func TestRescueRetriesFailedAuditPersistenceWithoutRetoggling(t *testing.T) {
	s, _ := testServer(t)
	fake := &rescueSwitchFake{}
	s.RescueSwitch = fake
	rescueRule(t, s, "r", "SN-A", "plug-A", "on", true)
	_, err := s.Store.App.DB().NewQuery(`CREATE TRIGGER fail_rescue_audit BEFORE INSERT ON automation_decisions BEGIN SELECT RAISE(ABORT, 'synthetic disk failure'); END`).Execute()
	if err != nil {
		t.Fatal(err)
	}
	s.rescueTick(context.Background(), rt, rescuePacket(rt, 2))
	if !s.rescue.states["SN-A"].Fired || s.rescue.persistenceError == "" || len(s.rescue.pendingAudits) != 1 {
		t.Fatal("lost latch/pending audit")
	}
	if _, err := s.Store.App.DB().NewQuery(`DROP TRIGGER fail_rescue_audit`).Execute(); err != nil {
		t.Fatal(err)
	}
	s.rescueTick(context.Background(), rt+1, nil)
	audits, err := s.Store.List("automation_decisions", "SN-A", 0, 100)
	if err != nil || len(audits) != 1 || len(fake.hosts) != 1 || len(s.rescue.pendingAudits) != 0 || s.rescue.persistenceError != "" {
		t.Fatal(audits, fake, err)
	}
}
