package forecast

import (
	"math"
	"testing"
	"time"
)

func TestForecastSolarHouseShadingSunnyVsCloudy(t *testing.T) {
	// Simulate 10 days in late November at 35°N, -95°W (UTC-6), south-facing 30° array.
	// Unshaded 10:00–13:00; neighbor's house blocks direct beam at 14:00–16:00.
	offset := -6 * 3600
	lat, lon := 35.0, -95.0
	plane := Plane{Declination: 30, Azimuth: 0, KWP: 4.0}
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.FixedZone("CST", offset)).Unix()

	var history, weather []Row
	for day := 10; day >= 1; day-- {
		for h := 0; h < 24; h++ {
			begin := time.Date(2026, 11, 20-day, h, 0, 0, 0, time.FixedZone("CST", offset)).Unix()
			tsEnd := begin + 3600
			cloudy := day%4 == 0
			direct, diffuse, cloud := 0.0, 0.0, 10.0
			if h >= 8 && h <= 16 {
				if cloudy {
					direct, diffuse, cloud = 20, 140, 95
				} else {
					// Rough bell curve around noon (Nov GHI max ~550 W/m²)
					dist := math.Abs(float64(h) - 12.0)
					ghi := math.Max(0, 560-95*dist)
					direct = ghi * 0.85
					diffuse = ghi * 0.15
				}
			}
			ghi := direct + diffuse
			weather = append(weather, Row{
				"ts":              float64(tsEnd),
				"ghi_w_m2":        ghi,
				"direct_w_m2":     direct,
				"diffuse_w_m2":    diffuse,
				"cloud_cover_pct": cloud,
			})
			pd, pf := POAComponents(ghi, direct, diffuse, lat, lon, begin+1800, float64(plane.Declination), float64(plane.Azimuth))
			// True array delivers ~3.2 W per W/m² POA when unshaded.
			// House blocks direct beam at 15:00 completely and 14:00 partially (70% sky view on diffuse).
			beamTrans, diffTrans := 1.0, 1.0
			if h == 14 {
				beamTrans, diffTrans = 0.25, 0.75
			} else if h == 15 {
				beamTrans, diffTrans = 0.0, 0.70
			} else if h == 16 {
				beamTrans, diffTrans = 0.0, 0.0 // complete dark shadow -> 0 W
			}
			sol := 3.2 * (pd*beamTrans + pf*diffTrans)
			history = append(history, Row{
				"ts":          float64(begin),
				"solar_w":     sol,
				"solar_wh":    sol,
				"output_w":    300.0,
				"output_wh":   300.0,
				"battery_pct": 55.0,
				"system_soc":  55.0,
			})
		}
	}

	shade := FitShadeModel(history, weather, ShadeOptions{
		Now:    now,
		Offset: offset,
		Lat:    lat,
		Lon:    lon,
		Plane:  &plane,
	})

	noonBegin := time.Date(2026, 11, 21, 12, 0, 0, 0, time.FixedZone("CST", offset)).Unix()
	h15Begin := time.Date(2026, 11, 21, 15, 0, 0, 0, time.FixedZone("CST", offset)).Unix()
	h16Begin := time.Date(2026, 11, 21, 16, 0, 0, 0, time.FixedZone("CST", offset)).Unix()

	sunnyNoonWx := Row{"ghi_w_m2": 560.0, "direct_w_m2": 476.0, "diffuse_w_m2": 84.0, "cloud_cover_pct": 5.0}
	sunny15Wx := Row{"ghi_w_m2": 280.0, "direct_w_m2": 238.0, "diffuse_w_m2": 42.0, "cloud_cover_pct": 5.0}
	cloudy15Wx := Row{"ghi_w_m2": 160.0, "direct_w_m2": 10.0, "diffuse_w_m2": 150.0, "cloud_cover_pct": 95.0}

	// Unshaded noon passes through near 100%.
	adjNoon, multNoon := shade.ApplyForecastSolar(2500, sunnyNoonWx, 12, noonBegin)
	if multNoon < 0.90 || adjNoon < 2250 {
		t.Fatalf("unshaded noon unexpectedly attenuated: adj=%.1f mult=%.3f", adjNoon, multNoon)
	}
	// Shaded 15:00 on a sunny day is strongly reduced (not left at 1.0!).
	adj15Sun, mult15Sun := shade.ApplyForecastSolar(1500, sunny15Wx, 15, h15Begin)
	if mult15Sun > 0.30 || adj15Sun > 450 {
		t.Fatalf("house-shaded sunny 15:00 still optimistic: adj=%.1f mult=%.3f (hs=%+v)", adj15Sun, mult15Sun, shade.Hours[15])
	}
	// Shaded 15:00 on a cloudy day preserves diffuse sky light much better than sunny beam.
	_, mult15Cloud := shade.ApplyForecastSolar(300, cloudy15Wx, 15, h15Begin)
	if mult15Cloud <= mult15Sun+0.25 {
		t.Fatalf("cloudy diffuse should be less attenuated than direct beam: cloud=%.3f sun=%.3f", mult15Cloud, mult15Sun)
	}
	// Completely blocked 16:00 (0 W actuals) is NOT dropped/reverted to 1.0!
	adj16Sun, mult16Sun := shade.ApplyForecastSolar(600, sunny15Wx, 16, h16Begin)
	if mult16Sun > 0.25 || adj16Sun > 150 {
		t.Fatalf("zero-solar shaded hour reverted to unshaded: adj=%.1f mult=%.3f (hs=%+v)", adj16Sun, mult16Sun, shade.Hours[16])
	}
}

