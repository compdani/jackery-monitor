package server

import (
	"encoding/json"
	"github.com/compdani/jackery-monitor/internal/forecast"
	"strings"
	"testing"
	"time"
)

func TestPredictionAndBackupRoutes(t *testing.T) {
	s, h := testServer(t)
	w := request(h, "POST", "/api/auth/setup", `{"username":"admin","password":"secure-pass"}`, "")
	var auth map[string]any
	if e := json.Unmarshal(w.Body.Bytes(), &auth); e != nil {
		t.Fatal(e)
	}
	token := str(auth, "token")
	if token == "" {
		t.Fatal(w.Body.String())
	}
	for _, path := range []string{"/api/forecast/accuracy", "/api/daily_summary", "/api/backup/snapshots"} {
		if w := request(h, "GET", path, "", ""); w.Code != 401 {
			t.Fatal(path, w.Code)
		}
	}
	now := time.Now().Unix()
	target := float64((now - 3600) / 3600 * 3600)
	if _, e := s.Store.RecordForecast("SN", target-3600, []forecast.Row{{"ts": target, "predicted_soc": 70.0}}); e != nil {
		t.Fatal(e)
	}
	for _, ts := range []float64{target - 60, target + 60} {
		if e := s.Store.Put("samples", strings.ReplaceAll(time.Unix(int64(ts), 0).Format(time.RFC3339), ":", ""), "SN", map[string]any{"ts": ts, "battery_percent": 65.0}); e != nil {
			t.Fatal(e)
		}
	}
	w = request(h, "GET", "/api/forecast/accuracy?device_sn=SN&since_ts=0", "", token)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var result map[string]any
	json.Unmarshal(w.Body.Bytes(), &result)
	rows, ok := result["samples"].([]any)
	if !ok || len(rows) != 1 || rows[0].(map[string]any)["error"] != 5.0 {
		t.Fatal(result)
	}
	w = request(h, "GET", "/api/daily_summary?device_sn=SN", "", token)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	w = request(h, "GET", "/api/backup/snapshots", "", token)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"snapshots":[]`) {
		t.Fatal(w.Code, w.Body.String())
	}
	for _, name := range []string{"../backup.zip", "/backup.zip", "missing.tar"} {
		data, _ := json.Marshal(map[string]any{"name": name})
		w = request(h, "POST", "/api/backup/restore", string(data), token)
		if w.Code != 400 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
}
