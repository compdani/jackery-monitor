package server

import (
	"context"
	"net/http"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/compdani/jackery-monitor/internal/forecast"
)

// Local-only backups (Pi scope): PocketBase archives named jackery-*.zip,
// a daily scheduler, keep-count retention and pre-auth setup restore. No
// remote transport exists.

const backupPrefix = "jackery-"

type backupRuntime struct {
	mu      sync.Mutex
	lastDay string
}

type archive struct {
	Name     string `json:"name"`
	Size     int64  `json:"size"`
	Modified int64  `json:"modified"`
}

func (s *Server) listArchives(ctx context.Context) ([]archive, error) {
	fs, err := s.Store.App.NewBackupsFilesystem()
	if err != nil {
		return nil, err
	}
	defer fs.Close()
	fs.SetContext(ctx)
	entries, err := fs.List("")
	if err != nil {
		return nil, err
	}
	out := []archive{}
	for _, e := range entries {
		if !e.IsDir && strings.HasSuffix(e.Key, ".zip") {
			out = append(out, archive{Name: e.Key, Size: e.Size, Modified: e.ModTime.Unix()})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Modified != out[j].Modified {
			return out[i].Modified > out[j].Modified
		}
		return out[i].Name > out[j].Name
	})
	return out, nil
}

// pruneArchives keeps the newest keep jackery-*.zip archives. Archives the
// app did not create (manual uploads, PocketBase auto backups) are untouched.
func (s *Server) pruneArchives(ctx context.Context, keep int) ([]string, error) {
	if keep < 1 {
		return nil, nil
	}
	list, err := s.listArchives(ctx)
	if err != nil {
		return nil, err
	}
	ours := []archive{}
	for _, a := range list {
		if strings.HasPrefix(a.Name, backupPrefix) {
			ours = append(ours, a)
		}
	}
	sort.Slice(ours, func(i, j int) bool { return ours[i].Name > ours[j].Name }) // timestamped names
	if len(ours) <= keep {
		return []string{}, nil
	}
	fs, err := s.Store.App.NewBackupsFilesystem()
	if err != nil {
		return nil, err
	}
	defer fs.Close()
	fs.SetContext(ctx)
	removed := []string{}
	for _, a := range ours[keep:] {
		if err := fs.Delete(a.Name); err != nil {
			s.Store.App.Logger().Warn("backup prune", "archive", a.Name, "error", err)
			continue
		}
		removed = append(removed, a.Name)
	}
	return removed, nil
}

// runBackup creates an archive, prunes to backup_keep_count and appends the
// result to the persisted status (last 20 runs, last_ok_ts).
func (s *Server) runBackup(ctx context.Context, trigger string) map[string]any {
	s.backups.mu.Lock()
	defer s.backups.mu.Unlock()
	start := time.Now()
	name := backupPrefix + start.UTC().Format("20060102-150405") + ".zip"
	call, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	result := map[string]any{"ok": false, "name": name, "remote": false, "trigger": trigger, "ts": float64(start.Unix()), "error": nil, "pruned": []string{}}
	if err := s.Store.App.CreateBackup(call, name); err != nil {
		result["error"] = err.Error()
	} else {
		result["ok"] = true
		keep := 30
		if values, _, err := s.settingValues(); err == nil && forecast.Number(values, "backup_keep_count") >= 1 {
			keep = int(forecast.Number(values, "backup_keep_count"))
		}
		removed, err := s.pruneArchives(call, keep)
		if err != nil {
			result["prune_error"] = err.Error()
		}
		if removed != nil {
			result["pruned"] = removed
		}
	}
	result["duration_s"] = time.Since(start).Seconds()
	status, err := s.Store.Get("settings", "backup_status")
	if err != nil && !isMissing(err) {
		s.Store.App.Logger().Warn("backup status", "error", err)
	}
	if status == nil {
		status = map[string]any{}
	}
	runs, _ := status["runs"].([]any)
	runs = append([]any{result}, runs...)
	if len(runs) > 20 {
		runs = runs[:20]
	}
	status["runs"] = runs
	if result["ok"] == true {
		status["last_ok_ts"] = result["ts"]
		for k, v := range result {
			status[k] = v // legacy top-level fields kept for compatibility
		}
	}
	if err := s.Store.Put("settings", "backup_status", "", status); err != nil {
		s.Store.App.Logger().Warn("backup status", "error", err)
	}
	return result
}

