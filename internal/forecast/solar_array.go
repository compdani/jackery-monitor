package forecast

import (
	"fmt"
	"math"
	"strings"
	"time"
)

type Plane struct {
	Declination int     `json:"declination"`
	Azimuth     int     `json:"azimuth"`
	KWP         float64 `json:"kwp"`
}

func ValidatePlane(dec, az, kwp float64) (Plane, bool) {
	d, a := math.RoundToEven(dec), math.RoundToEven(az)
	if math.IsNaN(d) || math.IsNaN(a) || math.IsNaN(kwp) || d < 0 || d > 90 || a < -180 || a > 180 || kwp < 0.01 || kwp > 100 {
		return Plane{}, false
	}
	return Plane{int(d), int(a), math.RoundToEven(kwp*10000) / 10000}, true
}
func SolarPosition(lat, lon float64, ts int64) (float64, float64) {
	t := time.Unix(ts, 0).UTC()
	hour := float64(t.Hour()) + float64(t.Minute())/60 + float64(t.Second())/3600
	fy := 2 * math.Pi / 365 * (float64(t.YearDay()-1) + (hour-12)/24)
	decl := 0.006918 - 0.399912*math.Cos(fy) + 0.070257*math.Sin(fy) - 0.006758*math.Cos(2*fy) + 0.000907*math.Sin(2*fy) - 0.002697*math.Cos(3*fy) + 0.00148*math.Sin(3*fy)
	eq := 229.18 * (0.000075 + 0.001868*math.Cos(fy) - 0.032077*math.Sin(fy) - 0.014615*math.Cos(2*fy) - 0.040849*math.Sin(2*fy))
	ha := ((hour*60+eq+4*lon)/4 - 180) * math.Pi / 180
	l := lat * math.Pi / 180
	z := math.Acos(max(-1, min(1, math.Sin(l)*math.Sin(decl)+math.Cos(l)*math.Cos(decl)*math.Cos(ha))))
	az := math.Atan2(-math.Sin(ha), math.Cos(ha)*math.Sin(l)-math.Tan(decl)*math.Cos(l))*180/math.Pi + 180
	return z * 180 / math.Pi, math.Mod(az+360, 360)
}
func POA(ghi, lat, lon float64, ts int64, tilt, az float64) float64 {
	if ghi <= 0 {
		return 0
	}
	z, a := SolarPosition(lat, lon, ts)
	if z >= 87 {
		return 0
	}
	z *= math.Pi / 180
	t := tilt * math.Pi / 180
	delta := (az - (a - 180)) * math.Pi / 180
	incidence := math.Cos(z)*math.Cos(t) + math.Sin(z)*math.Sin(t)*math.Cos(delta)
	if incidence <= 0 {
		return 0
	}
	return ghi * incidence / max(math.Cos(z), 0.12)
}

type planePair struct {
	ts         int64
	ghi, solar float64
}

func planePairs(energy, weather []Row) []planePair {
	byHour := map[int64]float64{}
	for _, r := range energy {
		ts := int64(Number(r, "ts"))
		if ts <= 0 {
			continue
		}
		h := ts / 3600 * 3600
		byHour[h] = max(byHour[h], Number(r, "solar_w"))
	}
	out := []planePair{}
	for _, r := range weather {
		ts := int64(Number(r, "ts"))
		if ts <= 0 {
			continue
		}
		h := ts / 3600 * 3600
		g := Number(r, "ghi_w_m2")
		s := byHour[h]
		if g >= 80 && s >= 50 {
			out = append(out, planePair{h, g, s})
		}
	}
	return out
}
func correlation(x, y []float64) float64 {
	if len(x) < 3 {
		return 0
	}
	mx, my := 0.0, 0.0
	for i := range x {
		mx += x[i]
		my += y[i]
	}
	mx /= float64(len(x))
	my /= float64(len(y))
	num, dx, dy := 0.0, 0.0, 0.0
	for i := range x {
		a, b := x[i]-mx, y[i]-my
		num += a * b
		dx += a * a
		dy += b * b
	}
	if dx <= 0 || dy <= 0 {
		return 0
	}
	return num / math.Sqrt(dx*dy)
}
func InferPlane(energy, weather []Row, lat, lon float64) map[string]any {
	pairs := planePairs(energy, weather)
	peak := 0.0
	for _, p := range pairs {
		peak = max(peak, p.solar)
	}
	tilt := int(math.RoundToEven(min(60, math.Abs(lat))))
	az := 0
	if lat < 0 {
		az = 180
	}
	kwp := math.RoundToEven(max(0.05, peak/850)*100) / 100
	result := func(t, a int, k, r float64, notes string) map[string]any {
		conf := "low"
		if r >= 0.85 {
			conf = "high"
		} else if r >= 0.6 {
			conf = "medium"
		}
		return map[string]any{"declination": t, "azimuth": a, "kwp": k, "n_samples": len(pairs), "correlation": math.RoundToEven(r*1000) / 1000, "confidence": conf, "notes": notes}
	}
	if len(pairs) < 12 || peak < 50 {
		if peak <= 0 {
			return map[string]any{"error": "not enough solar history to infer the array (need ~12 daylight hours of production + GHI)", "n_samples": len(pairs)}
		}
		return result(tilt, az, kwp, 0, "Too few paired hours for a geometry search; tilt≈latitude and kWp from peak watts / 850.")
	}
	azimuths := []int{}
	if lat >= 0 {
		for a := -90; a <= 90; a += 15 {
			azimuths = append(azimuths, a)
		}
	} else {
		for a := 90; a <= 180; a += 15 {
			azimuths = append(azimuths, a)
		}
		for a := -180; a <= -90; a += 15 {
			azimuths = append(azimuths, a)
		}
	}
	sols := make([]float64, len(pairs))
	for i, p := range pairs {
		sols[i] = p.solar
	}
	best := math.Inf(-1)
	for t := 0; t <= 60; t += 5 {
		for _, a := range azimuths {
			poas := make([]float64, len(pairs))
			peakPOA := 0.0
			for i, p := range pairs {
				poas[i] = POA(p.ghi, lat, lon, p.ts, float64(t), float64(a))
				peakPOA = max(peakPOA, poas[i])
			}
			if peakPOA <= 1 {
				continue
			}
			r := correlation(poas, sols)
			if r > best {
				best = r
				tilt = t
				az = a
			}
		}
	}
	if math.IsInf(best, -1) {
		return result(tilt, az, kwp, 0, "Geometry search found no sun-up hours; used latitude fallback.")
	}
	sxx, sxy := 0.0, 0.0
	for _, p := range pairs {
		v := POA(p.ghi, lat, lon, p.ts, float64(tilt), float64(az))
		sxx += v * v
		sxy += v * p.solar
	}
	fit := kwp
	if sxx > 0 {
		fit = sxy / sxx / 0.8
	}
	kwp = math.RoundToEven(max(0.05, min(50, 0.5*fit+0.5*peak/850))*100) / 100
	notes := fmt.Sprintf("Best match among tilt 0–60° and azimuth −90…90 (0=south): r=%.2f on %d daylight hours. Review before saving — shade and MPPT clipping can skew the fit.", best, len(pairs))
	if lat < 0 {
		notes = strings.ReplaceAll(notes, "−90…90", "north-facing 90…180")
	}
	out := result(tilt, az, kwp, best, notes)
	out["peak_solar_w"] = math.RoundToEven(peak*10) / 10
	return out
}
