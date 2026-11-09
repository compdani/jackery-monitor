package forecast

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"reflect"
	"testing"
)

func TestPythonParity(t *testing.T) {
	raw, e := os.ReadFile("testdata/python.json")
	if e != nil {
		t.Fatal(e)
	}
	var fixture struct {
		Simulation []struct {
			Start, Capacity float64
			Hours, Expected []Row
			Options         struct {
				ACFloor    *float64 `json:"ac_charge_floor_pct"`
				Efficiency *float64 `json:"charge_efficiency"`
				Extra      float64  `json:"extra_load_w"`
				ExtraFloor *float64 `json:"extra_load_floor_pct"`
				Ceiling    *float64 `json:"soc_ceiling_pct"`
			}
		}
		Solar []struct {
			Energy, Weather []Row
			K               float64
			N               int
			Shape           map[int]float64
		}
	}
	if e = json.Unmarshal(raw, &fixture); e != nil {
		t.Fatal(e)
	}
	for i, c := range fixture.Simulation {
		t.Run(fmt.Sprint("simulation/", i), func(t *testing.T) {
			o := c.Options
			got := SimulateSOC(c.Start, c.Capacity, c.Hours, Options{o.ACFloor, o.Efficiency, o.ExtraFloor, o.Ceiling, o.Extra})
			if !reflect.DeepEqual(got, c.Expected) {
				for j := range got {
					if !reflect.DeepEqual(got[j], c.Expected[j]) {
						t.Fatalf("hour %d got %v want %v", j, got[j], c.Expected[j])
					}
				}
			}
		})
	}
	for i, c := range fixture.Solar {
		t.Run(fmt.Sprint("solar/", i), func(t *testing.T) {
			k, n := FitSolarCoefficient(c.Energy, c.Weather)
			if math.Abs(k-c.K) > 1e-12 || n != c.N {
				t.Fatalf("got %v/%v want %v/%v", k, n, c.K, c.N)
			}
			shape := FitDiurnalShape(c.Energy, c.Weather, k, -25200)
			for h, v := range c.Shape {
				if math.Abs(shape[h]-v) > 1e-12 {
					t.Fatalf("hour %d got %v want %v", h, shape[h], v)
				}
			}
		})
	}
}
func TestExpectedLoadFallback(t *testing.T) {
	for _, c := range []struct {
		name string
		p    map[LoadKey]float64
		want float64
	}{{"idle", nil, 33}, {"off", map[LoadKey]float64{{0, 0}: 0}, 0}, {"opposite", map[LoadKey]float64{{0, 1}: 100}, 110}, {"neighbor", map[LoadKey]float64{{1, 0}: 200}, 220}} {
		t.Run(c.name, func(t *testing.T) {
			got := ExpectedLoad(c.p, 1704067200, 0, .1, 0)
			if math.Abs(got-c.want) > 1e-9 {
				t.Fatalf("got %v want %v", got, c.want)
			}
		})
	}
}
func TestSolarNoise(t *testing.T) {
	for _, w := range []float64{0, 8, 50} {
		k, n := FitSolarCoefficient([]Row{{"ts": float64(1700000000), "solar_w": w}}, nil)
		if k != 0 || n != 0 {
			t.Fatal(k, n)
		}
	}
}
