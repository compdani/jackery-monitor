// Package store owns PocketBase persistence; keys and query values are bound.
package store

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	"math"
	"reflect"
	"time"
)

type Store struct{ App core.App }

func ID() string {
	b := make([]byte, 12)
	_, err := rand.Read(b)
	if err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}
func (s *Store) Get(collection, key string) (map[string]any, error) {
	r, e := s.App.FindFirstRecordByData(collection, "key", key)
	if e != nil {
		return nil, e
	}
	return payload(r)
}
func payload(r *core.Record) (map[string]any, error) {
	var p map[string]any
	e := json.Unmarshal([]byte(r.GetString("payload")), &p)
	return p, e
}
func (s *Store) Put(collection, key, device string, p map[string]any) error {
	return s.App.RunInTransaction(func(app core.App) error {
		r, e := app.FindFirstRecordByData(collection, "key", key)
		if e != nil {
			if !errors.Is(e, sql.ErrNoRows) {
				return e
			}
			c, e := app.FindCollectionByNameOrId(collection)
			if e != nil {
				return e
			}
			r = core.NewRecord(c)
		}
		if collection == "samples" && e == nil {
			previous, err := payload(r)
			if err != nil {
				return err
			}
			// Normalize numeric Go representations before comparison.
			encoded, err := json.Marshal(p)
			if err != nil {
				return err
			}
			var normalized map[string]any
			if err := json.Unmarshal(encoded, &normalized); err != nil {
				return err
			}
			if reflect.DeepEqual(previous, normalized) {
				return nil
			}
		}
		ts := float64(time.Now().Unix())
		if v, ok := p["ts"].(float64); ok {
			ts = v
		}
		r.Set("key", key)
		r.Set("device_sn", device)
		r.Set("ts", ts)
		r.Set("payload", p)
		if err := app.Save(r); err != nil {
			return err
		}
		if collection == "samples" {
			// Track only the changed range; raw observations may integrate
			// back to a predecessor and portal records span half an hour.
			tx := &Store{App: app}
			key := "energy_dirty:" + device
			dirty, err := tx.Get("settings", key)
			if err != nil && !missing(err) {
				return err
			}
			since, until := ts, ts+60
			if p["_energy_kind"] == "portal_solar" {
				until = ts + 1800
			}
			if p["bucket"] == nil && p["_energy_kind"] != "portal_solar" && p["_energy_kind"] != "aggregate" {
				if prev, err := tx.rawEnergyNeighbor(device, ts, true); err != nil {
					return err
				} else if prev != nil {
					since = math.Min(since, num(prev["ts"]))
				}
				if next, err := tx.rawEnergyNeighbor(device, ts, false); err != nil {
					return err
				} else if next != nil {
					until = math.Max(until, num(next["ts"])+60)
				}
			}
			if dirty != nil {
				since = math.Min(since, num(dirty["since"]))
				until = math.Max(until, num(dirty["until"]))
			}
			return tx.Put("settings", key, device, map[string]any{"since": since, "until": until})
		}
		return nil
	})
}
func (s *Store) Delete(collection, key string) error {
	r, e := s.App.FindFirstRecordByData(collection, "key", key)
	if e != nil {
		return e
	}
	return s.App.Delete(r)
}
func (s *Store) List(collection, device string, since float64, limit int) ([]map[string]any, error) {
	return s.ListRange(collection, device, since, 0, limit)
}
func (s *Store) ListRange(collection, device string, since, until float64, limit int) ([]map[string]any, error) {
	c, err := s.App.FindCollectionByNameOrId(collection)
	if err != nil {
		return nil, err
	}
	out := []map[string]any{}
	var cursorTS float64
	cursorID := ""
	for {
		size := 1000
		if limit > 0 {
			size = min(size, limit-len(out))
			if size <= 0 {
				break
			}
		}
		// Select only the envelope fields we need, avoiding PocketBase Record
		// construction and repeated JSON decoding for historical reads.
		q := s.App.DB().Select("id", "ts", "payload").From(c.Name).
			Where(dbx.NewExp("ts >= {:since}", dbx.Params{"since": since})).
			OrderBy("ts", "id").Limit(int64(size))
		if until > 0 {
			q.AndWhere(dbx.NewExp("ts < {:until}", dbx.Params{"until": until}))
		}
		if device != "" {
			q.AndWhere(dbx.HashExp{"device_sn": device})
		}
		if cursorID != "" {
			q.AndWhere(dbx.NewExp("(ts, id) > ({:ts}, {:id})", dbx.Params{"ts": cursorTS, "id": cursorID}))
		}
		var rows []struct {
			ID      string  `db:"id"`
			TS      float64 `db:"ts"`
			Payload string  `db:"payload"`
		}
		if err := q.All(&rows); err != nil {
			return nil, err
		}
		for _, row := range rows {
			var p map[string]any
			if err := json.Unmarshal([]byte(row.Payload), &p); err != nil {
				return nil, err
			}
			out = append(out, p)
			cursorTS, cursorID = row.TS, row.ID
		}
		if len(rows) < size {
			break
		}
	}
	return out, nil
}
