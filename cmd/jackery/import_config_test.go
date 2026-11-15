package main

import (
	"bytes"
	"github.com/pocketbase/pocketbase"
	"os"
	"path/filepath"
	"testing"
)

func TestImportConfigCLIDryRunLeavesDestinationUnopened(t *testing.T) {
	source := t.TempDir()
	destination := filepath.Join(t.TempDir(), "unopened")
	key := filepath.Join(source, "key")
	if err := os.WriteFile(key, bytes.Repeat([]byte{1}, 32), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("JACKERY_AT_REST_KEY_FILE", key)
	os.WriteFile(filepath.Join(source, "settings.json"), []byte(`{"poll_interval_s":12}`), 0600)
	app := pocketbase.NewWithConfig(pocketbase.Config{DefaultDataDir: destination})
	cmd := importConfigCommand(app)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{source, "--dry-run"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out.Bytes(), []byte(`"dry_run":true`)) || !bytes.Contains(out.Bytes(), []byte(`"candidates":1`)) {
		t.Fatal(out.String())
	}
	if _, err := os.Stat(destination); !os.IsNotExist(err) {
		t.Fatal("dry run opened destination", err)
	}
}
func TestImportConfigCLIRequiresExistingKeyBeforeBootstrap(t *testing.T) {
	destination := filepath.Join(t.TempDir(), "unopened")
	key := filepath.Join(t.TempDir(), "absent")
	t.Setenv("JACKERY_AT_REST_KEY_FILE", key)
	app := pocketbase.NewWithConfig(pocketbase.Config{DefaultDataDir: destination})
	cmd := importConfigCommand(app)
	cmd.SetArgs([]string{t.TempDir()})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	if err := cmd.Execute(); err == nil {
		t.Fatal("missing key accepted")
	}
	for _, path := range []string{destination, key} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatal("created", path)
		}
	}
}
