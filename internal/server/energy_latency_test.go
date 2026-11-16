package server

import (
	"encoding/json"
	"errors"
	"github.com/compdani/jackery-monitor/internal/store"
	"github.com/pocketbase/dbx"
	"net/http/httptest"
	"testing"
	"time"
)

func TestEnergyLatencyWithLargeHistory(t *testing.T) {
	s, _ := testServer(t)
	// ~69 days at one observation/minute, with realistic envelope JSON. Load
	// directly to keep fixture creation from dominating the measured read.
	now := float64(time.Now().Unix())
	_, err := s.Store.App.DB().NewQuery(`WITH RECURSIVE n(x) AS (VALUES(0) UNION ALL SELECT x+1 FROM n WHERE x<99999)
 INSERT INTO samples (id,key,device_sn,ts,payload)
 SELECT printf('%015d',x),printf('latency:%d',x),'latency',{:now}-x*60,
 json_object('ts',{:now}-x*60,'input_power_w',360,'output_power_w',120,'solar_input_w',300,'battery_percent',49)
 FROM n`).Bind(dbx.Params{"now": now}).Execute()
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/energy/totals?device_sn=latency", "/api/energy/daily?days=365&device_sn=latency"} {
		for i := 0; i < 2; i++ {
			start := time.Now()
			w := httptest.NewRecorder()
			s.energyRead(w, httptest.NewRequest("GET", path, nil))
			t.Logf("%s pass %d: %s (%d bytes)", path, i, time.Since(start), w.Body.Len())
			if w.Code == 202 {
				t.Logf("initial backfill reports preparation in %s", time.Since(start))
				buildStart := time.Now()
				for attempts := 0; attempts < 1000 && w.Code == 202; attempts++ {
					w = httptest.NewRecorder()
					s.energyRead(w, httptest.NewRequest("GET", path, nil))
				}
				t.Logf("background-equivalent daily chunks completed in %s", time.Since(buildStart))
			}
			if w.Code != 200 {
				t.Fatal(w.Code, w.Body.String())
			}
		}
	}
}

func TestEnergyCacheDeviceIsolationAndExpiry(t *testing.T) {
	s, _ := testServer(t)
	now := float64(time.Now().Unix())
	for sn, wh := range map[string]float64{"A": 100, "B": 200} {
		if err := s.Store.Put("samples", "cache:"+sn, sn, map[string]any{"ts": now, "bucket": now, "input_wh": wh}); err != nil {
			t.Fatal(err)
		}
	}
	read := func(sn string) float64 {
		t.Helper()
		w := httptest.NewRecorder()
		s.energyRead(w, httptest.NewRequest("GET", "/api/energy/totals?device_sn="+sn, nil))
		var v map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
			t.Fatal(err)
		}
		return v["lifetime"].(map[string]any)["input_wh"].(float64)
	}
	if read("A") != 100 || read("B") != 200 {
		t.Fatal("mixed devices")
	}
	if err := s.Store.Put("samples", "cache:A", "A", map[string]any{"ts": now, "bucket": now, "input_wh": 300.0}); err != nil {
		t.Fatal(err)
	}
	s.energyReads.invalidate()
	if read("A") != 300 || read("B") != 200 {
		t.Fatal("cache invalidation failed")
	}
}

func TestHistoricalTOUSavingsUsesTimestampTimezone(t *testing.T) {
	s, _ := testServer(t)
	if err := s.Store.SaveSecret(s.Key, "location", map[string]any{"timezone": "America/Chicago"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Store.Put("settings", "cost_plan", "", map[string]any{"type": "tou", "currency": "USD", "tou_rates": []any{map[string]any{"start_hour": 7, "end_hour": 8, "rate": 1.0}}}); err != nil {
		t.Fatal(err)
	}
	for _, stamp := range []string{"2026-01-15T13:30:00Z", "2026-07-15T12:30:00Z"} {
		ts, _ := time.Parse(time.RFC3339, stamp)
		if err := s.Store.Put("samples", stamp, "tou", map[string]any{"ts": float64(ts.Unix()), "bucket": float64(ts.Unix()), "output_wh": 1000.0}); err != nil {
			t.Fatal(err)
		}
	}
	now, _ := time.Parse(time.RFC3339, "2026-11-15T20:00:00Z")
	for i := 0; i < 1000; i++ {
		err := s.Store.EnsureEnergyRollups("tou")
		if err == nil {
			break
		}
		if !errors.Is(err, store.ErrEnergyBuilding) {
			t.Fatal(err)
		}
	}
	totals, err := s.energyTotals("tou", float64(now.Unix()), -21600)
	if err != nil {
		t.Fatal(err)
	}
	savings := totals["lifetime_savings"].(map[string]any)
	if savings["solar_savings"] != 2.0 {
		t.Fatal(totals)
	}
}

func TestStatusKeepsLastGoodEnergyAfterInvalidation(t *testing.T) {
	s, _ := testServer(t)
	w := httptest.NewRecorder()
	s.energyRead(w, httptest.NewRequest("GET", "/api/energy/totals?device_sn=keep", nil))
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	s.energyReads.invalidate()
	if s.statusEnergy("keep") == nil {
		t.Fatal("last good totals lost")
	}
}
