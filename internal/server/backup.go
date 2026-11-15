package server

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"time"
)

func (s *Server) backupRoutes(w http.ResponseWriter, r *http.Request, b map[string]any) bool {
	p := r.URL.Path
	if p == "/api/backup/list-rsync-modules" || p == "/api/backup/discover" || p == "/api/backup/list-shares" || p == "/api/backup/credentials" || p == "/api/backup/test" {
		fail(w, http.StatusGone, "remote backups retired; use local PocketBase backups")
		return true
	}
	if p == "/api/backup/snapshots" && r.Method == "GET" {
		list, e := s.listArchives(r.Context())
		if e != nil {
			storageError(w, e)
			return true
		}
		jsonOut(w, 200, map[string]any{"snapshots": list})
		return true
	}
	if p == "/api/backup/restore" && r.Method == "POST" {
		if remote, _ := b["remote"].(bool); remote {
			fail(w, http.StatusGone, "remote backups retired")
			return true
		}
		name := str(b, "name")
		if name == "" {
			name = str(b, "snapshot")
		}
		if !validArchiveName(name) {
			fail(w, 400, "backup archive name required")
			return true
		}
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
		defer cancel()
		if e := s.restoreArchive(ctx, name); e != nil {
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
			return true
		}
		if v["runs"] == nil {
			v["runs"] = []any{}
		}
		if _, ok := v["last_ok_ts"]; !ok {
			v["last_ok_ts"] = nil
		}
		v["configured"], v["remote"], v["local"] = true, nil, true
		jsonOut(w, 200, v)
		return true
	}
	if p != "/api/backup/run" || r.Method != "POST" {
		return false
	}
	res := s.runBackup(r.Context(), "manual")
	if res["ok"] != true {
		fail(w, 500, "backup failed")
		return true
	}
	jsonOut(w, 200, res)
	return true
}
