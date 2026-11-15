package automation

import "testing"

const rescueT0 = 1700000000.0

func rescuePtr(v float64) *float64 { return &v }

// All cases from tests/test_rescue.py, including the exact 60-second boundary.
func TestRescueDecisions(t *testing.T) {
	for _, c := range []struct {
		soc  float64
		want string
	}{{4, "live"}, {6, ""}} {
		st := RescueState{}
		if got := st.Decide(rescueT0, rescuePtr(rescueT0), rescuePtr(c.soc)); got != c.want {
			t.Fatal(c, got)
		}
	}
	st := RescueState{}
	for _, c := range []struct {
		dt   float64
		want string
	}{{0, "live"}, {59, "live"}, {60, "deadman"}} {
		if got := st.Decide(rescueT0+c.dt, rescuePtr(rescueT0), rescuePtr(3)); got != c.want {
			t.Fatal(c, got)
		}
	}
	st = RescueState{}
	if st.Decide(rescueT0, rescuePtr(rescueT0), rescuePtr(8)) != "" || st.Decide(rescueT0+3600, rescuePtr(rescueT0), rescuePtr(8)) != "" {
		t.Fatal("healthy silence fired")
	}
	st = RescueState{}
	if st.Decide(rescueT0, rescuePtr(rescueT0), rescuePtr(4)) != "live" {
		t.Fatal("not armed")
	}
	st.MarkFired("live", rescueT0)
	if st.Decide(rescueT0+10, rescuePtr(rescueT0+10), rescuePtr(4)) != "" || st.Decide(rescueT0+200, rescuePtr(rescueT0+10), rescuePtr(4)) != "" {
		t.Fatal("latched refire")
	}
	if st.Decide(rescueT0+300, rescuePtr(rescueT0+300), rescuePtr(25)) != "" || st.Fired {
		t.Fatal("not rearmed")
	}
	if st.Decide(rescueT0+400, rescuePtr(rescueT0+400), rescuePtr(5)) != "live" {
		t.Fatal("next incident failed")
	}
	st = RescueState{}
	if st.Decide(rescueT0, rescuePtr(rescueT0), rescuePtr(2)) != "live" || st.Decide(rescueT0+2, rescuePtr(rescueT0+2), rescuePtr(2)) != "live" {
		t.Fatal("failed action latched")
	}
	st = RescueState{}
	if st.Decide(rescueT0, nil, nil) != "" || st.Decide(rescueT0+999, nil, nil) != "" {
		t.Fatal("unseen fired")
	}
	if st.Decide(rescueT0, rescuePtr(rescueT0), rescuePtr(4)) != "live" || st.Decide(rescueT0+61, rescuePtr(rescueT0), nil) != "deadman" {
		t.Fatal("missing SOC disarmed")
	}
}
func TestRescueFrozenAndOlderTimestampCannotRecover(t *testing.T) {
	st := RescueState{}
	st.Decide(rescueT0, rescuePtr(rescueT0), rescuePtr(3))
	st.MarkFired("live", rescueT0)
	st.Decide(rescueT0+5, rescuePtr(rescueT0), rescuePtr(90))
	if !st.Fired || *st.LastFreshMain != 3 {
		t.Fatal(st)
	}
	st.Decide(rescueT0+10, rescuePtr(rescueT0-1), rescuePtr(90))
	if !st.Fired || *st.LastFreshMain != 3 {
		t.Fatal(st)
	}
}
