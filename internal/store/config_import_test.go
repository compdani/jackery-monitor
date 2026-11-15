package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"github.com/pocketbase/pocketbase"
	"golang.org/x/crypto/pbkdf2"
	"os"
	"path/filepath"
	"testing"
)

func fixtureHash() string {
	salt := []byte("synthetic-salt-16")
	hash := pbkdf2.Key([]byte("old-password"), salt, 1000, 32, sha256.New)
	return "pbkdf2_sha256:1000:" + base64.StdEncoding.EncodeToString(salt) + ":" + base64.StdEncoding.EncodeToString(hash)
}
func importTestStore(t *testing.T) *Store {
	t.Helper()
	app := pocketbase.NewWithConfig(pocketbase.Config{DefaultDataDir: t.TempDir()})
	if err := app.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { app.ResetBootstrapState() })
	if err := app.RunAllMigrations(); err != nil {
		t.Fatal(err)
	}
	return &Store{App: app}
}
func TestConfigImportDryRunIdempotenceAndSecrets(t *testing.T) {
	s := importTestStore(t)
	dir := t.TempDir()
	key := bytes.Repeat([]byte{9}, 32)
	keyPath := filepath.Join(dir, "key")
	if err := os.WriteFile(keyPath, key, 0600); err != nil {
		t.Fatal(err)
	}
	original := map[string][]byte{}
	for _, src := range ConfigSources {
		v := map[string]any{"synthetic": true}
		switch src.Shape {
		case "prefs":
			v = map[string]any{"SN-A": map[string]any{"alias": "House"}}
		case "rules":
			v = map[string]any{"rules": []any{map[string]any{"id": "r1", "enabled": false, "device_sn": "SN-A"}}}
		case "schedule":
			v = map[string]any{"SN-A": map[string]any{"enabled": true, "windows": []any{}}}
		case "ai_prefs":
			v = map[string]any{"provider": "openai", "openai_compat_preset": "custom"}
		case "user":
			v = map[string]any{"username": "legacy", "password_hash": fixtureHash()}
		}
		data, _ := json.Marshal(v)
		if src.Secret {
			env, err := Encrypt(key, data)
			if err != nil {
				t.Fatal(err)
			}
			data, _ = json.Marshal(env)
		}
		path := filepath.Join(dir, src.File)
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
		original[path] = data
	}
	opt := ConfigImportOptions{Directory: dir, KeyPath: keyPath, DryRun: true}
	report, err := s.ImportConfig(context.Background(), opt)
	if err != nil {
		t.Fatal(err, report)
	}
	for _, r := range report {
		if r.Candidates < 1 || r.Imported != 0 || len(r.Errors) > 0 {
			t.Fatal(r)
		}
	}
	rows, _ := s.List("settings", "", 0, 1000)
	if len(rows) != 0 {
		t.Fatal("dry run wrote settings")
	}
	opt.DryRun = false
	report, err = s.ImportConfig(context.Background(), opt)
	if err != nil {
		t.Fatal(err, report)
	}
	for _, r := range report {
		if r.Imported != r.Candidates {
			t.Fatal(r)
		}
	}
	prefs, _ := s.Get("device_prefs", "SN-A")
	if prefs["alias"] != "House" {
		t.Fatal(prefs)
	}
	if _, err := s.Get("settings", "load_schedule:SN-A"); err != nil {
		t.Fatal(err)
	}
	provider, _ := s.Get("settings", "ai_provider")
	if provider["provider"] != "openai" {
		t.Fatal(provider)
	}
	if _, err := s.Secret(key, "jackery"); err != nil {
		t.Fatal(err)
	}
	u, err := s.App.FindFirstRecordByData("users", "username", "legacy")
	if err != nil {
		t.Fatal(err)
	}
	if u.ValidatePassword("old-password") {
		t.Fatal("PB password must not contain a PBKDF2 hash or known temporary password")
	}
	legacy, err := s.Secret(key, "legacy_user:"+u.Id)
	if err != nil || !VerifyLegacyPassword("old-password", legacy["password_hash"].(string)) {
		t.Fatal("hash not preserved", err)
	}
	// Native changes win over reruns, and source mutations do not create duplicates.
	if err := s.Put("device_prefs", "SN-A", "SN-A", map[string]any{"alias": "Native"}); err != nil {
		t.Fatal(err)
	}
	report, err = s.ImportConfig(context.Background(), opt)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range report {
		if r.Imported != 0 || r.Skipped != r.Candidates {
			t.Fatal(r)
		}
	}
	for path, data := range original {
		got, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(got, data) {
			t.Fatal("source modified", path)
		}
	}
	got, _ := os.ReadFile(keyPath)
	if !bytes.Equal(got, key) {
		t.Fatal("key modified")
	}
}
func TestConfigImportRejectsMissingBadKeyAndBadSources(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "missing")
	var s *Store
	if _, err := s.ImportConfig(context.Background(), ConfigImportOptions{Directory: dir, KeyPath: path, DryRun: true}); err == nil {
		t.Fatal("missing key accepted")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("key created")
	}
	os.WriteFile(path, []byte("bad"), 0600)
	if _, err := ReadExistingKey(path); err == nil {
		t.Fatal("bad key accepted")
	}
	got, _ := os.ReadFile(path)
	if string(got) != "bad" {
		t.Fatal("bad key replaced")
	}
	os.WriteFile(path, bytes.Repeat([]byte{1}, 32), 0600)
	os.WriteFile(filepath.Join(dir, "device_prefs.json"), []byte(`{"valid":{"alias":"ok"},"invalid":42}`), 0600)
	os.WriteFile(filepath.Join(dir, "jackery-creds.json"), []byte(`{"ct":"bad","v":"v1"}`), 0600)
	report, err := s.ImportConfig(context.Background(), ConfigImportOptions{Directory: dir, KeyPath: path, DryRun: true})
	if err == nil {
		t.Fatal("invalid sources accepted")
	}
	failures := 0
	for _, r := range report {
		if len(r.Errors) > 0 {
			failures++
		}
	}
	if failures != 2 {
		t.Fatal(report)
	}
}
func TestConfigImportIsAtomicPerSource(t *testing.T) {
	s := importTestStore(t)
	dir := t.TempDir()
	key := filepath.Join(dir, "key")
	os.WriteFile(key, bytes.Repeat([]byte{2}, 32), 0600)
	os.WriteFile(filepath.Join(dir, "device_prefs.json"), []byte(`{"valid":{"alias":"ok"},"invalid":42}`), 0600)
	if _, err := s.ImportConfig(context.Background(), ConfigImportOptions{Directory: dir, KeyPath: key}); err == nil {
		t.Fatal("invalid source accepted")
	}
	rows, err := s.List("device_prefs", "", 0, 100)
	if err != nil || len(rows) != 0 {
		t.Fatal("partial source imported", rows, err)
	}
}
