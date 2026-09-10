package migrate_test

import (
	"crypto/sha256"
	"database/sql"
	"jackery-monitor/internal/energy"
	"os"
	"path/filepath"
	"testing"

	"jackery-monitor/internal/migrate"

	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/core"
)

func target(t *testing.T) core.App {
	t.Helper()
	app := pocketbase.NewWithConfig(pocketbase.Config{DefaultDataDir: t.TempDir()})
	if err := app.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { app.ResetBootstrapState() })
	if err := app.RunAllMigrations(); err != nil {
		t.Fatal(err)
	}
	return app
}
func source(t *testing.T, extra string) (string, *sql.DB) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "energy.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	_, err = db.Exec(`PRAGMA journal_mode=WAL; PRAGMA wal_autocheckpoint=0;
 CREATE TABLE devices(device_sn TEXT PRIMARY KEY,name TEXT,model_code INTEGER,model_name TEXT,first_seen INTEGER,last_seen INTEGER);
 CREATE TABLE samples(device_sn TEXT,bucket INTEGER,input_wh REAL,output_wh REAL,last_input_w INTEGER,last_output_w INTEGER,last_battery_pct INTEGER,sample_count INTEGER,PRIMARY KEY(device_sn,bucket));
 INSERT INTO devices VALUES('LEGACY','Existing device',13,'Explorer',1000,2000);
 INSERT INTO samples VALUES('LEGACY',1200,12.5,8,100,50,80,1),('LEGACY',1260,7.5,2,100,50,80,1);` + extra)
	if err != nil {
		t.Fatal(err)
	}
	return path, db
}
func TestLegacyImportReadsWALAndIsIdempotent(t *testing.T) {
	app := target(t)
	path, db := source(t, "")
	original, _ := os.ReadFile(path)
	hash := sha256.Sum256(original)
	if err := migrate.ImportEnergy(app, path); err != nil {
		t.Fatal(err)
	}
	rows, err := energy.Query(app, `SELECT SUM(input_wh) total,SUM(solar_wh) solar,COUNT(*) count FROM samples`, nil)
	if err != nil {
		t.Fatal(err)
	}
	if energy.Number(rows[0]["total"]) != 20 || energy.Number(rows[0]["count"]) != 2 || energy.Number(rows[0]["solar"]) != 0 {
		t.Fatalf("wrong imported totals: %v", rows)
	}
	if err = migrate.ImportEnergy(app, path); err != nil {
		t.Fatal(err)
	}
	rows, _ = energy.Query(app, `SELECT COUNT(*) count FROM samples`, nil)
	if energy.Number(rows[0]["count"]) != 2 {
		t.Fatal("import duplicated rows")
	}
	raw, _ := os.ReadFile(path)
	if sha256.Sum256(raw) != hash {
		t.Fatal("original DB file was modified")
	}
	var cols int
	if err = db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('samples')`).Scan(&cols); err != nil || cols != 8 {
		t.Fatalf("original schema changed: %d %v", cols, err)
	}
	marker, _ := energy.Query(app, `SELECT * FROM legacy_imports`, nil)
	if len(marker) != 1 {
		t.Fatal("missing verified import marker")
	}
}
func TestImportRollsBackUnsupportedColumns(t *testing.T) {
	app := target(t)
	path, _ := source(t, `ALTER TABLE samples ADD COLUMN future_field TEXT;`)
	if err := migrate.ImportEnergy(app, path); err == nil {
		t.Fatal("silently dropped unknown source column")
	}
	for _, table := range []string{"samples", "energy_devices", "legacy_imports"} {
		rows, _ := energy.Query(app, "SELECT COUNT(*) count FROM "+table, nil)
		if energy.Number(rows[0]["count"]) != 0 {
			t.Fatalf("partial import in %s", table)
		}
	}
}
func TestConfigImportPreservesExistingSettings(t *testing.T) {
	app := target(t)
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "settings.json"), []byte(`{"poll_interval_s":5}`), 0600)
	os.WriteFile(filepath.Join(root, "smart_charge.json"), []byte(`{"mode":"test"}`), 0600)
	if err := migrate.ImportConfigs(app, root); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(root, "settings.json"), []byte(`{"poll_interval_s":99}`), 0600)
	if err := migrate.ImportConfigs(app, root); err != nil {
		t.Fatal(err)
	}
	record, err := app.FindFirstRecordByData("app_settings", "key", "tunables")
	if err != nil {
		t.Fatal(err)
	}
	var data map[string]int
	record.UnmarshalJSONField("data", &data)
	if data["poll_interval_s"] != 5 {
		t.Fatal("overwrote imported settings")
	}
}
