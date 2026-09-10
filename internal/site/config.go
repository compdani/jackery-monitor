// Package site stores installation coordinates encrypted in PocketBase.
package site

import (
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"path/filepath"
	"strings"
	"time"

	"jackery-monitor/internal/secrets"

	"github.com/pocketbase/pocketbase/core"
)

type Location struct {
	Latitude  *float64 `json:"latitude"`
	Longitude *float64 `json:"longitude"`
	Updated   float64  `json:"updated_at"`
	Label     string   `json:"label,omitempty"`
	Timezone  string   `json:"timezone,omitempty"`
	Offset    *int     `json:"utc_offset_seconds,omitempty"`
}

func (l Location) Configured() bool { return l.Latitude != nil && l.Longitude != nil }
func (l Location) Validate() error {
	if !l.Configured() || math.IsNaN(*l.Latitude) || math.IsNaN(*l.Longitude) || math.Abs(*l.Latitude) > 90 || math.Abs(*l.Longitude) > 180 || (*l.Latitude == 0 && *l.Longitude == 0) {
		return errors.New("latitude/longitude out of range")
	}
	if l.Timezone != "" {
		if _, err := time.LoadLocation(l.Timezone); err != nil {
			return errors.New("invalid timezone")
		}
	}
	if l.Offset != nil && (*l.Offset < -43200 || *l.Offset > 50400) {
		return errors.New("invalid UTC offset")
	}
	return nil
}
func (l Location) Zone() *time.Location {
	if l.Timezone != "" {
		if zone, err := time.LoadLocation(l.Timezone); err == nil {
			return zone
		}
	}
	if l.Offset != nil {
		return time.FixedZone("device", *l.Offset)
	}
	return time.Local
}
func Read(app core.App, collection string, value any) (bool, error) {
	r, err := app.FindFirstRecordByData(collection, "key", "default")
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, r.UnmarshalJSONField("data", value)
}
func Write(app core.App, collection string, value any) error {
	return app.RunInTransaction(func(tx core.App) error {
		r, err := tx.FindFirstRecordByData(collection, "key", "default")
		if errors.Is(err, sql.ErrNoRows) {
			c, e := tx.FindCollectionByNameOrId(collection)
			if e != nil {
				return e
			}
			r = core.NewRecord(c)
			r.Set("key", "default")
		} else if err != nil {
			return err
		}
		r.Set("data", value)
		return tx.Save(r)
	})
}
func crypt(app core.App) *secrets.Store {
	return &secrets.Store{KeyPath: secrets.KeyFile(filepath.Dir(app.DataDir()))}
}
func Get(app core.App) (Location, error) {
	var raw json.RawMessage
	var l Location
	found, err := Read(app, "location", &raw)
	if err != nil || !found {
		return l, err
	}
	var blob map[string]any
	if err = json.Unmarshal(raw, &blob); err != nil {
		return l, err
	}
	if _, encrypted := blob["ct"]; encrypted {
		err = crypt(app).Open(raw, &l)
	} else {
		err = json.Unmarshal(raw, &l)
	}
	return l, err
}
func Save(app core.App, l Location) error {
	l.Label = strings.TrimSpace(l.Label)
	l.Label = string([]rune(l.Label)[:min(200, len([]rune(l.Label)))])
	raw, err := crypt(app).Seal(l)
	if err != nil {
		return err
	}
	return Write(app, "location", raw)
}

// Upgrade encrypts legacy plaintext once; encrypted imports are validated, never regenerated.
func Upgrade(app core.App) error {
	var raw map[string]any
	found, err := Read(app, "location", &raw)
	if err != nil || !found {
		return err
	}
	l, err := Get(app)
	if err != nil {
		return err
	}
	if _, ok := raw["ct"]; ok {
		return nil
	}
	return Save(app, l)
}
