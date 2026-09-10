package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	_ "jackery-monitor/pb_migrations"

	"jackery-monitor/internal/live"

	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/apis"
	"github.com/pocketbase/pocketbase/core"
)

func testApp(t *testing.T) (core.App, http.Handler) {
	t.Helper()
	app := pocketbase.NewWithConfig(pocketbase.Config{DefaultDataDir: filepath.Join(t.TempDir(), "pb_data")})
	public := t.TempDir()
	if err := os.WriteFile(filepath.Join(public, "index.html"), []byte("dashboard shell"), 0600); err != nil {
		t.Fatal(err)
	}
	Register(app, &public, live.Options{Mock: true, Disabled: true})
	if err := app.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { app.OnTerminate().Trigger(&core.TerminateEvent{App: app}); app.ResetBootstrapState() })
	if err := app.RunAllMigrations(); err != nil {
		t.Fatal(err)
	}
	router, err := apis.NewRouter(app)
	if err != nil {
		t.Fatal(err)
	}
	event := &core.ServeEvent{App: app, Router: router}
	if err := app.OnServe().Trigger(event); err != nil {
		t.Fatal(err)
	}
	handler, err := router.BuildMux()
	if err != nil {
		t.Fatal(err)
	}
	return app, handler
}
func request(h http.Handler, method, path, body, token string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res := httptest.NewRecorder()
	h.ServeHTTP(res, req)
	return res
}
func expect(t *testing.T, r *httptest.ResponseRecorder, code int) {
	t.Helper()
	if r.Code != code {
		t.Fatalf("expected %d, got %d: %s", code, r.Code, r.Body.String())
	}
}

const owner = `{"username":"owner","password":"test-password-123","passwordConfirm":"test-password-123"}`

func login(t *testing.T, h http.Handler, identity, password, collection string) string {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"identity": identity, "password": password})
	r := request(h, "POST", "/api/collections/"+collection+"/auth-with-password", string(body), "")
	expect(t, r, 200)
	var payload struct{ Token string }
	if err := json.Unmarshal(r.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Token == "" {
		t.Fatal("empty token")
	}
	return payload.Token
}
func TestSetupAuthenticationAndSettings(t *testing.T) {
	app, h := testApp(t)
	r := request(h, "GET", "/japi/settings", "", "")
	expect(t, r, 401)
	if !strings.Contains(r.Body.String(), "setup_required") {
		t.Fatal(r.Body.String())
	}
	// Bad setup rolls back the matching admin as well as the user.
	expect(t, request(h, "POST", "/api/collections/users/records", `{"username":"bad","password":"test-password-123","passwordConfirm":"wrong"}`, ""), 400)
	n, err := app.CountRecords("_superusers")
	if err != nil || n != 0 {
		t.Fatalf("stranded superuser: %d %v", n, err)
	}
	expect(t, request(h, "POST", "/api/collections/users/records", owner, ""), 200)
	expect(t, request(h, "POST", "/api/collections/users/records", owner, ""), 403)
	expect(t, request(h, "GET", "/japi/settings", "", ""), 401)
	token := login(t, h, "owner", "test-password-123", "users")
	admin := login(t, h, "owner@local.invalid", "test-password-123", "_superusers")
	expect(t, request(h, "GET", "/japi/settings", "", admin), 403)
	expect(t, request(h, "GET", "/api/settings", "", token), 403)
	expect(t, request(h, "GET", "/api/settings", "", admin), 200)
	expect(t, request(h, "GET", "/japi/settings", "", token), 200)
	r = request(h, "POST", "/japi/settings", `{"poll_interval_s":0,"backup_keep_count":99999,"unknown":7}`, token)
	expect(t, r, 200)
	if !strings.Contains(r.Body.String(), `"poll_interval_s":1`) || !strings.Contains(r.Body.String(), `"backup_keep_count":3650`) {
		t.Fatal(r.Body.String())
	}
	saved, err := app.FindFirstRecordByData("app_settings", "key", "tunables")
	if err != nil {
		t.Fatal(err)
	}
	var overrides map[string]int
	if err := saved.UnmarshalJSONField("data", &overrides); err != nil {
		t.Fatal(err)
	}
	if len(overrides) != 2 {
		t.Fatalf("unexpected persisted overrides: %v", overrides)
	}
	expect(t, request(h, "POST", "/japi/settings", `null`, token), 400)
	expect(t, request(h, "GET", "/japi/missing", "", token), 404)
	expect(t, request(h, "GET", "/energy", "", ""), 200)
	user, err := app.FindFirstRecordByData("users", "username", "owner")
	if err != nil {
		t.Fatal(err)
	}
	path := "/api/collections/users/records/" + user.Id
	expect(t, request(h, "PATCH", path, `{"oldPassword":"incorrect","password":"new-password-123","passwordConfirm":"new-password-123"}`, token), 400)
	expect(t, request(h, "PATCH", path, `{"oldPassword":"test-password-123","password":"new-password-123","passwordConfirm":"new-password-123"}`, token), 200)
	expect(t, request(h, "GET", "/japi/settings", "", token), 401)
	login(t, h, "owner", "new-password-123", "users")
}
func TestConcurrentSignup(t *testing.T) {
	app, h := testApp(t)
	var wg sync.WaitGroup
	codes := make(chan int, 2)
	for _, name := range []string{"alice", "bob"} {
		wg.Add(1)
		go func(name string) {
			defer wg.Done()
			codes <- request(h, "POST", "/api/collections/users/records", strings.Replace(owner, "owner", name, 1), "").Code
		}(name)
	}
	wg.Wait()
	close(codes)
	success := 0
	for code := range codes {
		if code == 200 {
			success++
		} else if code != 403 {
			t.Fatalf("unexpected status %d", code)
		}
	}
	if success != 1 {
		t.Fatalf("created %d users", success)
	}
	for _, collection := range []string{"users", "_superusers"} {
		n, err := app.CountRecords(collection)
		if err != nil || n != 1 {
			t.Fatalf("%s count=%d: %v", collection, n, err)
		}
	}
}
