package backup

import (
	"archive/zip"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestVerifiedRestore(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "backup.zip")
	f, e := os.Create(source)
	if e != nil {
		t.Fatal(e)
	}
	z := zip.NewWriter(f)
	w, _ := z.Create("data.db")
	_, _ = w.Write([]byte("db bytes"))
	if e = z.Close(); e != nil {
		t.Fatal(e)
	}
	f.Close()
	manifest, e := Inspect(source)
	if e != nil {
		t.Fatal(e)
	}
	mb, _ := json.Marshal(manifest)
	archive, _ := os.ReadFile(source)
	for _, kind := range []string{"good", "checksum", "missing_manifest", "wrong_name", "unsafe_archive", "existing"} {
		t.Run(kind, func(t *testing.T) {
			remote := map[string][]byte{"backup.zip": archive, "backup.zip.manifest.json": mb}
			if kind == "checksum" {
				remote["backup.zip"] = []byte("corrupt")
			}
			if kind == "missing_manifest" {
				delete(remote, "backup.zip.manifest.json")
			}
			if kind == "wrong_name" {
				m := manifest
				m.Name = "wrong.zip"
				remote["backup.zip.manifest.json"], _ = json.Marshal(m)
			}
			if kind == "unsafe_archive" {
				p := filepath.Join(t.TempDir(), "unsafe.zip")
				f, _ := os.Create(p)
				z := zip.NewWriter(f)
				_, _ = z.Create("data.db")
				_, _ = z.Create("../escape")
				z.Close()
				f.Close()
				m, _ := Inspect(p)
				m.Name = "backup.zip"
				remote["backup.zip.manifest.json"], _ = json.Marshal(m)
				remote["backup.zip"], _ = os.ReadFile(p)
			}
			r := Remote{Credentials: Credentials{Transport: "rsyncd", Host: "nas.local", Module: "backup"}, Run: func(_ context.Context, _ string, args, env []string) error {
				a, b := args[len(args)-2], args[len(args)-1]
				data, ok := remote[filepath.Base(a)]
				if !ok {
					return os.ErrNotExist
				}
				return os.WriteFile(b, data, 0600)
			}}
			destination := filepath.Join(t.TempDir(), "restored.zip")
			if kind == "existing" {
				os.WriteFile(destination, []byte("keep"), 0600)
			}
			_, e := r.DownloadVerified(context.Background(), "backup.zip", destination)
			if kind == "good" {
				if e != nil {
					t.Fatal(e)
				}
				b, _ := os.ReadFile(destination)
				if string(b) != string(archive) {
					t.Fatal("archive changed")
				}
				info, _ := os.Stat(destination)
				if info.Mode().Perm() != 0600 {
					t.Fatal(info.Mode())
				}
			} else {
				if e == nil {
					t.Fatal("invalid restore accepted")
				}
				if kind == "existing" {
					b, _ := os.ReadFile(destination)
					if string(b) != "keep" {
						t.Fatal("existing file replaced")
					}
				} else if _, e = os.Stat(destination); !os.IsNotExist(e) {
					t.Fatal("failed restore left destination")
				}
			}
		})
	}
	r := Remote{}
	for _, name := range []string{"../backup.zip", "bad;command.zip", "backup.tar", "/backup.zip"} {
		if _, e := r.DownloadVerified(context.Background(), name, filepath.Join(dir, "out")); e == nil {
			t.Fatal(name)
		}
	}
	if strings.Contains(string(mb), "password") {
		t.Fatal("secret in manifest")
	}
}
