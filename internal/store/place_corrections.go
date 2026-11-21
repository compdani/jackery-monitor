package store

import (
	"math"
	"sort"
	"time"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
)

// PlaceCorrection is an address the driver picked for a Flex pin.
type PlaceCorrection struct {
	ID       string  `json:"id"`
	PinLat   float64 `json:"pin_lat"`
	PinLng   float64 `json:"pin_lng"`
	PlaceID  string  `json:"place_id"`
	Address  string  `json:"address,omitempty"`
	Lat      float64 `json:"lat"`
	Lng      float64 `json:"lng"`
	Uses     int     `json:"uses"`
	LastUsed int64   `json:"last_used,omitempty"`
	Created  string  `json:"created,omitempty"`
	// DistanceM: how far the pin it was found for is from PinLat/PinLng.
	DistanceM float64 `json:"distance_m,omitempty"`
}

const placeCorrections = "place_corrections"

// CorrectionMatchM is how close a new pin must be to a saved one for the
// correction to apply. Amazon repeats a pin almost exactly for the same
// address; neighbours' parking pins can be close, so this stays tight.
const CorrectionMatchM = 6

func distM(aLat, aLng, bLat, bLng float64) float64 {
	const r = 6371000
	rad := math.Pi / 180
	dLat, dLng := (bLat-aLat)*rad, (bLng-aLng)*rad
	h := math.Pow(math.Sin(dLat/2), 2) + math.Cos(aLat*rad)*math.Cos(bLat*rad)*math.Pow(math.Sin(dLng/2), 2)
	return 2 * r * math.Asin(math.Min(1, math.Sqrt(h)))
}

func correctionFrom(rec *core.Record) PlaceCorrection {
	return PlaceCorrection{
		ID: rec.Id, PinLat: rec.GetFloat("pin_lat"), PinLng: rec.GetFloat("pin_lng"),
		PlaceID: rec.GetString("place_id"), Address: rec.GetString("address"),
		Lat: rec.GetFloat("lat"), Lng: rec.GetFloat("lng"),
		Uses: rec.GetInt("uses"), LastUsed: int64(rec.GetFloat("last_used")), Created: rec.GetString("created"),
	}
}

// near returns saved corrections within m metres of the pin, nearest first.
func (s *Store) nearCorrections(app core.App, lat, lng, m float64) ([]*core.Record, []float64, error) {
	// A small box first (1e-4° ≈ 11 m), then exact distance.
	d := m/111000 + 1e-5
	dl := d / math.Max(math.Cos(lat*math.Pi/180), 0.2)
	recs, err := app.FindRecordsByFilter(placeCorrections,
		"pin_lat >= {:a} && pin_lat <= {:b} && pin_lng >= {:c} && pin_lng <= {:d}", "", 50, 0,
		dbx.Params{"a": lat - d, "b": lat + d, "c": lng - dl, "d": lng + dl})
	if err != nil {
		return nil, nil, err
	}
	type pair struct {
		r *core.Record
		d float64
	}
	var ps []pair
	for _, r := range recs {
		if x := distM(lat, lng, r.GetFloat("pin_lat"), r.GetFloat("pin_lng")); x <= m {
			ps = append(ps, pair{r, x})
		}
	}
	sort.Slice(ps, func(i, j int) bool { return ps[i].d < ps[j].d })
	out := make([]*core.Record, len(ps))
	ds := make([]float64, len(ps))
	for i, p := range ps {
		out[i], ds[i] = p.r, p.d
	}
	return out, ds, nil
}

// FindPlaceCorrection returns the saved pick for a pin within
// CorrectionMatchM, if any, and counts the use.
func (s *Store) FindPlaceCorrection(lat, lng float64) (*PlaceCorrection, error) {
	recs, ds, err := s.nearCorrections(s.App, lat, lng, CorrectionMatchM)
	if err != nil || len(recs) == 0 {
		return nil, err
	}
	rec := recs[0]
	rec.Set("uses", rec.GetInt("uses")+1)
	rec.Set("last_used", time.Now().Unix())
	_ = s.App.Save(rec)
	c := correctionFrom(rec)
	c.DistanceM = math.Round(ds[0]*10) / 10
	return &c, nil
}

// SavePlaceCorrection records the pick for a pin, replacing any saved pick
// at the same spot.
func (s *Store) SavePlaceCorrection(c PlaceCorrection) (PlaceCorrection, error) {
	var out PlaceCorrection
	err := s.App.RunInTransaction(func(app core.App) error {
		recs, _, err := s.nearCorrections(app, c.PinLat, c.PinLng, CorrectionMatchM)
		if err != nil {
			return err
		}
		var rec *core.Record
		if len(recs) > 0 {
			rec = recs[0]
			for _, extra := range recs[1:] { // one pick per spot
				if err := app.Delete(extra); err != nil {
					return err
				}
			}
		} else {
			col, err := app.FindCollectionByNameOrId(placeCorrections)
			if err != nil {
				return err
			}
			rec = core.NewRecord(col)
		}
		rec.Set("pin_lat", c.PinLat)
		rec.Set("pin_lng", c.PinLng)
		rec.Set("place_id", c.PlaceID)
		rec.Set("address", c.Address)
		rec.Set("lat", c.Lat)
		rec.Set("lng", c.Lng)
		rec.Set("last_used", time.Now().Unix())
		if err := app.Save(rec); err != nil {
			return err
		}
		out = correctionFrom(rec)
		return nil
	})
	return out, err
}

// ListPlaceCorrections returns all saved picks, most recently used first.
func (s *Store) ListPlaceCorrections() ([]PlaceCorrection, error) {
	recs, err := s.App.FindRecordsByFilter(placeCorrections, "", "-last_used", 0, 0)
	if err != nil {
		return nil, err
	}
	out := make([]PlaceCorrection, len(recs))
	for i, r := range recs {
		out[i] = correctionFrom(r)
	}
	return out, nil
}

// DeletePlaceCorrection forgets a saved pick.
func (s *Store) DeletePlaceCorrection(id string) error {
	rec, err := s.App.FindRecordById(placeCorrections, id)
	if err != nil {
		return err
	}
	return s.App.Delete(rec)
}
