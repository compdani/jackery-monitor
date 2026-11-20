package forecast

import (
	"math"
	"sort"
	"strconv"
	"time"
)

const (
	shadeHalfLifeDays = 5.0
	shadePriorWeight  = 0.40
	shadeDiffuseFloor = 0.70
	minShadeCoverageS = 45 * 60
)

func hasShadeCoverage(r Row) bool {
	// Callers with preaggregated hourly data may omit minute coverage.
	return r["solar_coverage_s"] == nil || Number(r, "solar_coverage_s") >= minShadeCoverageS
}

type ShadeOptions struct {
	Now        int64
	Offset     int
	Lat, Lon   float64
	Plane      *Plane
	FSHistory  map[int64]float64
	SolarCoeff float64
}

type HourShade struct {
	Beam    float64 `json:"beam"`
	Diffuse float64 `json:"diffuse"`
	Factor  float64 `json:"factor"`
	Samples int     `json:"samples"`
	Source  string  `json:"source"`
}

type ShadeModel struct {
	Hours       map[int]HourShade
	OpenMeteo   map[int]HourShade
	FSScale     float64
	FSScaleN    int
	Lat, Lon    float64
	Plane       *Plane
	HasDirect   bool
	HasDaylight bool
}

type shadeSample struct {
	ratio    float64
	beamFrac float64
	weight   float64
}

func recencyWeight(ts, refNow int64, halfLifeDays float64) float64 {
	if refNow <= 0 || halfLifeDays <= 0 {
		return 1
	}
	ageDays := math.Max(0, float64(refNow-ts)/86400.0)
	return math.Pow(0.5, ageDays/halfLifeDays)
}

func weightedQuantile(vals, weights []float64, q float64) float64 {
	n := len(vals)
	if n == 0 {
		return 0
	}
	idx := make([]int, n)
	total := 0.0
	for i := range idx {
		idx[i] = i
		total += weights[i]
	}
	if total <= 0 {
		return median(append([]float64(nil), vals...))
	}
	sort.Slice(idx, func(a, b int) bool { return vals[idx[a]] < vals[idx[b]] })
	target := clamp(q, 0, 1) * total
	cum := 0.0
	for _, i := range idx {
		cum += weights[i]
		if cum >= target {
			return vals[i]
		}
	}
	return vals[idx[n-1]]
}

func weatherBeamComponents(w Row, lat, lon float64, plane *Plane, tsBegin int64) (irrad, beamFrac float64, hasDirect bool) {
	ghi := Number(w, "ghi_w_m2")
	cloud := Number(w, "cloud_cover_pct")
	hasDirect = w["direct_w_m2"] != nil || w["diffuse_w_m2"] != nil
	direct := Number(w, "direct_w_m2")
	diffuse := Number(w, "diffuse_w_m2")
	if ValidLocation(lat, lon) && plane != nil {
		pd, pf := POAComponents(ghi, direct, diffuse, lat, lon, tsBegin+1800, float64(plane.Declination), float64(plane.Azimuth))
		irrad = pd + pf
		if irrad > 0 {
			if hasDirect {
				beamFrac = clamp(pd/irrad, 0, 1)
			} else {
				beamFrac = clamp(1-0.75*cloud/100, 0.05, 0.95)
			}
		}
		return irrad, beamFrac, hasDirect
	}
	irrad = ghi
	if hasDirect && direct+diffuse > 0 {
		beamFrac = clamp(direct/(direct+diffuse), 0, 1)
	} else if irrad > 0 {
		beamFrac = clamp(1-0.75*cloud/100, 0.05, 0.95)
	}
	return irrad, beamFrac, hasDirect
}

