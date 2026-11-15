package server

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/compdani/jackery-monitor/internal/forecast"
)

func writeArchive(t *testing.T, s *Server, name string) {
	t.Helper()
	dir := filepath.Join(s.Store.App.DataDir(), "backups")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte("PK"), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestLocalBackupRetentionStatusAndSchedule(t *testing.T) {
	s, h := testServer(t)
	token := contractToken(t, h)
	for i := 1; i <= 4; i++ {
		writeArchive(t, s, fmt.Sprintf("jackery-2026010%d-030000.zip", i))
	}
	writeArchive(t, s, "manual-upload.zip")
	contractJSON(t, h, "POST", "/api/settings", `{"backup_keep_count":2}`, token, 200)
	v := contractJSON(t, h, "POST", "/api/backup/run", `{}`, token, 200)
	if v["ok"] != true || len(v["pruned"].([]any)) != 3 {
		t.Fatal(v)
	}
	list, err := s.listArchives(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	names := []string{}
	for _, a := range list {
		names = append(names, a.Name)
	}
	joined := strings.Join(names, ",")
	if len(list) != 3 || !strings.Contains(joined, "manual-upload.zip") || !strings.Contains(joined, "jackery-20260104-030000.zip") || strings.Contains(joined, "jackery-20260103") {
		t.Fatal(names)
	}
	st := contractJSON(t, h, "GET", "/api/backup/status", "", token, 200)
	if st["configured"] != true || st["last_ok_ts"] == nil || len(st["runs"].([]any)) != 1 || st["remote"] != nil {
		t.Fatal(st)
	}
	if v = contractJSON(t, h, "GET", "/api/backup/snapshots", "", token, 200); len(v["snapshots"].([]any)) != 3 {
		t.Fatal(v)
	}
	// Scheduler: once per local day at backup_schedule_hour.
	values, _, _ := s.settingValues()
	hour := int64(forecast.Number(values, "backup_schedule_hour"))
	at := time.Unix(time.Now().Unix()/86400*86400+hour*3600+120, 0)
	if !s.backupTick(context.Background(), at) || s.backupTick(context.Background(), at.Add(10*time.Minute)) || s.backupTick(context.Background(), at.Add(time.Hour)) {
		t.Fatal("schedule gating")
	}
	if !s.backupTick(context.Background(), at.Add(24*time.Hour)) {
		t.Fatal("next day not run")
	}
	// Manual restore validates names and never accepts remote archives.
	contractJSON(t, h, "POST", "/api/backup/restore", `{"name":"../x.zip"}`, token, 400)
	contractJSON(t, h, "POST", "/api/backup/restore", `{"name":"a.zip","remote":true}`, token, 410)
	restored := ""
	s.RestoreBackup = func(_ context.Context, name string) error { restored = name; return nil }
	contractJSON(t, h, "POST", "/api/backup/restore", `{"name":"manual-upload.zip"}`, token, 200)
	if restored != "manual-upload.zip" {
		t.Fatal(restored)
	}
}

func TestSetupRestoreIsPreAuthOnlyBeforeSetup(t *testing.T) {
	s, h := testServer(t)
	writeArchive(t, s, "jackery-20260101-030000.zip")
	restored := ""
	s.RestoreBackup = func(_ context.Context, name string) error { restored = name; return nil }
	v := contractJSON(t, h, "POST", "/api/backup/setup_restore/test", `{}`, "", 200)
	if v["ok"] != true || v["snapshot_count"] != 1.0 {
		t.Fatal(v)
	}
	if v = contractJSON(t, h, "POST", "/api/backup/setup_restore/snapshots", `{}`, "", 200); len(v["snapshots"].([]any)) != 1 {
		t.Fatal(v)
	}
	contractJSON(t, h, "POST", "/api/backup/setup_restore/restore", `{}`, "", 400)
	contractJSON(t, h, "POST", "/api/backup/setup_restore/restore", `{"snapshot":"missing.zip"}`, "", 404)
	if v = contractJSON(t, h, "POST", "/api/backup/setup_restore/restore", `{"snapshot":"jackery-20260101-030000.zip"}`, "", 200); v["ok"] != true || restored == "" {
		t.Fatal(v, restored)
	}
	if w := request(h, "GET", "/api/backup/setup_restore/snapshots", "", ""); w.Code != 405 {
		t.Fatal(w.Code)
	}
	token := contractToken(t, h)
	for _, path := range []string{"test", "snapshots", "restore"} {
		contractJSON(t, h, "POST", "/api/backup/setup_restore/"+path, `{"snapshot":"jackery-20260101-030000.zip"}`, "", 403)
		contractJSON(t, h, "POST", "/api/backup/setup_restore/"+path, `{"snapshot":"jackery-20260101-030000.zip"}`, token, 403)
	}
}
