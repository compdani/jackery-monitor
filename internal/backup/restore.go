package backup

import (
	"archive/zip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// DownloadVerified requires the upload completion manifest and checks both
// size and SHA-256 before returning an archive for PocketBase to restore.
// Destination must not exist; checksum failures remove all downloaded bytes.
func (r Remote) DownloadVerified(ctx context.Context, name, destination string) (Manifest, error) {
	var manifest Manifest
	if filepath.Base(name) != name || !strings.HasSuffix(name, ".zip") || !safeRemotePath(name) {
		return manifest, errors.New("invalid backup archive name")
	}
	dir, e := os.MkdirTemp("", "jackery-restore-")
	if e != nil {
		return manifest, e
	}
	defer os.RemoveAll(dir)
	mp := filepath.Join(dir, "manifest.json")
	if e = r.Get(ctx, name+".manifest.json", mp); e != nil {
		return manifest, e
	}
	f, e := os.Open(mp)
	if e != nil {
		return manifest, e
	}
	e = json.NewDecoder(io.LimitReader(f, 65536)).Decode(&manifest)
	f.Close()
	if e != nil {
		return manifest, e
	}
	if manifest.Name != name || manifest.Size <= 0 || len(manifest.SHA256) != 64 {
		return manifest, errors.New("invalid backup completion manifest")
	}
	archive := filepath.Join(dir, name)
	if e = r.Get(ctx, name, archive); e != nil {
		return manifest, e
	}
	got, e := Inspect(archive)
	if e != nil {
		return manifest, e
	}
	if got.Size != manifest.Size || got.SHA256 != manifest.SHA256 {
		return manifest, errors.New("remote backup checksum mismatch")
	}
	if e = ValidateArchive(archive); e != nil {
		return manifest, e
	}
	src, e := os.Open(archive)
	if e != nil {
		return manifest, e
	}
	defer src.Close()
	dst, e := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if e != nil {
		return manifest, e
	}
	_, e = io.Copy(dst, src)
	closeErr := dst.Close()
	if e == nil {
		e = closeErr
	}
	if e != nil {
		os.Remove(destination)
	}
	return manifest, e
}
func ValidateArchive(file string) error {
	z, e := zip.OpenReader(file)
	if e != nil {
		return e
	}
	defer z.Close()
	hasDB := false
	for _, f := range z.File {
		if f.Name == "data.db" {
			hasDB = true
		}
		if f.Mode()&os.ModeSymlink != 0 {
			return errors.New("backup archive contains symlink")
		}
		if strings.Contains(f.Name, "\\") || strings.HasPrefix(f.Name, "/") || !filepath.IsLocal(f.Name) {
			return errors.New("unsafe backup archive path")
		}
	}
	if !hasDB {
		return errors.New("archive does not contain PocketBase data.db")
	}
	return nil
}
