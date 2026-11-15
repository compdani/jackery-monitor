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
		ts := float64(time.Now().Unix())
		if v, ok := p["ts"].(float64); ok {
			ts = v
		}
		r.Set("key", key)
		r.Set("device_sn", device)
		r.Set("ts", ts)
		r.Set("payload", p)
		return app.Save(r)
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
	filter := "ts >= {:since}"
	params := dbx.Params{"since": since, "device": device}
	if device != "" {
		filter += " && device_sn = {:device}"
	}
	if limit <= 0 || limit > 100000 {
		limit = 100000
	}
	rows, e := s.App.FindRecordsByFilter(collection, filter, "ts", limit, 0, params)
	if e != nil {
		return nil, e
	}
	out := make([]map[string]any, 0, len(rows))
	for _, r := range rows {
		p, e := payload(r)
		if e != nil {
			return nil, e
		}
		out = append(out, p)
	}
	return out, nil
}