func fitBeamDiffuse(samples []shadeSample, capMax float64, source string) HourShade {
	if len(samples) == 0 {
		return HourShade{Beam: 1, Diffuse: 1, Factor: 1, Source: "prior"}
	}
	var allV, allW []float64
	var sunV, sunW []float64
	var cldV, cldW []float64
	wAll, wSun, wCld, fSum := 0.0, 0.0, 0.0, 0.0
	for _, s := range samples {
		allV = append(allV, s.ratio)
		allW = append(allW, s.weight)
		wAll += s.weight
		if s.beamFrac >= 0.50 {
			sunV = append(sunV, s.ratio)
			sunW = append(sunW, s.weight)
			wSun += s.weight
			fSum += s.beamFrac * s.weight
		} else if s.beamFrac < 0.40 {
			cldV = append(cldV, s.ratio)
			cldW = append(cldW, s.weight)
			wCld += s.weight
		}
	}
	if wSun >= 0.50 {
		rSun := weightedQuantile(sunV, sunW, 0.5)
		sunnyFactor := clamp((rSun*wSun+shadePriorWeight*1.0)/(wSun+shadePriorWeight), 0, capMax)
		if sunnyFactor >= 0.92 {
			dif := clamp(math.Min(1.0, capMax), 0, capMax)
			return HourShade{Beam: sunnyFactor, Diffuse: dif, Factor: sunnyFactor, Samples: len(samples), Source: source}
		}
		dif := math.Min(math.Min(1.0, capMax), math.Max(sunnyFactor, shadeDiffuseFloor))
		if wCld >= 0.75 {
			rCld := weightedQuantile(cldV, cldW, 0.5)
			dif = clamp((rCld*wCld+0.5*shadeDiffuseFloor)/(wCld+0.5), sunnyFactor, math.Min(1.0, capMax))
		}
		fBar := clamp(fSum/wSun, 0.50, 0.95)
		beam := clamp((sunnyFactor-(1-fBar)*dif)/fBar, 0, capMax)
		return HourShade{Beam: beam, Diffuse: dif, Factor: sunnyFactor, Samples: len(samples), Source: source}
	}
	rAll := weightedQuantile(allV, allW, 0.5)
	f := clamp((rAll*wAll+0.5*1.0)/(wAll+0.5), 0, capMax)
	return HourShade{Beam: f, Diffuse: f, Factor: f, Samples: len(samples), Source: source}
}