// backupTick runs once per local day at backup_schedule_hour.
func (s *Server) backupTick(ctx context.Context, now time.Time) bool {
	values, _, err := s.settingValues()
	if err != nil {
		return false
	}
	local := now.Add(time.Duration(s.tzOffset()) * time.Second).UTC()
	if float64(local.Hour()) != forecast.Number(values, "backup_schedule_hour") {
		return false
	}
	day := local.Format("2006-01-02")
	s.backups.mu.Lock()
	done := s.backups.lastDay == day
	if !done {
		s.backups.lastDay = day
	}
	s.backups.mu.Unlock()
	if done {
		return false
	}
	if res := s.runBackup(ctx, "scheduled"); res["ok"] != true {
		s.Store.App.Logger().Warn("scheduled backup failed", "error", res["error"])
	}
	return true
}

// BackupLoop checks the schedule every minute; settings changes apply live.
func (s *Server) BackupLoop(ctx context.Context) {
	for {
		s.backupTick(ctx, time.Now())
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Minute):
		}
	}
}
func validArchiveName(name string) bool {
	return name != "" && filepath.Base(name) == name && strings.HasSuffix(name, ".zip") && !strings.HasPrefix(name, ".")
}

// setupRestore serves /api/backup/setup_restore/* before authentication and
// only while no account exists (Python's _require_no_user). Restores use an
// archive already present in <pb_data>/backups; nothing is uploaded pre-auth.
func (s *Server) setupRestore(w http.ResponseWriter, r *http.Request) bool {
	p := r.URL.Path
	if !strings.HasPrefix(p, "/api/backup/setup_restore/") {
		return false
	}
	if r.Method != "POST" {
		fail(w, 405, "method not allowed")
		return true
	}
	s.setupMu.Lock()
	defer s.setupMu.Unlock()
	if has, err := s.hasUser(); err != nil {
		fail(w, 500, "auth storage unavailable")
		return true
	} else if has {
		fail(w, 403, "already_set_up")
		return true
	}
	b, err := body(r)
	if err != nil {
		fail(w, 400, "invalid JSON object")
		return true
	}
	list, err := s.listArchives(r.Context())
	if err != nil {
		storageError(w, err)
		return true
	}
	switch strings.TrimPrefix(p, "/api/backup/setup_restore/") {
	case "test":
		jsonOut(w, 200, map[string]any{"ok": true, "local": true, "snapshot_count": len(list), "detail": "local PocketBase backups directory is readable"})
	case "snapshots":
		jsonOut(w, 200, map[string]any{"snapshots": list})
	case "restore":
		name := str(b, "name")
		if name == "" {
			name = str(b, "snapshot")
		}
		if !validArchiveName(name) {
			fail(w, 400, "snapshot is required")
			return true
		}
		found := false
		for _, a := range list {
			found = found || a.Name == name
		}
		if !found {
			fail(w, 404, "snapshot not found")
			return true
		}
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
		defer cancel()
		if err := s.restoreArchive(ctx, name); err != nil {
			fail(w, 500, err.Error())
			return true
		}
		jsonOut(w, 200, map[string]any{"ok": true, "name": name, "restart": true})
	default:
		fail(w, 404, "not found")
	}
	return true
}
func (s *Server) restoreArchive(ctx context.Context, name string) error {
	if s.RestoreBackup != nil {
		return s.RestoreBackup(ctx, name)
	}
	return s.Store.App.RestoreBackup(ctx, name)
}