func TestForecastSolarDirectHistoryCalibrationAndRecency(t *testing.T) {
	offset := 0
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC).Unix()
	var history, weather []Row
	fsHist := map[int64]float64{}
	for day := 12; day >= 1; day-- {
		begin := time.Date(2026, 11, 20-day, 15, 0, 0, 0, time.UTC).Unix()
		weather = append(weather, Row{"ts": float64(begin + 3600), "ghi_w_m2": 400.0, "direct_w_m2": 340.0, "diffuse_w_m2": 60.0, "cloud_cover_pct": 10.0})
		fsHist[begin] = 1000.0
		// Older days (8..12 days ago) were unshaded (900W); recent 5 days are blocked by low sun (150W).
		actual := 900.0
		if day <= 6 {
			actual = 150.0
		}
		history = append(history, Row{"ts": float64(begin), "solar_w": actual, "solar_wh": actual, "output_w": 200.0, "output_wh": 200.0, "battery_pct": 50.0})
	}
	shade := FitShadeModel(history, weather, ShadeOptions{Now: now, Offset: offset, FSHistory: fsHist})
	if shade.Hours[15].Source != "fs_direct" {
		t.Fatalf("expected fs_direct source, got %+v", shade.Hours[15])
	}
	adj, mult := shade.ApplyForecastSolar(1000, Row{"ghi_w_m2": 400.0, "direct_w_m2": 340.0, "diffuse_w_m2": 60.0, "cloud_cover_pct": 10.0}, 15, now+3*3600)
	if mult > 0.35 || adj > 350 {
		t.Fatalf("recency-weighted fs_direct shade failed to track recent shadow: adj=%.1f mult=%.3f", adj, mult)
	}
}

func TestMPPTCurtailmentNotMistakenForShade(t *testing.T) {
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC).Unix()
	var history, weather []Row
	for day := 6; day >= 1; day-- {
		// 11:00 unshaded with headroom (SOC 50%, 1000W)
		b11 := time.Date(2026, 11, 20-day, 11, 0, 0, 0, time.UTC).Unix()
		weather = append(weather, Row{"ts": float64(b11 + 3600), "ghi_w_m2": 500.0, "direct_w_m2": 425.0, "diffuse_w_m2": 75.0, "cloud_cover_pct": 10.0})
		history = append(history, Row{"ts": float64(b11), "solar_w": 1000.0, "output_w": 200.0, "battery_pct": 50.0})
		// 13:00 battery is FULL (SOC 96%), MPPT throttles solar down to 100W — NOT shade!
		b13 := time.Date(2026, 11, 20-day, 13, 0, 0, 0, time.UTC).Unix()
		weather = append(weather, Row{"ts": float64(b13 + 3600), "ghi_w_m2": 500.0, "direct_w_m2": 425.0, "diffuse_w_m2": 75.0, "cloud_cover_pct": 10.0})
		history = append(history, Row{"ts": float64(b13), "solar_w": 100.0, "output_w": 200.0, "battery_pct": 96.0})
	}
	shape := FitDiurnalShape(history, weather, 2.0, 0)
	if shape[13] < 0.95 {
		t.Fatalf("full-battery curtailment falsely reduced diurnal shape: shape[13]=%.3f", shape[13])
	}
	shade := FitShadeModel(history, weather, ShadeOptions{Now: now, Offset: 0, SolarCoeff: 2.0})
	if shade.Hours[13].Factor < 0.95 {
		t.Fatalf("full-battery curtailment falsely reduced shade factor: %+v", shade.Hours[13])
	}
}