// FitShadeModel learns per-local-hour beam/diffuse shading attenuation for both
// Forecast.Solar and Open-Meteo hours, keeping zero-solar shaded daylight hours,
// filtering MPPT curtailment at high SOC, and weighting recent days higher so
// seasonal low-sun shadows adapt within days.
func FitShadeModel(history, weather []Row, opts ShadeOptions) ShadeModel {
	m := ShadeModel{
		Hours:     make(map[int]HourShade, 24),
		OpenMeteo: make(map[int]HourShade, 24),
		FSScale:   1.0,
		Lat:       opts.Lat,
		Lon:       opts.Lon,
		Plane:     opts.Plane,
	}
	for h := 0; h < 24; h++ {
		m.Hours[h] = HourShade{Beam: 1, Diffuse: 1, Factor: 1, Source: "prior"}
		m.OpenMeteo[h] = HourShade{Beam: 1, Diffuse: 1, Factor: 1, Source: "prior"}
	}
	if len(history) == 0 {
		return m
	}
	solar := map[int64]float64{}
	hasEnergy := map[int64]bool{}
	soc := map[int64]float64{}
	hasSOC := map[int64]bool{}
	ac := map[int64]bool{}
	refNow := opts.Now
	for _, r := range history {
		ts := int64(Number(r, "ts"))
		if ts <= 0 || !hasShadeCoverage(r) {
			continue
		}
		h := ts / 3600 * 3600
		if opts.Now > 0 && h+3600 > opts.Now {
			continue
		}
		hasEnergy[h] = true
		if h+3600 > refNow && opts.Now == 0 {
			refNow = h + 3600
		}
		if s := Number(r, "solar_w"); s > solar[h] {
			solar[h] = s
		}
		if v := RowSOC(r); v != nil {
			if !hasSOC[h] || *v < soc[h] {
				soc[h] = *v
				hasSOC[h] = true
			}
		}
		if Number(r, "ac_input_w") > 50 {
			ac[h] = true
		}
	}

	wxByBegin := map[int64]Row{}
	for _, w := range weather {
		tsEnd := int64(Number(w, "ts"))
		if tsEnd <= 0 {
			continue
		}
		begin := tsEnd/3600*3600 - 3600
		wxByBegin[begin] = w
		if w["direct_w_m2"] != nil || w["diffuse_w_m2"] != nil {
			m.HasDirect = true
		}
	}

	isCurtailed := func(h int64, ratio float64) bool {
		if ac[h] {
			return true
		}
		if hasSOC[h] && soc[h] >= 85 && ratio < 0.85 {
			return true
		}
		return false
	}

	// 1. Fit POA/GHI-based per-hour shading (available immediately from 14-day history).
	type obs struct {
		tsBegin  int64
		lhod     int
		irrad    float64
		beamFrac float64
		sol      float64
		weight   float64
	}
	var allObs []obs
	var peakVals, peakWeights []float64
	var peakSunVals, peakSunWeights []float64
	poaSource := "ghi"
	if ValidLocation(opts.Lat, opts.Lon) && opts.Plane != nil {
		poaSource = "poa"
	}
	for begin, w := range wxByBegin {
		if !hasEnergy[begin] {
			continue
		}
		irrad, beamFrac, _ := weatherBeamComponents(w, opts.Lat, opts.Lon, opts.Plane, begin)
		if irrad < 50 {
			continue
		}
		m.HasDaylight = true
		wt := recencyWeight(begin, refNow, shadeHalfLifeDays)
		lhod := time.Unix(begin+int64(opts.Offset), 0).UTC().Hour()
		sol := solar[begin]
		allObs = append(allObs, obs{begin, lhod, irrad, beamFrac, sol, wt})
		if irrad >= 80 && sol > 0 && !ac[begin] && (!hasSOC[begin] || soc[begin] <= 75) {
			r := sol / irrad
			peakVals = append(peakVals, r)
			peakWeights = append(peakWeights, wt)
			if beamFrac >= 0.40 {
				peakSunVals = append(peakSunVals, r)
				peakSunWeights = append(peakSunWeights, wt)
			}
		}
	}

	kPeak := 0.0
	if len(peakSunVals) >= 3 {
		kPeak = weightedQuantile(peakSunVals, peakSunWeights, 0.85)
	} else if len(peakVals) >= 2 {
		kPeak = weightedQuantile(peakVals, peakWeights, 0.85)
	} else if opts.SolarCoeff > 0 {
		kPeak = opts.SolarCoeff
	}

	poaBuckets := map[int][]shadeSample{}
	omBuckets := map[int][]shadeSample{}
	for _, o := range allObs {
		if kPeak > 0 {
			ratio := clamp(o.sol/(kPeak*o.irrad), 0, 1.5)
			if !isCurtailed(o.tsBegin, ratio) {
				poaBuckets[o.lhod] = append(poaBuckets[o.lhod], shadeSample{ratio: ratio, beamFrac: o.beamFrac, weight: o.weight})
			}
		}
		if opts.SolarCoeff > 0 {
			ghi := Number(wxByBegin[o.tsBegin], "ghi_w_m2")
			if ghi > 50 {
				ratio := clamp(o.sol/(opts.SolarCoeff*ghi), 0, 3.0)
				if !isCurtailed(o.tsBegin, ratio) {
					omBuckets[o.lhod] = append(omBuckets[o.lhod], shadeSample{ratio: ratio, beamFrac: o.beamFrac, weight: o.weight})
				}
			}
		}
	}

	poaShade := map[int]HourShade{}
	for h := 0; h < 24; h++ {
		if len(poaBuckets[h]) > 0 {
			poaShade[h] = fitBeamDiffuse(poaBuckets[h], 1.0, poaSource)
			m.Hours[h] = poaShade[h]
		} else {
			poaShade[h] = HourShade{Beam: 1, Diffuse: 1, Factor: 1, Source: "prior"}
		}
		if len(omBuckets[h]) > 0 {
			m.OpenMeteo[h] = fitBeamDiffuse(omBuckets[h], 3.0, "ghi")
		}
	}

	// 2. Direct Forecast.Solar per-hour calibration + global scale when history exists.
	fsBuckets := map[int][]shadeSample{}
	fsWeight := map[int]float64{}
	var scaleVals, scaleWeights []float64
	scaleWTotal := 0.0
	for begin, fsW := range opts.FSHistory {
		if fsW < 30 || !hasEnergy[begin] {
			continue
		}
		ratio := clamp(solar[begin]/fsW, 0, 1.5)
		if isCurtailed(begin, ratio) {
			continue
		}
		beamFrac := 0.75
		if w, ok := wxByBegin[begin]; ok {
			_, beamFrac, _ = weatherBeamComponents(w, opts.Lat, opts.Lon, opts.Plane, begin)
		}
		wt := recencyWeight(begin, refNow, shadeHalfLifeDays)
		lhod := time.Unix(begin+int64(opts.Offset), 0).UTC().Hour()
		fsBuckets[lhod] = append(fsBuckets[lhod], shadeSample{ratio: ratio, beamFrac: beamFrac, weight: wt})
		fsWeight[lhod] += wt

		if fsW >= 50 {
			ps := poaShade[lhod]
			poaMult := clamp(beamFrac*ps.Beam+(1-beamFrac)*ps.Diffuse, 0.05, 1.0)
			if poaMult >= 0.40 {
				res := clamp(solar[begin]/(fsW*poaMult), 0.25, 1.5)
				scaleVals = append(scaleVals, res)
				scaleWeights = append(scaleWeights, wt)
				scaleWTotal += wt
			}
		}
	}
	for h, samples := range fsBuckets {
		if fsWeight[h] >= 1.5 {
			m.Hours[h] = fitBeamDiffuse(samples, 1.25, "fs_direct")
		}
	}
	if scaleWTotal >= 1.5 {
		rScale := weightedQuantile(scaleVals, scaleWeights, 0.5)
		m.FSScale = clamp((rScale*scaleWTotal+1.5*1.0)/(scaleWTotal+1.5), 0.40, 1.25)
		m.FSScaleN = len(scaleVals)
	}
	return m
}

