package server

import (
	"encoding/json"
	"testing"
)

func TestPiBackupDisablesRemoteOperations(t *testing.T) {
	s, h := testServer(t)
	w := request(h, "POST", "/api/auth/setup", `{"username":"admin","password":"secure-pass"}`, "")
	var auth map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &auth); err != nil {
		t.Fatal(err)
	}
	token := auth["token"].(string)
	// Even pre-existing remote credentials must never trigger remote transport.
	if err := s.Store.SaveSecret(s.Key, "backup", map[string]any{"host": "unreachable.invalid", "password": "keep"}); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ method, path, body string }{
		{"GET", "/api/backup/discover", ""},
		{"POST", "/api/backup/list-shares", `{}`},
		{"GET", "/api/backup/credentials", ""},
		{"POST", "/api/backup/credentials", `{}`},
		{"DELETE", "/api/backup/credentials", ""},
		{"POST", "/api/backup/test", `{}`},
		{"POST", "/api/backup/restore", `{"name":"archive.zip","remote":true}`},
	} {
		w := request(h, c.method, c.path, c.body, token)
		if w.Code != 410 {
			t.Fatalf("%s: %d %s", c.path, w.Code, w.Body.String())
		}
	}
	saved, err := s.Store.Secret(s.Key, "backup")
	if err != nil || saved["password"] != "keep" {
		t.Fatalf("legacy data changed: %v %v", saved, err)
	}
	w = request(h, "POST", "/api/backup/run", `{}`, token)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var created map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created["remote"] != false || created["ok"] != true {
		t.Fatal(created)
	}
	w = request(h, "GET", "/api/backup/snapshots", "", token)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var listed struct {
		Snapshots []struct {
			Name string `json:"name"`
		} `json:"snapshots"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Snapshots) != 1 || listed.Snapshots[0].Name != created["name"] {
		t.Fatal(w.Body.String())
	}
}
