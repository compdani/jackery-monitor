package backup

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRsyncReferenceTargets(t *testing.T) {
	for _, c := range []struct {
		Creds Credentials
		Want  string
	}{{Credentials{Transport: "rsync_ssh", SSHUser: "backupuser", Host: "nas.local", TargetDir: "/backups"}, "backupuser@nas.local:/backups/snapshot/MANIFEST.json"}, {Credentials{Transport: "rsyncd", DaemonUser: "alice", Host: "nas.local", Module: "backups", Subpath: "jackery"}, "rsync://alice@nas.local/backups/jackery/snapshot/MANIFEST.json"}, {Credentials{Transport: "rsyncd_ssh", SSHUser: "bob", Host: "nas.local", Module: "backups", Subpath: "jackery"}, "bob@nas.local::backups/jackery/snapshot/MANIFEST.json"}} {
		if got := c.Creds.Remote("snapshot/MANIFEST.json"); got != c.Want {
			t.Fatalf("got %s want %s", got, c.Want)
		}
	}
}
func TestSSHKeyLifecycle(t *testing.T) {
	var keyPath string
	r := Remote{Credentials: Credentials{Transport: "rsync_ssh", Host: "nas.local", SSHUser: "backup", SSHKey: "SECRET", TargetDir: "/backup"}, Run: func(_ context.Context, name string, args, env []string) error {
		if name != "rsync" {
			t.Fatal(name)
		}
		for i, a := range args {
			if a == "-e" {
				keyPath = strings.Split(args[i+1], "-i ")[1]
			}
		}
		info, e := os.Stat(keyPath)
		if e != nil || info.Mode().Perm() != 0600 {
			t.Fatal(info, e)
		}
		b, _ := os.ReadFile(keyPath)
		if string(b) != "SECRET\n" {
			t.Fatal(string(b))
		}
		if strings.Contains(strings.Join(args, " "), "SECRET") {
			t.Fatal("secret in argv")
		}
		return nil
	}}
	if e := r.Put(context.Background(), "/tmp/test.zip", "test.zip"); e != nil {
		t.Fatal(e)
	}
	if _, e := os.Stat(keyPath); !os.IsNotExist(e) {
		t.Fatal("temporary key retained")
	}
}
func TestVerifiedUploadCompletionMarker(t *testing.T) {
	for _, corrupt := range []bool{false, true} {
		t.Run(map[bool]string{false: "good", true: "corrupt"}[corrupt], func(t *testing.T) {
			dir := t.TempDir()
			src := filepath.Join(dir, "archive.zip")
			os.WriteFile(src, []byte("backup bytes"), 0600)
			remote := map[string][]byte{}
			r := Remote{Credentials: Credentials{Transport: "rsyncd", Host: "nas.local", Module: "backups"}, Run: func(_ context.Context, _ string, args, env []string) error {
				a, b := args[len(args)-2], args[len(args)-1]
				if strings.HasPrefix(a, "rsync://") {
					data := remote[filepath.Base(a)]
					if corrupt {
						data = []byte("corrupt")
					}
					return os.WriteFile(b, data, 0600)
				}
				data, e := os.ReadFile(a)
				remote[filepath.Base(b)] = data
				return e
			}}
			_, e := r.Upload(context.Background(), src)
			_, complete := remote["archive.zip.manifest.json"]
			if corrupt && (e == nil || complete) {
				t.Fatal("corrupt backup marked complete")
			}
			if !corrupt && (e != nil || !complete) {
				t.Fatal(e, complete)
			}
		})
	}
}
func TestRejectUnsafeRemotePaths(t *testing.T) {
	for _, p := range []string{"../bad", "archive;rm.zip", "-option", "x\n"} {
		r := Remote{Credentials: Credentials{Transport: "rsyncd", Host: "nas", Module: "backup"}}
		if e := r.Put(context.Background(), "/tmp/file", p); e == nil {
			t.Fatal(p)
		}
	}
}