// ApplyForecastSolar adjusts raw unshaded Forecast.Solar watts for learned
// per-hour beam/diffuse shading and global array calibration.
func (m ShadeModel) ApplyForecastSolar(fsW float64, w Row, localHour int, tsBegin int64) (adjusted, mult float64) {
	if fsW <= 0 {
		return 0, 1
	}
	hs, ok := m.Hours[localHour]
	if !ok {
		return fsW, 1
	}
	_, beamFrac, _ := weatherBeamComponents(w, m.Lat, m.Lon, m.Plane, tsBegin)
	mult = clamp(beamFrac*hs.Beam+(1-beamFrac)*hs.Diffuse, 0, 1.25)
	if hs.Source != "fs_direct" {
		mult *= m.FSScale
	}
	return math.Max(0, fsW*mult), mult
}

// ApplyOpenMeteo adjusts k*GHI using beam/diffuse-aware shading when direct
// radiation is available, otherwise falling back to the learned diurnal factor.
func (m ShadeModel) ApplyOpenMeteo(k, ghi float64, w Row, localHour int, tsBegin int64, fallbackShape float64) (adjusted, mult float64) {
	if k <= 0 || ghi <= 0 {
		return 0, fallbackShape
	}
	hs, ok := m.OpenMeteo[localHour]
	if !ok || hs.Samples == 0 {
		return math.Max(0, k*ghi*fallbackShape), fallbackShape
	}
	_, beamFrac, hasDirect := weatherBeamComponents(w, m.Lat, m.Lon, m.Plane, tsBegin)
	if !hasDirect && !m.HasDirect {
		return math.Max(0, k*ghi*fallbackShape), fallbackShape
	}
	mult = math.Max(0, beamFrac*hs.Beam+(1-beamFrac)*hs.Diffuse)
	return math.Max(0, k*ghi*mult), mult
}

// Diagnostics exposes the learned per-hour shade factors for API/UI inspection.
func (m ShadeModel) Diagnostics() map[string]any {
	hours := map[string]any{}
	for h := 0; h < 24; h++ {
		hs := m.Hours[h]
		if hs.Samples == 0 && hs.Source == "prior" {
			continue
		}
		hours[strconv.Itoa(h)] = map[string]any{
			"beam":    round(hs.Beam, 3),
			"diffuse": round(hs.Diffuse, 3),
			"factor":  round(hs.Factor, 3),
			"samples": hs.Samples,
			"source":  hs.Source,
		}
	}
	return map[string]any{
		"fs_scale":         round(m.FSScale, 3),
		"fs_scale_samples": m.FSScaleN,
		"has_direct_split": m.HasDirect,
		"hours":            hours,
	}
}
