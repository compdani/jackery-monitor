package site

import (
	"os"
	"path/filepath"
	"testing"

	"jackery-monitor/internal/secrets"

	_ "jackery-monitor/pb_migrations"

	"github.com/pocketbase/pocketbase"
)

func TestLegacyLocationUpgradeAndMissingKey(t *testing.T) {
	root := t.TempDir()
	app := pocketbase.NewWithConfig(pocketbase.Config{DefaultDataDir: filepath.Join(root, "pb_data")})
	if err := app.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	defer app.ResetBootstrapState()
	if err := app.RunAllMigrations(); err != nil {
		t.Fatal(err)
	}
	if err := Write(app, "location", map[string]any{"latitude": 35.0, "longitude": -120.0, "utc_offset_seconds": -25200}); err != nil {
		t.Fatal(err)
	}
	if err := Upgrade(app); err != nil {
		t.Fatal(err)
	}
	l, err := Get(app)
	if err != nil || *l.Latitude != 35 || *l.Offset != -25200 {
		t.Fatal(l, err)
	}
	if err = os.Remove(secrets.KeyFile(root)); err != nil {
		t.Fatal(err)
	}
	if _, err = Get(app); err == nil {
		t.Fatal("missing encryption key silently accepted")
	}
	if err = Upgrade(app); err == nil {
		t.Fatal("upgrade must not replace missing encryption key")
	}
	if _, err = os.Stat(secrets.KeyFile(root)); !os.IsNotExist(err) {
		t.Fatal("replacement key created")
	}
}
