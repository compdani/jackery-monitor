package forecast

import (
	"encoding/json"
	"math"
	"os"
	"testing"
	"time"
)

func TestLoadAndChargePythonParity(t *testing.T) {
	old := time.Local
	time.Local = time.UTC
	defer func() { time.Local = old }()
	raw, e := os.ReadFile("testdata/python.json")
	if e != nil {
		t.Fatal(e)
	}
	var fixture struct {
		Load []struct {
			Rows    []Row
			Profile []struct {
				Hour, Weekend int
				Value         float64
			}
			Efficiency float64
			Windows    int
			Ceiling    *float64
		}
	}
	if e = json.Unmarshal(raw, &fixture); e != nil {
		t.Fatal(e)
	}
	if len(fixture.Load) != 20 {
		t.Fatal("missing reference load fixtures")
	}
	for i, c := range fixture.Load {
		profile := FitLoadProfile(c.Rows, 1704067200+24*14*3600, 600, 3600)
		for _, p := range c.Profile {
			got := profile[LoadKey{p.Hour, p.Weekend}]
			if math.Abs(got-p.Value) > 1e-9 {
				t.Fatalf("fixture %d hour %d: got %v want %v", i, p.Hour, got, p.Value)
			}
		}
		eff, n := FitChargeEfficiency(c.Rows, 5040, .9, 5)
		if math.Abs(eff-c.Efficiency) > 1e-12 || n != c.Windows {
			t.Fatalf("fixture %d charge got %v/%v want %v/%v", i, eff, n, c.Efficiency, c.Windows)
		}
		ceiling := FitChargeCeiling(c.Rows, 5040, 0, 5, .05)
		if (ceiling == nil) != (c.Ceiling == nil) {
			t.Fatalf("fixture %d ceiling mismatch", i)
		}
		if ceiling != nil && math.Abs(*ceiling-*c.Ceiling) > 1e-9 {
			t.Fatal(*ceiling, *c.Ceiling)
		}
	}
}
