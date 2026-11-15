package tesla

import (
	"fmt"
	"math"
)

// RateFunc returns the home electricity price per kWh at a Unix time.
type RateFunc func(ts int64) float64

// Session is one charging session, built from successive ChargeSamples.
type Session struct {
	Key     string `json:"key"`
	VIN     string `json:"vin"`
	StartTS int64  `json:"start_ts"`
	// EndTS is the last time energy was seen increasing; UnpluggedTS is
	// when the session closed.
	EndTS       int64   `json:"end_ts"`
	UnpluggedTS int64   `json:"unplugged_ts,omitempty"`
	LastTS      int64   `json:"last_ts"`
	Active      bool    `json:"active"`
	StartLevel  int     `json:"start_level"`
	EndLevel    int     `json:"end_level"`
	AtHome      bool    `json:"at_home"`
	FastCharger bool    `json:"fast_charger"`
	PeakPowerKW float64 `json:"peak_power_kw"`
	AddedKWh    float64 `json:"energy_added_kwh"`
	WallKWh     float64 `json:"energy_wall_kwh"`
	Cost        float64 `json:"cost"`
	Currency    string  `json:"currency"`
	Efficiency  float64 `json:"efficiency"`
	Samples     int     `json:"samples"`
	// Unpriced is battery energy added before the first observation of the
	// session (or away from home), which has no reliable rate.
	UnpricedKWh float64 `json:"unpriced_kwh"`
}

// Tracker turns charge samples into sessions. It keeps no I/O; the caller
// persists Current after each Observe and restores it on restart.
type Tracker struct {
	VIN        string
	Efficiency float64
	Currency   string
	Rate       RateFunc
	// Home reports whether a sample was taken at home. When nil, every
	// non-fast-charger session counts as home.
	Home    func(ChargeSample) bool
	Current *Session
}

func round(v float64, places int) float64 {
	f := math.Pow10(places)
	return math.Round(v*f) / f
}

// Observe folds one sample in and returns the session it finished, if any.
//
// A session follows Tesla's charge_energy_added counter, which runs from
// plug-in to unplug: it starts at the first sample that is charging and
// ends when the cable is disconnected or the counter resets (a new plug-in
// between observations). Paused states (Stopped, Complete, NoPower) keep it
// open so energy is never counted twice.
func (t *Tracker) Observe(s ChargeSample) (finished *Session) {
	eff := t.Efficiency
	if eff <= 0 || eff > 1 {
		eff = 0.9
	}
	if cur := t.Current; cur != nil && cur.Active {
		if s.TS < cur.LastTS {
			return nil // stale or duplicate reading
		}
		if !s.PluggedIn() {
			return t.close(s.TS)
		}
		if s.EnergyAddedKWh+0.05 < cur.AddedKWh {
			finished = t.close(cur.LastTS)
		} else {
			if delta := s.EnergyAddedKWh - cur.AddedKWh; delta > 0 {
				mid := cur.LastTS + (s.TS-cur.LastTS)/2
				t.addEnergy(cur, delta, mid, false)
				cur.AddedKWh = s.EnergyAddedKWh
				cur.EndTS = s.TS
			}
			t.update(cur, s)
			return nil
		}
	}
	if !s.Charging() {
		return finished
	}
	home := !s.FastCharger
	if t.Home != nil && !s.FastCharger {
		home = t.Home(s)
	}
	cur := &Session{
		Key: fmt.Sprintf("%s:%d", t.VIN, s.TS), VIN: t.VIN,
		StartTS: s.TS, EndTS: s.TS, LastTS: s.TS, Active: true,
		StartLevel: s.BatteryLevel, EndLevel: s.BatteryLevel,
		AtHome: home, FastCharger: s.FastCharger, Currency: t.Currency, Efficiency: eff,
	}
	// Energy already added before the first observation: price it at the
	// current rate but also report it as unpriced, so the estimate's
	// uncertainty is visible.
	if s.EnergyAddedKWh > 0 {
		t.addEnergy(cur, s.EnergyAddedKWh, s.TS, true)
	}
	cur.AddedKWh = s.EnergyAddedKWh
	t.Current = cur
	t.update(cur, s)
	return finished
}

func (t *Tracker) update(cur *Session, s ChargeSample) {
	cur.LastTS = s.TS
	cur.Samples++
	if s.BatteryLevel > 0 {
		cur.EndLevel = s.BatteryLevel
	}
	if s.PowerKW > cur.PeakPowerKW {
		cur.PeakPowerKW = s.PowerKW
	}
}

func (t *Tracker) addEnergy(cur *Session, addedKWh float64, ts int64, unpriced bool) {
	wall := addedKWh / cur.Efficiency
	cur.WallKWh = round(cur.WallKWh+wall, 4)
	if !cur.AtHome || t.Rate == nil {
		cur.UnpricedKWh = round(cur.UnpricedKWh+wall, 4)
		return
	}
	if unpriced {
		cur.UnpricedKWh = round(cur.UnpricedKWh+wall, 4)
	}
	cur.Cost = round(cur.Cost+wall*t.Rate(ts), 4)
}

func (t *Tracker) close(unplugTS int64) *Session {
	cur := t.Current
	if cur == nil || !cur.Active {
		return nil
	}
	cur.Active = false
	cur.UnpluggedTS = unplugTS
	cur.Cost = round(cur.Cost, 2)
	t.Current = nil
	return cur
}

// DistanceM is the great-circle distance between two points in meters.
func DistanceM(lat1, lng1, lat2, lng2 float64) float64 {
	const r = 6371000.0
	rad := math.Pi / 180
	dLat := (lat2 - lat1) * rad
	dLng := (lng2 - lng1) * rad
	a := math.Sin(dLat/2)*math.Sin(dLat/2) + math.Cos(lat1*rad)*math.Cos(lat2*rad)*math.Sin(dLng/2)*math.Sin(dLng/2)
	return 2 * r * math.Asin(math.Min(1, math.Sqrt(a)))
}
