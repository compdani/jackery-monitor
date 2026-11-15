package main

import (
	"bytes"
	"github.com/compdani/jackery-monitor/internal/bridge"
	"github.com/compdani/jackery-monitor/internal/store"
	"github.com/pocketbase/pocketbase"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestDiagnosticCommandWithFakeAccountTransport(t *testing.T) {
	dir := t.TempDir()
	key := bytes.Repeat([]byte{7}, 32)
	keyPath := filepath.Join(t.TempDir(), "existing-key")
	os.WriteFile(keyPath, key, 0600)
	t.Setenv("JACKERY_AT_REST_KEY_FILE", keyPath)
	t.Setenv("JACKERY_MQTT_CA_FILE", "")
	app := pocketbase.NewWithConfig(pocketbase.Config{DefaultDataDir: dir})
	if err := app.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	if err := app.RunAllMigrations(); err != nil {
		t.Fatal(err)
	}
	if err := (&store.Store{App: app}).SaveSecret(key, "jackery", map[string]any{"email": "synthetic@example.invalid", "password": "synthetic-secret"}); err != nil {
		t.Fatal(err)
	}
	app.ResetBootstrapState()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/auth/login":
			w.Write([]byte(`{"code":0,"data":{"token":"synthetic-token","userId":123,"mqttPassword":"unused"}}`))
		case "/v1/device/bind/list":
			w.Write([]byte(`{"code":0,"data":[{"devId":1,"devSn":"SYNTHETIC"}]}`))
		default:
			t.Error("unexpected diagnostic operation", r.URL.Path)
		}
	}))
	defer server.Close()
	app = pocketbase.NewWithConfig(pocketbase.Config{DefaultDataDir: dir})
	cmd := diagnosticCommand(app, func(creds map[string]any) *bridge.Cloud {
		return &bridge.Cloud{Email: fmtString(creds["email"]), Password: fmtString(creds["password"]), BaseURL: server.URL}
	})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--devices"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"synthetic-secret", "synthetic-token"} {
		if bytes.Contains(out.Bytes(), []byte(secret)) {
			t.Fatal("diagnostic leaked secret")
		}
	}
	if !bytes.Contains(out.Bytes(), []byte("SYNTHETIC")) {
		t.Fatal(out.String())
	}
}
