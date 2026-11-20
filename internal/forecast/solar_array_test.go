package forecast

import (
	"math"
	"testing"
	"time"
)

func TestSolarPositionEquinox(t *testing.T) {
	z, a := SolarPosition(0, 0, time.Date(2024, 3, 20, 12, 0, 0, 0, time.UTC).Unix())
	if z >= 8 || a < 0 || a >= 360 {
		t.Fatal(z, a)
	}
}

func TestDirectIrradianceFollowsSunAzimuth(t *testing.T) {
	for _, hour := range []int{9, 15} {
		ts := time.Date(2026, 3, 20, hour, 0, 0, 0, time.UTC).Unix()
		_, az := SolarPosition(35, 0, ts)
		east, _ := POAComponents(450, 400, 50, 35, 0, ts, 45, -90)
		west, _ := POAComponents(450, 400, 50, 35, 0, ts, 45, 90)
		if hour < 12 && (az >= 180 || east <= west) || hour > 12 && (az <= 180 || west <= east) {
			t.Errorf("%d:00 azimuth %.1f, east-facing beam %.1f, west-facing beam %.1f", hour, az, east, west)
		}
	}
}

func TestInferPlaneReference(t *testing.T) {
	if out := InferPlane(nil, nil, 37.3, -121.9); out["error"] == nil || out["n_samples"] != 0 {
		t.Fatal(out)
	}
	energy, weather := []Row{}, []Row{}
	for d := 3; d < 6; d++ {
		for h := 13; h < 24; h++ {
			ts := time.Date(2024, 3, d, h, 0, 0, 0, time.UTC).Unix()
			z, _ := SolarPosition(37.3, -121.9, ts)
			if z > 80 {
				continue
			}
			ghi := 900 * math.Cos(z*math.Pi/180)
			solar := 2 * POA(ghi, 37.3, -121.9, ts, 20, -90)
			if ghi < 80 || solar < 50 {
				continue
			}
			energy = append(energy, Row{"ts": float64(ts), "solar_w": solar})
			weather = append(weather, Row{"ts": float64(ts), "ghi_w_m2": ghi})
		}
	}
	out := InferPlane(energy, weather, 37.3, -121.9)
	if len(energy) < 12 || out["error"] != nil || out["azimuth"] != -90 || math.Abs(float64(out["declination"].(int))-20) > 10 || out["kwp"].(float64) < 1 || out["kwp"].(float64) > 8 {
		t.Fatal(out)
	}
	for _, v := range []float64{math.NaN(), math.Inf(1), -1, 101} {
		if _, ok := ValidatePlane(20, 0, v); ok {
			t.Fatal(v)
		}
	}
}
