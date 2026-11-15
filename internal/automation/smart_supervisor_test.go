package automation

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

type smartFake struct {
	calls []bool
	err   error
}

func (f *smartFake) SetPower(_ context.Context, _ string, on bool) error {
	f.calls = append(f.calls, on)
	return f.err
}
func TestSmartExecutionRestartAndFailure(t *testing.T) {
	cfg := NormalizeSmart(map[string]any{"mode": "ACTIVE", "kasa_device_host": "fake", "max_on_duration_minutes": 30})
	f := &smartFake{}
	st, d := RunSmart(context.Background(), f, cfg, SmartState{}, "sn", ChargePlan{Action: "on", DecidedAt: 100})
	if !d.Executed || st.OnSince != 100 {
		t.Fatal(st, d)
	}
	b, _ := json.Marshal(st)
	var restarted SmartState
	_ = json.Unmarshal(b, &restarted)
	f.err = errors.New("offline")
	st, d = RunSmart(context.Background(), f, cfg, restarted, "sn", ChargePlan{Action: "on", DecidedAt: 1900})
	if d.Executed || d.Action != "off" || st.OnSince != 100 || st.LastError == "" {
		t.Fatal(st, d)
	}
	f.err = nil
	st, d = RunSmart(context.Background(), f, cfg, st, "sn", ChargePlan{Action: "on", DecidedAt: 1901})
	if !d.Executed || st.OnSince != 0 || st.LastAction != "off" {
		t.Fatal(st, d)
	}
}
func TestSmartTestAndOffNeverActuate(t *testing.T) {
	for _, mode := range []string{"off", "test"} {
		f := &smartFake{}
		cfg := NormalizeSmart(map[string]any{"mode": mode, "kasa_device_host": "fake"})
		_, d := RunSmart(context.Background(), f, cfg, SmartState{}, "sn", ChargePlan{Action: "on", DecidedAt: 100})
		if len(f.calls) != 0 || d.Executed {
			t.Fatal(mode, d)
		}
	}
}
func TestSmartNormalization(t *testing.T) {
	cfg := NormalizeSmart(map[string]any{"mode": "bad", "target_sunrise_soc_pct": 99, "max_charge_w": "1000", "max_on_duration_minutes": 2})
	if cfg.Mode != "off" || cfg.Target != 25 || cfg.MaxChargeW != 1000 || cfg.MaxOnMinutes != 480 {
		t.Fatal(cfg)
	}
}
