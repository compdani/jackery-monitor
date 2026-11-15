package server

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/coder/websocket"
	"github.com/compdani/jackery-monitor/internal/store"
	_ "github.com/compdani/jackery-monitor/migrations"
	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/apis"
	"github.com/pocketbase/pocketbase/core"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testServer(t *testing.T) (*Server, http.Handler) {
	t.Helper()
	app := pocketbase.NewWithConfig(pocketbase.Config{DefaultDataDir: t.TempDir()})
	if e := app.Bootstrap(); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = app.ResetBootstrapState() })
	if e := app.RunAllMigrations(); e != nil {
		t.Fatal(e)
	}
	dir := t.TempDir()
	if e := os.WriteFile(filepath.Join(dir, "index.html"), []byte("SPA shell"), 0600); e != nil {
		t.Fatal(e)
	}
	s := &Server{Store: &store.Store{App: app}, Key: bytes.Repeat([]byte{7}, 32), PublicDir: dir}
	router, e := apis.NewRouter(app)
	if e != nil {
		t.Fatal(e)
	}
	s.Register(&core.ServeEvent{App: app, Router: router})
	handler, e := router.BuildMux()
	if e != nil {
		t.Fatal(e)
	}
	return s, handler
}
func request(h http.Handler, method, path, data, token string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(data))
	r.Header.Set("Content-Type", "application/json")
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}
func TestAuthAndPersistence(t *testing.T) {
	_, h := testServer(t)
	if w := request(h, "GET", "/api/status", "", ""); w.Code != 401 || !strings.Contains(w.Body.String(), "setup_required") {
		t.Fatal(w.Code, w.Body.String())
	}
	w := request(h, "POST", "/api/auth/setup", `{"username":"admin","password":"secure-pass"}`, "")
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var result map[string]any
	if e := json.Unmarshal(w.Body.Bytes(), &result); e != nil {
		t.Fatal(e)
	}
	token := result["token"].(string)
	for _, c := range []struct {
		method, path, data string
		status             int
	}{
		{"POST", "/api/auth/setup", `{"username":"second","password":"secure-pass"}`, 403},
		{"POST", "/api/auth/login", `{"username":"admin","password":"wrong"}`, 401},
		{"GET", "/api/auth/me", "", 200},
		{"POST", "/api/settings", `{"poll_interval_s":15}`, 200},
		{"GET", "/api/settings", "", 200},
		{"GET", "/api/status", "", 200},
		{"GET", "/api/forecast", "", 200},
		{"GET", "/api/not-a-route", "", 501},
		{"POST", "/api/set_output", `{"port":"ac","on":true}`, 503},
		{"POST", "/api/auth/change_password", `{"current":"wrong","new":"another-pass"}`, 401},
		{"GET", "/energy", "", 200},
	} {
		w := request(h, c.method, c.path, c.data, token)
		if w.Code != c.status {
			t.Fatalf("%s %s: got %d: %s", c.method, c.path, w.Code, w.Body.String())
		}
	}
	cookie := httptest.NewRequest("GET", "/api/status", nil)
	cookie.AddCookie(w.Result().Cookies()[0])
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, cookie)
	if rec.Code != 200 {
		t.Fatal(rec.Code)
	}
}
func TestWebsocketAuth(t *testing.T) {
	_, h := testServer(t)
	w := request(h, "POST", "/api/auth/setup", `{"username":"admin","password":"secure-pass"}`, "")
	var v map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &v)
	srv := httptest.NewServer(h)
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, _, e := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http")+"/ws?token="+v["token"].(string), nil)
	if e != nil {
		t.Fatal(e)
	}
	defer c.CloseNow()
	_, data, e := c.Read(ctx)
	if e != nil {
		t.Fatal(e)
	}
	if !bytes.Contains(data, []byte(`"type":"snapshot"`)) {
		t.Fatal(string(data))
	}
	bad, resp, e := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http")+"/ws", nil)
	if bad != nil {
		bad.CloseNow()
	}
	if e == nil || resp.StatusCode != 401 {
		t.Fatal("unauthorized websocket accepted", e)
	}
}