func TestShadeTrainingRequiresCompleteMeasuredHours(t *testing.T) {
	now := time.Date(2026, 11, 20, 13, 50, 0, 0, time.UTC).Unix()
	history := []Row{}
	weather := []Row{}
	fsHistory := map[int64]float64{}
	for _, h := range []struct {
		begin           int64
		solar, coverage float64
	}{
		{now - 3*3600 - 3000, 800, 3600}, // measured, unshaded 10:00
		{now - 2*3600 - 3000, 0, 60},     // missing most of 11:00
		{now - 3600 - 3000, 0, 3600},     // measured zero at 12:00
		{now - 3000, 0, 3000},            // 13:00 is still in progress
	} {
		history = append(history, Row{"ts": h.begin, "solar_w": h.solar, "solar_coverage_s": h.coverage, "battery_pct": 50.0})
		weather = append(weather, Row{"ts": h.begin + 3600, "ghi_w_m2": 400.0, "direct_w_m2": 360.0, "diffuse_w_m2": 40.0})
		fsHistory[h.begin] = 800
	}
	model := FitShadeModel(history, weather, ShadeOptions{Now: now, FSHistory: fsHistory, SolarCoeff: 2})
	shape := FitDiurnalShape(history[:len(history)-1], weather, 2, 0)
	for _, hour := range []int{11, 13} {
		if model.Hours[hour].Samples != 0 || model.OpenMeteo[hour].Samples != 0 || shape[hour] != 1 {
			t.Errorf("%d:00 trained on incomplete data: %+v, %+v, shape=%v", hour, model.Hours[hour], model.OpenMeteo[hour], shape[hour])
		}
	}
	if model.OpenMeteo[12].Samples != 1 || shape[12] >= 1 {
		t.Fatalf("fully measured zero should reduce the estimate: %+v, shape=%v", model.OpenMeteo[12], shape[12])
	}
}

func TestWinterClearSkyAndLoadProfileOffsetAndOverrides(t *testing.T) {
	// Winter clear-sky noon GHI is 420 W/m² (< 700), with headroom SOC 50% -> k should fit cleanly.
	now := time.Date(2026, 11, 20, 18, 0, 0, 0, time.UTC).Unix()
	var history, weather []Row
	for day := 3; day >= 1; day-- {
		for h := 0; h < 24; h++ {
			begin := time.Date(2026, 11, 20-day, h, 0, 0, 0, time.UTC).Unix()
			ghi, sol := 0.0, 0.0
			if h == 18 { // 12:00 CST (UTC-6)
				ghi = 420.0
				sol = 840.0 // true k = 2.0
			}
			// Heavy evening load at 19:00–20:00 local CST (= 01:00–02:00 UTC next day)
			// spans >5% of hours so p95 outlier cap does not clip it.
			localHour := (h - 6 + 24) % 24
			outW := 100.0
			if localHour == 19 || localHour == 20 {
				outW = 900.0
			}
			weather = append(weather, Row{"ts": float64(begin + 3600), "ghi_w_m2": ghi, "cloud_cover_pct": 10.0})
			history = append(history, Row{"ts": float64(begin), "solar_w": sol, "solar_wh": sol, "output_w": outW, "output_wh": outW, "battery_pct": 50.0})
		}
	}
	k, n := FitSolarCoefficient(history, weather)
	if n < 3 || math.Abs(k-2.0) > 0.05 {
		t.Fatalf("winter clear-sky k fit failed: k=%.3f n=%d", k, n)
	}
	// Verify FitLoadProfileOffset buckets in device local timezone (UTC-6), not server UTC.
	prof := FitLoadProfileOffset(history, now, 1400, 3600, -6*3600)
	if prof[LoadKey{Hour: 19, Weekend: 0}] < 800 {
		t.Fatalf("expected 19:00 local load ~900W, got %v", prof[LoadKey{Hour: 19, Weekend: 0}])
	}
	// Verify Build respects user overrides.
	kOver, pOver, oOver, eOver := 1.25, 120.0, 0.15, 0.85
	futureWx := append([]Row(nil), weather...)
	for h := 1; h <= 24; h++ {
		futureWx = append(futureWx, Row{"ts": float64(now + int64(h)*3600), "ghi_w_m2": 400.0, "cloud_cover_pct": 10.0})
	}
	res := Build(history, futureWx, 50, 5040, BuildOptions{
		Now:                now,
		Offset:             -6 * 3600,
		Horizon:            24,
		SolarCoeffOverride: &kOver,
		ParasiticOverride:  &pOver,
		OverheadOverride:   &oOver,
		ChargeEffOverride:  &eOver,
	})
	if Number(res, "solar_coefficient") != 1.25 || Number(res, "parasitic_w") != 120 || Number(res, "inverter_overhead_pct") != 0.15 || Number(res, "charge_efficiency") != 0.85 {
		t.Fatalf("overrides not applied: k=%v p=%v o=%v eff=%v", res["solar_coefficient"], res["parasitic_w"], res["inverter_overhead_pct"], res["charge_efficiency"])
	}
	if res["inverter_overhead_source"] != "user" || res["charge_efficiency_source"] != "user" {
		t.Fatalf("expected user sources, got %v / %v", res["inverter_overhead_source"], res["charge_efficiency_source"])
	}
}
