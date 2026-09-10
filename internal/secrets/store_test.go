package secrets

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestPythonEnvelopeAndRoundTrip(t *testing.T) {
	root := t.TempDir()
	s := &Store{Path: filepath.Join(root, "creds.json"), KeyPath: filepath.Join(root, ".key")}
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	fixture := `{"v":"v1","alg":"AES-256-GCM","nonce":"AAECAwQFBgcICQoL","tag":"3i1udIMdTLuNbWkgUWE/TQ==","ct":"PCCzdqSMrjm3Y/HiyZ0NH+aW4kyRFi8QXUmG6nBLLJBxcd2P2K5g/FaeXYvh/1xNnDxN/Tul0K1Q5U47NMGHi5dVqRTx6wQ0T3bX"}`
	if err := os.WriteFile(s.KeyPath, key, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(s.Path, []byte(fixture), 0600); err != nil {
		t.Fatal(err)
	}
	creds, err := s.Load()
	if err != nil || creds.Email != "fixture@example.com" || creds.Password != "fixture-password" {
		t.Fatalf("Python envelope: %v %v", creds, err)
	}
	creds.Password = "new-fixture-password"
	if err = s.Save(creds); err != nil {
		t.Fatal(err)
	}
	saved, err := s.Load()
	if err != nil || saved != creds {
		t.Fatalf("roundtrip failed: %v", err)
	}
	raw, _ := os.ReadFile(s.Path)
	if bytes.Contains(raw, []byte(creds.Password)) {
		t.Fatal("plaintext password persisted")
	}
	mode, _ := os.Stat(s.Path)
	if mode.Mode().Perm() != 0600 {
		t.Fatalf("mode %v", mode.Mode())
	}
	if err = s.Forget(); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(s.KeyPath); err != nil {
		t.Fatal("forget must retain shared key")
	}
}
func TestNeverRegeneratesUnreadableKey(t *testing.T) {
	root := t.TempDir()
	s := &Store{Path: filepath.Join(root, "creds.json"), KeyPath: filepath.Join(root, "key")}
	os.WriteFile(s.Path, []byte(`{"v":"v1","alg":"AES-256-GCM","nonce":"","ct":"","tag":""}`), 0600)
	if _, err := s.Load(); err == nil {
		t.Fatal("missing key accepted")
	}
	if _, err := os.Stat(s.KeyPath); !os.IsNotExist(err) {
		t.Fatal("read created a key")
	}
	os.WriteFile(s.KeyPath, []byte("corrupt"), 0600)
	if err := s.Save(Credentials{"a@b.com", "test", "US"}); err == nil {
		t.Fatal("corrupt key was replaced")
	}
	raw, _ := os.ReadFile(s.KeyPath)
	if string(raw) != "corrupt" {
		t.Fatal("key was mutated")
	}
}
