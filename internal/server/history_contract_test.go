package server

import (
	"fmt"
	"github.com/compdani/jackery-monitor/internal/store"
	"testing"
	"time"
)

func TestImportedAggregateHistoryAndCorrectionsHTTP(t *testing.T) {
	s, h := testServer(t)
	token := contractToken(t, h)
	now := float64(time.Now().Unix() / 60 * 60)
	if err := s.Store.Put("devices", "SN", "SN", map[string]any{"device": map[string]any{"device_sn": "SN", "name": "Rig", "model_code": 22.0}, "capacity_wh_override": 9000.0}); err != nil {
		t.Fatal(err)
	}
	for _, at := range []float64{now - 120, now - 60} {
		if err := s.Store.Put("samples", store.SampleKey("SN", at), "SN", map[string]any{"ts": at, "bucket": at, "input_wh": 20.0, "output_wh": 10.0, "solar_wh": 5.0, "ac_input_wh": 15.0, "last_output_w": 99999.0, "last_battery_pct": 40.0}); err != nil {
			t.Fatal(err)
		}
	}
	v := contractJSON(t, h, "GET", "/api/energy/totals?device_sn=SN", "", token, 200)
	if v["lifetime"].(map[string]any)["output_wh"] != 20.0 || v["today"] == nil || v["last_7d"] == nil {
		t.Fatal(v)
	}
	v = contractJSON(t, h, "GET", "/api/energy/history?device_sn=SN", "", token, 200)
	sum := 0.0
	for _, raw := range v["history"].([]any) {
		row := raw.(map[string]any)
		sum += row["output_wh"].(float64)
		if row["battery_pct"] != 40.0 {
			t.Fatal(row)
		}
	}
	if sum != 20 {
		t.Fatal(v)
	}
	v = contractJSON(t, h, "GET", "/api/energy/daily?device_sn=SN", "", token, 200)
	if v["daily"].([]any)[0].(map[string]any)["consumed_kwh"] != .02 {
		t.Fatal(v)
	}
	body := fmt.Sprintf(`{"device_sn":"SN","start_ts":%.0f,"end_ts":%.0f,"output_w":600}`, now-120, now)
	v = contractJSON(t, h, "POST", "/api/energy/correct_output", body, token, 200)
	if v["buckets"] != 2.0 {
		t.Fatal(v)
	}
	v = contractJSON(t, h, "GET", "/api/energy/totals?device_sn=SN", "", token, 200)
	if v["lifetime"].(map[string]any)["output_wh"] != 20.0 || v["lifetime"].(map[string]any)["input_wh"] != 40.0 {
		t.Fatal(v)
	}
	contractJSON(t, h, "POST", "/api/energy/correct_output", `{"device_sn":"SN","start_ts":1,"end_ts":100000,"output_w":2}`, token, 400)
	v = contractJSON(t, h, "GET", "/api/devices/capacity", "", token, 200)
	if v["devices"].([]any)[0].(map[string]any)["effective_capacity_wh"] != 9000.0 {
		t.Fatal(v)
	}
	contractJSON(t, h, "POST", "/api/devices/capacity", `{"device_sn":"SN","capacity_wh":12000}`, token, 200)
	contractJSON(t, h, "POST", "/api/devices/capacity", `{"device_sn":"SN","capacity_wh":100}`, token, 400)
	contractJSON(t, h, "POST", "/api/devices/capacity", `{"device_sn":"SN","capacity_wh":null}`, token, 200)
}
