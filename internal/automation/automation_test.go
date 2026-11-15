package automation

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func TestRatesAndSavings(t *testing.T) {
	p := CostPlan{Type: "tou", Currency: "USD", Slots: []Slot{{Start: 22, End: 7, Rate: .1}, {Start: 7, End: 22, Rate: .3}}}
	for _, c := range []struct {
		hour, offset int
		want         float64
	}{{0, 0, .1}, {7, 0, .3}, {22, 0, .1}, {7, -3600, .1}} {
		if v := p.RateAt(1704067200+int64(c.hour)*3600, c.offset); v != c.want {
			t.Fatalf("got %v want %v", v, c.want)
		}
	}
	out := Savings([]Energy{{TS: 1704067200, OutputWH: 6000, SolarWH: 1700, InputWH: 1700}}, CostPlan{Type: "flat", Rate: .31}, 0)
	if out["solar_savings"] != 1.86 || out["grid_cost"] != 0.0 {
		t.Fatal(out)
	}
	old := Savings([]Energy{{InputWH: 1000, SolarWH: 200}}, CostPlan{Type: "flat", Rate: .3}, 0)
	if old["grid_cost"] != .24 {
		t.Fatal(old)
	}
}
func TestSchedule(t *testing.T) {
	for _, c := range []struct {
		m    int
		want bool
	}{{23 * 60, true}, {2 * 60, true}, {7 * 60, false}, {12 * 60, false}} {
		if InSleepWindow(c.m, "23:00", "07:00") != c.want {
			t.Fatal(c)
		}
	}
	w := []Window{{Start: "18:00", End: "22:00", Watts: 800, Days: "all"}, {Start: "18:00", End: "22:00", Watts: 200, Days: "weekday"}}
	for _, c := range []struct {
		h, d int
		want float64
	}{{18, 0, 1000}, {18, 5, 800}, {22, 0, 0}} {
		if v := ScheduledLoad(w, c.h, c.d, ""); v != c.want {
			t.Fatal(v, c)
		}
	}
}
func TestPlannedHours(t *testing.T) {
	anchor := int64(3)
	got := PickPlannedHours([]Candidate{{1, .1}, {2, .1}, {3, .3}}, 2, &anchor)
	if !reflect.DeepEqual(got, []int64{2, 3}) {
		t.Fatal(got)
	}
}

type fakeSwitch struct {
	calls int
	err   error
}

func (s *fakeSwitch) SetPower(context.Context, string, bool) error { s.calls++; return s.err }
func TestRuleEdges(t *testing.T) {
	f := &fakeSwitch{}
	e := &Engine{Switch: f}
	r := Rule{ID: "a", Enabled: true, Operator: "<", Value: 20, Host: "plug", Action: "off"}
	for _, soc := range []float64{30, 19, 18, 21, 19} {
		if _, err := e.Evaluate(context.Background(), r, "sn", soc); err != nil {
			t.Fatal(err)
		}
	}
	if f.calls != 2 {
		t.Fatal(f.calls)
	}
	f.err = errors.New("offline")
	r.ID = "b"
	for i := 0; i < 2; i++ {
		if _, err := e.Evaluate(context.Background(), r, "sn", 10); err == nil {
			t.Fatal("expected failure")
		}
	}
	if f.calls != 4 {
		t.Fatal("failed commands must retry")
	}
}
