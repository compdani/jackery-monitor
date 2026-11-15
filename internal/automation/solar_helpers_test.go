package automation

import "testing"

func TestSolarBalanceWindowBoundaries(t *testing.T) {
	config := map[string]any{"balance_max_window_days": 7., "balance_every_days": 30.}
	start := 1000.
	for _, c := range []struct {
		days             float64
		stalled, restart bool
	}{{0, false, false}, {6.99, false, false}, {7, true, false}, {36.99, true, false}, {37, false, true}} {
		now := start + c.days*86400
		opened, days, stalled := BalanceWindowStatus(config, &start, now)
		if stalled != c.stalled || c.restart && (opened != now || days != 0) || !c.restart && (opened != start || days != c.days) {
			t.Fatalf("%+v: %v %v %v", c, opened, days, stalled)
		}
	}
	opened, days, stalled := BalanceWindowStatus(config, nil, start)
	if opened != start || days != 0 || stalled {
		t.Fatal(opened, days, stalled)
	}
	config["balance_every_days"] = 0.
	now := start + 14*86400
	opened, days, stalled = BalanceWindowStatus(config, &start, now)
	if opened != now || days != 0 || stalled {
		t.Fatal(opened, days, stalled)
	}
	config["balance_max_window_days"] = 0.
	_, _, stalled = BalanceWindowStatus(config, &start, now)
	if stalled {
		t.Fatal("disabled bound stalled")
	}
}

func TestSolarGates(t *testing.T) {
	for _, action := range []string{"on", "off", "skip"} {
		p := SolarChargePlan{Action: action, Reason: "original", CarLoad: 1400}
		for _, load := range []float64{799, 800, 801} {
			for _, before := range []string{"off", "on"} {
				got := GateLoadCeiling(p, &load, before, 800)
				blocked := action == "on" && before == "off" && load >= 800
				if blocked && (got.Action != "skip" || got.Reason == p.Reason) || !blocked && got != p {
					t.Fatal(got)
				}
			}
		}
		if got := GateLoadCeiling(p, nil, "off", 800); got != p {
			t.Fatal(got)
		}
		for _, now := range []float64{1001, 2799, 2800, 2801} {
			got := GateInverterProtect(p, 1000, 1800, now)
			blocked := action == "on" && now < 2800
			if blocked && (got.Action != "skip" || got.Reason == p.Reason) || !blocked && got != p {
				t.Fatal(got)
			}
		}
		if got := GateInverterProtect(p, 0, 1800, 1001); got != p {
			t.Fatal(got)
		}
		got := GateMinHold(p, 1000, 30, "off", 1001)
		if got.Before == nil || *got.Before != "off" {
			t.Fatal(got)
		}
		if action == "on" && got.Action != "skip" {
			t.Fatal(got)
		}
	}
}
