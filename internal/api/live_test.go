package api

import (
	"context"
	"encoding/json"
	"jackery-monitor/internal/energy"
	"jackery-monitor/internal/live"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

func TestLiveAPIControlsAndAuth(t *testing.T) {
	_, h := testApp(t)
	expect(t, request(h, "POST", "/api/collections/users/records", owner, ""), 200)
	token := login(t, h, "owner", "test-password-123", "users")
	expect(t, request(h, "GET", "/japi/status", "", ""), 401)
	expect(t, request(h, "GET", "/ws?token=invalid", "", ""), 401)
	admin := login(t, h, "owner@local.invalid", "test-password-123", "_superusers")
	expect(t, request(h, "GET", "/ws?token="+admin, "", ""), 401)
	expect(t, request(h, "POST", "/japi/set_output", `{"device_sn":"MOCK-3000","port":"ac","on":false}`, token), 200)
	for _, body := range []string{`{"port":"ac"}`, `{"port":"ac","on":"false"}`, `{"port":"ac","on":true,"device_sn":"NOPE"}`, `{"port":"NOPE","on":true}`} {
		expect(t, request(h, "POST", "/japi/set_output", body, token), 400)
	}
	r := request(h, "GET", "/japi/status?view_device_id=mock-3000", "", token)
	expect(t, r, 200)
	var snapshot struct {
		Telemetry struct {
			AC bool `json:"ac_on"`
		}
		Mock bool `json:"mock_mode"`
	}
	if json.Unmarshal(r.Body.Bytes(), &snapshot) != nil || snapshot.Telemetry.AC || !snapshot.Mock {
		t.Fatal("wrong mock snapshot")
	}
	expect(t, request(h, "POST", "/japi/pause_polling", `{"seconds":600}`, token), 200)
	expect(t, request(h, "POST", "/japi/set_output", `{"port":"ac","on":true}`, token), 400)
	resumed := request(h, "POST", "/japi/resume_polling", `{}`, token)
	expect(t, resumed, 200)
	if !strings.Contains(resumed.Body.String(), `"was_paused":true`) {
		t.Fatal("missing resume compatibility field")
	}
}
func TestWebSocketSnapshotsSelectionAndRevocation(t *testing.T) {
	app, h := testApp(t)
	expect(t, request(h, "POST", "/api/collections/users/records", owner, ""), 200)
	token := login(t, h, "owner", "test-password-123", "users")
	server := httptest.NewServer(h)
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	dial := func(view string) *websocket.Conn {
		t.Helper()
		c, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http")+"/ws?token="+token+"&view_device_id="+view, nil)
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	first := dial("mock-5000")
	defer first.CloseNow()
	second := dial("mock-3000")
	defer second.CloseNow()
	type message struct {
		Type string `json:"type"`
		Data struct {
			Device struct {
				SN string `json:"device_sn"`
			}
			Telemetry struct {
				AC bool `json:"ac_on"`
			}
		}
	}
	for _, pair := range []struct {
		c  *websocket.Conn
		sn string
	}{{first, "MOCK-5000"}, {second, "MOCK-3000"}} {
		var m message
		if err := wsjson.Read(ctx, pair.c, &m); err != nil {
			t.Fatal(err)
		}
		if m.Type != "snapshot" || m.Data.Device.SN != pair.sn {
			t.Fatalf("wrong first frame: %+v", m)
		}
	}
	expect(t, request(h, "POST", "/japi/set_output", `{"device_sn":"MOCK-3000","port":"ac","on":false}`, token), 200)
	var m message
	if err := wsjson.Read(ctx, second, &m); err != nil || m.Data.Telemetry.AC {
		t.Fatalf("selected device update failed: %+v %v", m, err)
	}
	if err := wsjson.Read(ctx, first, &m); err != nil || !m.Data.Telemetry.AC {
		t.Fatalf("update crossed devices: %+v %v", m, err)
	}
	// Password updates revoke already-open sockets, not only future requests.
	user, err := app.FindFirstRecordByData("users", "username", "owner")
	if err != nil {
		t.Fatal(err)
	}
	expect(t, request(h, http.MethodPatch, "/api/collections/users/records/"+user.Id, `{"oldPassword":"test-password-123","password":"new-password-123","passwordConfirm":"new-password-123"}`, token), 200)
	for {
		_, _, err = second.Read(ctx)
		if err != nil {
			break
		}
	}
	if websocket.CloseStatus(err) != websocket.StatusPolicyViolation {
		t.Fatalf("socket was not revoked: %v", err)
	}
}

func TestEnergyAPIsAndPackPersistence(t *testing.T) {
	app, h := testApp(t)
	expect(t, request(h, "POST", "/api/collections/users/records", owner, ""), 200)
	token := login(t, h, "owner", "test-password-123", "users")
	store := energy.New(app)
	service := live.New(t.TempDir(), live.Options{Mock: true}, func(string) int { return 2 })
	if err := recordLive(store, service); err != nil {
		t.Fatal(err)
	}
	r := request(h, "GET", "/japi/devices/capacity", "", token)
	expect(t, r, 200)
	if !strings.Contains(r.Body.String(), `"effective_capacity_wh":15120`) {
		t.Fatalf("packs not counted: %s", r.Body.String())
	}
	r = request(h, "GET", "/japi/devices/battery_packs?device_sn=MOCK-5000", "", token)
	expect(t, r, 200)
	if !strings.Contains(r.Body.String(), "MOCK-PACK-2") {
		t.Fatal("pack snapshot not persisted")
	}
	expect(t, request(h, "POST", "/japi/devices/capacity", `{"device_sn":"MOCK-5000","capacity_wh":20000}`, token), 200)
	r = request(h, "GET", "/japi/status?view_device_id=mock-5000", "", token)
	expect(t, r, 200)
	if !strings.Contains(r.Body.String(), `"capacity_wh":20000`) {
		t.Fatal("live snapshot ignored capacity override")
	}
	for _, path := range []string{"/japi/energy/totals", "/japi/energy/history?hours=24", "/japi/energy/daily?days=30", "/japi/energy/devices", "/japi/migration/status"} {
		expect(t, request(h, "GET", path, "", token), 200)
		expect(t, request(h, "GET", path, "", ""), 401)
	}
	expect(t, request(h, "GET", "/japi/energy/history?hours=oops", "", token), 400)
	expect(t, request(h, "POST", "/japi/devices/capacity", `{"device_sn":"MOCK-5000","capacity_wh":400}`, token), 400)
}
