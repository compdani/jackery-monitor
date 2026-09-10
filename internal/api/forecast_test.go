package api

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"jackery-monitor/internal/energy"

	"jackery-monitor/internal/site"

	"github.com/pocketbase/dbx"
)

func TestForecastLocationAndCostRoutes(t *testing.T) {
	app, h := testApp(t)
	for _, path := range []string{"/japi/forecast", "/japi/location", "/japi/location/geocode", "/japi/cost/plan", "/japi/cost/savings", "/japi/forecast/accuracy", "/japi/daily_summary"} {
		expect(t, request(h, "GET", path, "", ""), 401)
	}
	expect(t, request(h, "POST", "/api/collections/users/records", owner, ""), 200)
	token := login(t, h, "owner", "test-password-123", "users")
	r := request(h, "GET", "/japi/forecast", "", token)
	expect(t, r, 200)
	if !strings.Contains(r.Body.String(), `"configured":false`) {
		t.Fatal(r.Body.String())
	}
	for _, body := range []string{`{}`, `{"latitude":0,"longitude":0}`, `{"latitude":91,"longitude":20}`, `{"latitude":20,"longitude":30,"timezone":"bogus"}`} {
		expect(t, request(h, "POST", "/japi/location", body, token), 400)
	}
	expect(t, request(h, "POST", "/japi/location", `{"latitude":35,"longitude":-120,"label":"Test location","timezone":"America/Los_Angeles"}`, token), 200)
	rec, err := app.FindFirstRecordByData("location", "key", "default")
	if err != nil {
		t.Fatal(err)
	}
	raw := rec.GetString("data")
	if strings.Contains(raw, "latitude") || !strings.Contains(raw, "ct") {
		t.Fatal("coordinates stored in plaintext")
	}
	l, err := site.Get(app)
	if err != nil || *l.Latitude != 35 {
		t.Fatal(l, err)
	}
	if localTime(app).Location().String() != "America/Los_Angeles" {
		t.Fatal("energy ignored encrypted timezone")
	}
	// Unknown device must not borrow telemetry from the first live device or call weather.
	r = request(h, "GET", "/japi/forecast?device_sn=missing", "", token)
	expect(t, r, 200)
	if !strings.Contains(r.Body.String(), "No current telemetry") {
		t.Fatal(r.Body.String())
	}
	for _, body := range []string{`{"type":"flat","rate_per_kwh":6}`, `{"type":"tou","tou_rates":[]}`, `{"type":"tou","tou_rates":[{"start_hour":1,"end_hour":2}]}`, `{"type":"tou","tou_rates":[{"start_hour":1,"end_hour":2,"rate":0.1,"months":[13]}]}`} {
		expect(t, request(h, "POST", "/japi/cost/plan", body, token), 400)
	}
	expect(t, request(h, "POST", "/japi/cost/plan", `{"type":"flat","rate_per_kwh":0.4,"currency":"USD"}`, token), 200)
	_, err = app.DB().NewQuery(`INSERT INTO samples(device_sn,bucket,output_wh,ac_input_wh) VALUES('S1',{:ts},2000,500)`).Bind(dbx.Params{"ts": time.Now().Unix() / 60 * 60}).Execute()
	if err != nil {
		t.Fatal(err)
	}
	r = request(h, "GET", "/japi/cost/savings?device_sn=S1", "", token)
	expect(t, r, 200)
	var got map[string]any
	if err = json.Unmarshal(r.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	today := got["today_savings"].(map[string]any)
	if energy.Number(today["net_savings"]) != .6 {
		t.Fatal(got)
	}
	expect(t, request(h, "GET", "/japi/daily_summary?days=no", "", token), 400)
}
