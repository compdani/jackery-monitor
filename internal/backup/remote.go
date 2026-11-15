// Package backup transfers and verifies PocketBase backup archives. Native
// archives differ from Python's directory snapshots; restore uses PocketBase.
package backup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strings"
)

type Credentials struct {
	Transport      string `json:"transport"`
	Host           string `json:"host"`
	Username       string `json:"username"`
	Password       string `json:"password"`
	Domain         string `json:"domain"`
	Share          string `json:"share"`
	SSHUser        string `json:"ssh_user"`
	SSHKey         string `json:"ssh_key"`
	TargetDir      string `json:"target_dir"`
	Module         string `json:"rsync_module"`
	Subpath        string `json:"target_subpath"`
	DaemonUser     string `json:"rsyncd_user"`
	DaemonPassword string `json:"rsyncd_password"`
}

var safeHost = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]*$`)
var safePath = regexp.MustCompile(`^[a-zA-Z0-9_./ -]+$`)

func safeRemotePath(v string) bool {
	if v == "" || v == "." || v == ".." {
		return false
	}
	for _, p := range strings.Split(v, "/") {
		if p == ".." {
			return false
		}
	}
	return safePath.MatchString(v) && !strings.HasPrefix(v, "-")
}
func (c Credentials) Validate() error {
	if !safeHost.MatchString(c.Host) {
		return errors.New("invalid backup host")
	}
	switch c.Transport {
	case "smb":
		if strings.Trim(c.Share, "/") == "" || !safeRemotePath(c.Share) || strings.ContainsAny(c.Username+c.Password+c.Domain, "\r\n") {
			return errors.New("invalid SMB credentials")
		}
	case "rsync_ssh":
		if !safeHost.MatchString(c.SSHUser) || !strings.HasPrefix(c.TargetDir, "/") || !safeRemotePath(c.TargetDir) || c.SSHKey == "" {
			return errors.New("SSH user, key and absolute target_dir required")
		}
	case "rsyncd", "rsyncd_ssh":
		if !safeHost.MatchString(c.Module) || !safeRemotePath("x/"+c.Subpath) {
			return errors.New("invalid rsync module or subpath")
		}
		if c.DaemonUser != "" && !safeHost.MatchString(c.DaemonUser) {
			return errors.New("invalid daemon user")
		}
		if c.Transport == "rsyncd_ssh" && (!safeHost.MatchString(c.SSHUser) || c.SSHKey == "") {
			return errors.New("SSH user and key required")
		}
	default:
		return errors.New("unsupported backup transport")
	}
	return nil
}
func (c Credentials) Remote(sub string) string {
	sub = strings.TrimLeft(sub, "/")
	switch c.Transport {
	case "rsync_ssh":
		base := strings.TrimRight(c.TargetDir, "/")
		if sub != "" {
			base += "/" + sub
		}
		return c.SSHUser + "@" + c.Host + ":" + base
	case "rsyncd":
		base := "rsync://" + c.DaemonUser + "@" + c.Host + "/" + c.Module
		p := strings.Trim(c.Subpath, "/")
		if sub != "" {
			p = path.Join(p, sub)
		}
		if p != "" {
			base += "/" + p
		}
		return base
	case "rsyncd_ssh":
		base := c.SSHUser + "@" + c.Host + "::" + c.Module
		p := strings.Trim(c.Subpath, "/")
		if sub != "" {
			p = path.Join(p, sub)
		}
		if p != "" {
			base += "/" + p
		}
		return base
	}
	return ""
}

type Runner func(context.Context, string, []string, []string) error

func run(ctx context.Context, name string, args, env []string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = append(os.Environ(), env...)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s transfer failed: %w", name, err)
	}
	return nil
}

type Remote struct {
	Credentials Credentials
	Run         Runner
}

func secretFile(prefix, text string) (string, error) {
	f, e := os.CreateTemp("", prefix)
	if e != nil {
		return "", e
	}
	name := f.Name()
	if _, e = f.WriteString(text); e != nil {
		f.Close()
		os.Remove(name)
		return "", e
	}
	if e = f.Close(); e != nil {
		os.Remove(name)
		return "", e
	}
	return name, nil
}
func (r Remote) transfer(ctx context.Context, local, remote string, pull bool) error {
	c := r.Credentials
	if e := c.Validate(); e != nil {
		return e
	}
	if !safeRemotePath(remote) || filepath.Base(remote) != remote {
		return errors.New("backup name must be a basename")
	}
	invoke := r.Run
	if invoke == nil {
		invoke = run
	}
	if c.Transport == "smb" {
		domain := c.Domain
		if domain == "" {
			domain = "WORKGROUP"
		}
		auth, e := secretFile("jackery-smb-", "username = "+c.Username+"\npassword = "+c.Password+"\ndomain = "+domain+"\n")
		if e != nil {
			return e
		}
		defer os.Remove(auth)
		share := strings.SplitN(strings.TrimLeft(c.Share, "/"), "/", 2)
		prefix := ""
		if len(share) > 1 {
			prefix = "cd \"" + share[1] + "\"; "
		}
		if strings.ContainsAny(local, "\"\r\n;") {
			return errors.New("unsupported local filename")
		}
		command := "put \"" + local + "\" \"" + remote + "\""
		if pull {
			command = "get \"" + remote + "\" \"" + local + "\""
		}
		return invoke(ctx, "smbclient", []string{"//" + c.Host + "/" + share[0], "-A", auth, "-c", prefix + command}, nil)
	}
	args := []string{"--protect-args"}
	env := []string{}
	if c.Transport == "rsync_ssh" || c.Transport == "rsyncd_ssh" {
		key, e := secretFile("jackery-rsync-key-", strings.TrimRight(c.SSHKey, "\n")+"\n")
		if e != nil {
			return e
		}
		defer os.Remove(key)
		args = append(args, "-e", "ssh -o BatchMode=yes -o StrictHostKeyChecking=yes -i "+key)
	}
	if c.Transport == "rsyncd" || c.Transport == "rsyncd_ssh" {
		env = append(env, "RSYNC_PASSWORD="+c.DaemonPassword)
	}
	target := c.Remote(remote)
	if pull {
		args = append(args, "--", target, local)
	} else {
		args = append(args, "--", local, target)
	}
	return invoke(ctx, "rsync", args, env)
}
func (r Remote) Put(ctx context.Context, local, name string) error {
	return r.transfer(ctx, local, name, false)
}
func (r Remote) Get(ctx context.Context, name, local string) error {
	return r.transfer(ctx, local, name, true)
}

type Manifest struct {
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

func Inspect(file string) (Manifest, error) {
	f, e := os.Open(file)
	if e != nil {
		return Manifest{}, e
	}
	defer f.Close()
	h := sha256.New()
	n, e := io.Copy(h, f)
	return Manifest{Name: filepath.Base(file), Size: n, SHA256: hex.EncodeToString(h.Sum(nil))}, e
}

// Upload round-trips the archive before publishing its manifest, which acts as
// a completion marker. Incomplete remote files never acquire a manifest.
func (r Remote) Upload(ctx context.Context, file string) (Manifest, error) {
	m, e := Inspect(file)
	if e != nil {
		return m, e
	}
	if e = r.Put(ctx, file, m.Name); e != nil {
		return m, e
	}
	dir, e := os.MkdirTemp("", "jackery-backup-verify-")
	if e != nil {
		return m, e
	}
	defer os.RemoveAll(dir)
	verify := filepath.Join(dir, m.Name)
	if e = r.Get(ctx, m.Name, verify); e != nil {
		return m, e
	}
	got, e := Inspect(verify)
	if e != nil {
		return m, e
	}
	if got.Size != m.Size || got.SHA256 != m.SHA256 {
		return m, errors.New("remote backup checksum mismatch")
	}
	b, _ := json.Marshal(m)
	manifest := filepath.Join(dir, m.Name+".manifest.json")
	if e = os.WriteFile(manifest, b, 0600); e != nil {
		return m, e
	}
	return m, r.Put(ctx, manifest, m.Name+".manifest.json")
}
