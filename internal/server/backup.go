package server

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"time"
)

func (s *Server) backupRoutes(w http.ResponseWriter, r *http.Request, b map[string]any) bool {
	p := r.URL.Path
	if p == "/api/backup/list-rsync-modules" || p == "/api/backup/discover" || p == "/api/backup/list-shares" || p == "/api/backup/credentials" || p == "/api/backup/test" {
		fail(w, http.StatusGone, "remote backups retired; use local PocketBase backups")
		return true
	}
	if p == "/api/backup/snapshots" && r.Method == "GET" {
		fs, e := s.Store.App.NewBackupsFilesystem()
		if e != nil {
			storageError(w, e)
			return true
		}
		defer fs.Close()
		fs.SetContext(r.Context())
		entries, e := fs.List("")
		if e != nil {
			storageError(w, e)
			return true
		}
		out := []any{}
		for _, entry := range entries {
			if !entry.IsDir && strings.HasSuffix(entry.Key, ".zip") {
				out = append(out, map[string]any{"name": entry.Key, "size": entry.Size, "modified": entry.ModTime.Unix()})
			}
		}
		jsonOut(w, 200, map[string]any{"snapshots": out})
		return true
	}
	if p == "/api/backup/restore" && r.Method == "POST" {
		name := str(b, "name")
		if name == "" || filepath.Base(name) != name || !strings.HasSuffix(name, ".zip") {
			fail(w, 400, "backup archive name required")
			return true
		}
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
		defer cancel()
		if remote, _ := b["remote"].(bool); remote {
			fail(w, http.StatusGone, "remote backups retired")
			return true
		}
		if e := s.Store.App.RestoreBackup(ctx, name); e != nil {
			fail(w, 500, e.Error())
		} else {
			jsonOut(w, 200, map[string]any{"ok": true, "name": name})
		}
		return true
	}
	if p == "/api/backup/status" && r.Method == "GET" {
		v, e := s.Store.Get("settings", "backup_status")
		if errors.Is(e, sql.ErrNoRows) {
			v = map[string]any{}
			e = nil
		}
		if e != nil {
			storageError(w, e)
		} else {
			jsonOut(w, 200, v)
		}
		return true
	}
	if p != "/api/backup/run" || r.Method != "POST" {
		return false
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
	defer cancel()
	name := "jackery-" + time.Now().UTC().Format("20060102-150405") + ".zip"
	if e := s.Store.App.CreateBackup(ctx, name); e != nil {
		fail(w, 500, "backup failed")
		return true
	}
	status := map[string]any{"name": name, "ok": true, "remote": false, "ts": float64(time.Now().Unix())}
	if err := s.Store.Put("settings", "backup_status", "", status); err != nil {
		storageError(w, err)
	} else {
		jsonOut(w, 200, status)
	}
	return true
}
