package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	_ "github.com/compdani/jackery-monitor/migrations"
	"github.com/pocketbase/pocketbase"
	"os"
	"path/filepath"
	"testing"
)

func TestCrypto(t *testing.T) {
	path := filepath.Join(t.TempDir(), "key")
	key, e := LoadKey(path)
	if e != nil {
		t.Fatal(e)
	}
	again, e := LoadKey(path)
	if e != nil || !bytes.Equal(key, again) {
		t.Fatal("key changed", e)
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0600 {
		t.Fatal(info.Mode())
	}
	for _, plain := range []string{"", "secret", "🔋 unicode \"quote\"\n"} {
		v, e := Encrypt(key, []byte(plain))
		if e != nil {
			t.Fatal(e)
		}
		out, e := Decrypt(key, v)
		if e != nil || string(out) != plain {
			t.Fatal(string(out), e)
		}
		v.Tag = "AAAAAAAAAAAAAAAAAAAAAA=="
		if _, e = Decrypt(key, v); e == nil {
			t.Fatal("tampered tag accepted")
		}
	}
	if e = os.WriteFile(path, []byte("bad"), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e = LoadKey(path); e == nil {
		t.Fatal("invalid key replaced")
	}
}
func TestPersistence(t *testing.T) {
	app := pocketbase.NewWithConfig(pocketbase.Config{DefaultDataDir: t.TempDir()})
	if e := app.Bootstrap(); e != nil {
		t.Fatal(e)
	}
	defer app.ResetBootstrapState()
	if e := app.RunAllMigrations(); e != nil {
		t.Fatal(e)
	}
	s := &Store{App: app}
	for _, key := range []string{"sn' OR 1=1 --", "normal"} {
		if e := s.Put("settings", key, "d", map[string]any{"value": 42}); e != nil {
			t.Fatal(e)
		}
	}
	if e := s.Put("settings", "normal", "d", map[string]any{"value": 43}); e != nil {
		t.Fatal(e)
	}
	rows, e := s.List("settings", "d", 0, 20)
	if e != nil || len(rows) != 2 {
		t.Fatal(rows, e)
	}
	v, e := s.Get("settings", "normal")
	if e != nil || v["value"] != float64(43) {
		t.Fatal(v, e)
	}
	key := bytes.Repeat([]byte{1}, 32)
	if e := s.SaveSecret(key, "test", map[string]any{"password": "never plaintext"}); e != nil {
		t.Fatal(e)
	}
	stored, _ := s.Get("credentials", "test")
	raw, _ := json.Marshal(stored)
	if bytes.Contains(raw, []byte("never plaintext")) {
		t.Fatal("secret leaked")
	}
	decoded, e := s.Secret(key, "test")
	if e != nil || decoded["password"] != "never plaintext" {
		t.Fatal(decoded, e)
	}
	if e := s.Delete("settings", "normal"); e != nil {
		t.Fatal(e)
	}
}

func TestLegacyImportReadOnlyIdempotent(t *testing.T) {
	source := filepath.Join(t.TempDir(), "legacy.db")
	db, e := sql.Open("sqlite", source)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = db.Exec("CREATE TABLE samples (device_sn TEXT, bucket INTEGER, input_wh REAL)"); e != nil {
		t.Fatal(e)
	}
	if _, e = db.Exec("INSERT INTO samples VALUES ('test',1700000000,123.5)"); e != nil {
		t.Fatal(e)
	}
	db.Close()
	before, e := os.ReadFile(source)
	if e != nil {
		t.Fatal(e)
	}
	app := pocketbase.NewWithConfig(pocketbase.Config{DefaultDataDir: t.TempDir()})
	if e = app.Bootstrap(); e != nil {
		t.Fatal(e)
	}
	defer app.ResetBootstrapState()
	if e = app.RunAllMigrations(); e != nil {
		t.Fatal(e)
	}
	s := &Store{App: app}
	for i := 0; i < 2; i++ {
		counts, e := s.ImportLegacy(context.Background(), source)
		if e != nil || counts["samples"] != 1 {
			t.Fatal(counts, e)
		}
	}
	rows, e := s.List("samples", "test", 0, 20)
	if e != nil || len(rows) != 1 || rows[0]["input_wh"] != 123.5 {
		t.Fatal(rows, e)
	}
	after, e := os.ReadFile(source)
	if e != nil || !bytes.Equal(before, after) {
		t.Fatal("source database changed", e)
	}
}
