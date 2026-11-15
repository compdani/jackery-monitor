package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/compdani/jackery-monitor/internal/backup"
	"github.com/pocketbase/pocketbase/tools/filesystem"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func (s *Server) backupRoutes(w http.ResponseWriter, r *http.Request, b map[string]any) bool {
	p := r.URL.Path
	if p == "/api/backup/discover" && r.Method == "GET" {
		ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
		defer cancel()
		hosts := (backup.Discovery{LocalIP: backup.LocalIPv4(), Subnets: os.Getenv("BACKUP_SCAN_SUBNETS")}).Discover(ctx, 32)
		jsonOut(w, 200, map[string]any{"hosts": hosts})
		return true
	}
	if p == "/api/backup/list-shares" && r.Method == "POST" {
		shares, e := backup.ListShares(r.Context(), str(b, "host"), str(b, "username"), str(b, "password"), str(b, "domain"), nil)
		if e != nil {
			jsonOut(w, 200, map[string]any{"ok": false, "error": e.Error()})
		} else {
			jsonOut(w, 200, map[string]any{"ok": true, "shares": shares})
		}
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
			m, e := s.Store.Secret(s.Key, "backup")
			if e != nil {
				storageError(w, e)
				return true
			}
			data, _ := json.Marshal(m)
			var creds backup.Credentials
			if json.Unmarshal(data, &creds) != nil || creds.Validate() != nil {
				fail(w, 400, "invalid backup configuration")
				return true
			}
			dir, e := os.MkdirTemp("", "jackery-stage-restore-")
			if e != nil {
				storageError(w, e)
				return true
			}
			defer os.RemoveAll(dir)
			local := filepath.Join(dir, name)
			if _, e = (backup.Remote{Credentials: creds}).DownloadVerified(ctx, name, local); e != nil {
				fail(w, 502, e.Error())
				return true
			}
			fs, e := s.Store.App.NewBackupsFilesystem()
			if e != nil {
				storageError(w, e)
				return true
			}
			defer fs.Close()
			fs.SetContext(ctx)
			// Preserve an existing backup under the same name.
			exists, e := fs.Exists(name)
			if e != nil {
				storageError(w, e)
				return true
			}
			if exists {
				fail(w, 409, "backup name already exists locally")
				return true
			}
			file, e := filesystem.NewFileFromPath(local)
			if e == nil {
				e = fs.UploadFile(file, name)
			}
			if e != nil {
				storageError(w, e)
				return true
			}
		}
		if e := s.Store.App.RestoreBackup(ctx, name); e != nil {
			fail(w, 500, e.Error())
		} else {
			jsonOut(w, 200, map[string]any{"ok": true, "name": name})
		}
		return true
	}
	if p == "/api/backup/credentials" {
		switch r.Method {
		case "GET":
			m, e := s.Store.Secret(s.Key, "backup")
			if e != nil && !errors.Is(e, sql.ErrNoRows) {
				storageError(w, e)
				return true
			}
			out := map[string]any{}
			for k, v := range m {
				switch k {
				case "password", "ssh_key", "rsyncd_password":
					out["has_"+k] = v != ""
				default:
					out[k] = v
				}
			}
			jsonOut(w, 200, out)
		case "POST":
			m, e := s.Store.Secret(s.Key, "backup")
			if e != nil && !errors.Is(e, sql.ErrNoRows) {
				storageError(w, e)
				return true
			}
			if m == nil {
				m = map[string]any{}
			}
			for k, v := range b {
				if v == nil {
					continue
				}
				if (k == "password" || k == "ssh_key" || k == "rsyncd_password") && v == "" {
					continue
				}
				m[k] = v
			}
			data, _ := json.Marshal(m)
			var creds backup.Credentials
			if json.Unmarshal(data, &creds) != nil || creds.Validate() != nil {
				fail(w, 400, "invalid backup credentials")
				return true
			}
			if e = s.Store.SaveSecret(s.Key, "backup", m); e != nil {
				storageError(w, e)
			} else {
				jsonOut(w, 200, map[string]any{"ok": true})
			}
		case "DELETE":
			e := s.Store.Delete("credentials", "backup")
			if e != nil && !errors.Is(e, sql.ErrNoRows) {
				storageError(w, e)
			} else {
				jsonOut(w, 200, map[string]any{"ok": true})
			}
		default:
			fail(w, 405, "method not allowed")
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
	m, e := s.Store.Secret(s.Key, "backup")
	if errors.Is(e, sql.ErrNoRows) {
		jsonOut(w, 200, map[string]any{"ok": true, "name": name, "remote": false})
		return true
	}
	if e != nil {
		storageError(w, e)
		return true
	}
	data, _ := json.Marshal(m)
	var creds backup.Credentials
	if e = json.Unmarshal(data, &creds); e != nil {
		fail(w, 500, "invalid backup configuration")
		return true
	}
	e = s.uploadBackup(ctx, name, backup.Remote{Credentials: creds})
	status := map[string]any{"name": name, "ok": e == nil, "remote": true, "ts": float64(time.Now().Unix())}
	if e != nil {
		status["error"] = e.Error()
	}
	if saveErr := s.Store.Put("settings", "backup_status", "", status); saveErr != nil {
		storageError(w, saveErr)
		return true
	}
	if e != nil {
		fail(w, 502, e.Error())
	} else {
		jsonOut(w, 200, status)
	}
	return true
}
func (s *Server) uploadBackup(ctx context.Context, name string, remote backup.Remote) error {
	fs, e := s.Store.App.NewBackupsFilesystem()
	if e != nil {
		return e
	}
	defer fs.Close()
	fs.SetContext(ctx)
	reader, e := fs.GetReader(name)
	if e != nil {
		return e
	}
	defer reader.Close()
	dir, e := os.MkdirTemp("", "jackery-export-")
	if e != nil {
		return e
	}
	defer os.RemoveAll(dir)
	p := filepath.Join(dir, name)
	file, e := os.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return e
	}
	_, e = io.Copy(file, reader)
	closeErr := file.Close()
	if e != nil {
		return e
	}
	if closeErr != nil {
		return closeErr
	}
	_, e = remote.Upload(ctx, p)
	return e
}
