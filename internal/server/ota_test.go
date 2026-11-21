package server

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// otaZip builds a minimal iOS-only `npm run ota` export.
func otaZip(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(body))
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func goodExport() map[string]string {
	return map[string]string{
		"metadata.json":                     `{"version":0,"bundler":"metro","fileMetadata":{"ios":{"bundle":"_expo/static/js/ios/entry-abc.hbc","assets":[{"path":"assets/1f2e","ext":"png"}]}}}`,
		"expoConfig.json":                   `{"name":"TeslaHelper","version":"0.3.0","scheme":["teslahelper","comgooglemaps"]}`,
		"_expo/static/js/ios/entry-abc.hbc": "BYTECODE",
		"assets/1f2e":                       "PNG",
	}
}

func otaUploadReq(t *testing.T, zipBytes []byte, fields map[string]string, token string) *http.Request {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, _ := mw.CreateFormFile("bundle_zip", "update.zip")
	_, _ = fw.Write(zipBytes)
	for k, v := range fields {
		_ = mw.WriteField(k, v)
	}
	_ = mw.Close()
	r := httptest.NewRequest("POST", "/api/ota/updates", &body)
	r.Header.Set("Content-Type", mw.FormDataContentType())
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	return r
}

func manifestReq(runtime, current string) *http.Request {
	r := httptest.NewRequest("GET", "/api/expo-updates/manifest", nil)
	r.Header.Set("expo-protocol-version", "1")
	r.Header.Set("expo-platform", "ios")
	r.Header.Set("expo-runtime-version", runtime)
	r.Header.Set("X-Forwarded-Proto", "https")
	r.Host = "j.example.net"
	if current != "" {
		r.Header.Set("expo-current-update-id", current)
	}
	return r
}

// multipartPart returns the named part's JSON from a manifest response.
func multipartPart(t *testing.T, w *httptest.ResponseRecorder, name string) map[string]any {
	t.Helper()
	_, params, err := mime.ParseMediaType(w.Header().Get("Content-Type"))
	if err != nil {
		t.Fatalf("content type %q: %v", w.Header().Get("Content-Type"), err)
	}
	mr := multipart.NewReader(w.Body, params["boundary"])
	for {
		p, err := mr.NextPart()
		if err == io.EOF {
			t.Fatalf("no %s part", name)
		}
		if err != nil {
			t.Fatal(err)
		}
		if p.FormName() == name {
			var v map[string]any
			if err := json.NewDecoder(p).Decode(&v); err != nil {
				t.Fatal(err)
			}
			return v
		}
	}
}

func TestOtaUploadPublishManifestAssetRollback(t *testing.T) {
	_, h, token, _ := teslaSetup(t)

	// Nothing published: 204 (app keeps its bundle).
	if w := serve(h, manifestReq("0.3.0", "")); w.Code != 204 {
		t.Fatalf("empty manifest %d %s", w.Code, w.Body)
	}
	// Management needs the token; the manifest does not.
	if w := serve(h, otaUploadReq(t, otaZip(t, goodExport()), map[string]string{"runtime_version": "0.3.0"}, "")); w.Code != 401 {
		t.Fatalf("unauthenticated upload %d", w.Code)
	}
	// A plain `expo export` (no expoConfig.json) is refused.
	bad := goodExport()
	delete(bad, "expoConfig.json")
	if w := serve(h, otaUploadReq(t, otaZip(t, bad), map[string]string{"runtime_version": "0.3.0"}, token)); w.Code != 400 || !strings.Contains(w.Body.String(), "expoConfig") {
		t.Fatalf("missing expoConfig %d %s", w.Code, w.Body)
	}
	// Path traversal in the zip is refused.
	evil := goodExport()
	evil["../escape.txt"] = "x"
	if w := serve(h, otaUploadReq(t, otaZip(t, evil), map[string]string{"runtime_version": "0.3.0"}, token)); w.Code != 400 {
		t.Fatalf("traversal zip %d %s", w.Code, w.Body)
	}

	w := serve(h, otaUploadReq(t, otaZip(t, goodExport()), map[string]string{"runtime_version": "0.3.0", "publish": "true", "notes": "AMZ numbers"}, token))
	if w.Code != 200 {
		t.Fatalf("upload %d %s", w.Code, w.Body)
	}
	var up otaInfo
	_ = json.Unmarshal(w.Body.Bytes(), &up)
	if up.Status != "published" || up.RuntimeVersion != "0.3.0" || len(up.Platforms) != 1 || up.Platforms[0] != "ios" {
		t.Fatalf("uploaded %+v", up)
	}

	// Another runtime version sees nothing.
	if w := serve(h, manifestReq("0.2.0", "")); w.Code != 204 {
		t.Fatalf("other runtime %d", w.Code)
	}
	w = serve(h, manifestReq("0.3.0", ""))
	if w.Code != 200 || w.Header().Get("expo-protocol-version") != "1" {
		t.Fatalf("manifest %d %v", w.Code, w.Header())
	}
	m := multipartPart(t, w, "manifest")
	if m["id"] != up.UpdateID || m["runtimeVersion"] != "0.3.0" {
		t.Fatalf("manifest %v", m)
	}
	if ca, _ := m["createdAt"].(string); !strings.HasSuffix(ca, "Z") || !strings.Contains(ca, "T") {
		t.Fatalf("createdAt %q must be ISO-8601 for iOS", ca)
	}
	launch := m["launchAsset"].(map[string]any)
	assetURL, _ := launch["url"].(string)
	if !strings.HasPrefix(assetURL, "https://j.example.net/api/expo-updates/assets?") {
		t.Fatalf("asset url %q", assetURL)
	}
	if extra := m["extra"].(map[string]any); extra["expoClient"] == nil {
		t.Fatal("expoClient missing")
	}
	// The launch asset downloads without a token.
	ar := httptest.NewRequest("GET", strings.TrimPrefix(assetURL, "https://j.example.net"), nil)
	if w := serve(h, ar); w.Code != 200 || w.Body.String() != "BYTECODE" {
		t.Fatalf("asset %d %q", w.Code, w.Body)
	}
	// Every asset carries fileExtension (iOS requires the key).
	for _, a := range m["assets"].([]any) {
		if _, ok := a.(map[string]any)["fileExtension"]; !ok {
			t.Fatalf("asset without fileExtension: %v", a)
		}
	}
	// Only listed files are served: not the update's own metadata or config,
	// and paths cannot escape it.
	for _, q := range []string{"../../data.db", "ota-info.json", "metadata.json", "expoConfig.json"} {
		r := httptest.NewRequest("GET", "/api/expo-updates/assets?updateId="+up.ID+"&asset="+q, nil)
		if w := serve(h, r); w.Code == 200 {
			t.Fatalf("asset %q served", q)
		}
	}
	// Already on it: noUpdateAvailable.
	w = serve(h, manifestReq("0.3.0", up.UpdateID))
	if d := multipartPart(t, w, "directive"); d["type"] != "noUpdateAvailable" {
		t.Fatalf("same id %v", d)
	}

	// Rollback: directive back to the embedded bundle.
	rb := httptest.NewRequest("POST", "/api/ota/updates/"+up.ID+"/rollback", nil)
	rb.Header.Set("Authorization", "Bearer "+token)
	if w := serve(h, rb); w.Code != 200 {
		t.Fatalf("rollback %d %s", w.Code, w.Body)
	}
	w = serve(h, manifestReq("0.3.0", up.UpdateID))
	if d := multipartPart(t, w, "directive"); d["type"] != "rollBackToEmbedded" {
		t.Fatalf("rollback directive %v", d)
	}

	// A second publish supersedes and mints a new id; list shows both.
	w = serve(h, otaUploadReq(t, otaZip(t, goodExport()), map[string]string{"runtime_version": "0.3.0", "publish": "1"}, token))
	var second otaInfo
	_ = json.Unmarshal(w.Body.Bytes(), &second)
	if second.UpdateID == up.UpdateID {
		t.Fatal("re-publish must mint a new update id")
	}
	lr := httptest.NewRequest("GET", "/api/ota/updates", nil)
	lr.Header.Set("Authorization", "Bearer "+token)
	w = serve(h, lr)
	var list []otaInfo
	_ = json.Unmarshal(w.Body.Bytes(), &list)
	active := 0
	for _, u := range list {
		if u.Status != "draft" {
			active++
		}
	}
	if len(list) != 2 || active != 1 {
		t.Fatalf("list %+v", list)
	}
	// Delete.
	dr := httptest.NewRequest("DELETE", "/api/ota/updates/"+second.ID, nil)
	dr.Header.Set("Authorization", "Bearer "+token)
	if w := serve(h, dr); w.Code != 200 {
		t.Fatalf("delete %d", w.Code)
	}
}

func serve(h http.Handler, r *http.Request) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

// TestOtaRealExport uploads a real `npm run ota` zip when
// OTA_REAL_ZIP points at one (skipped otherwise).
func TestOtaRealExport(t *testing.T) {
	zipPath := os.Getenv("OTA_REAL_ZIP")
	if zipPath == "" {
		t.Skip("set OTA_REAL_ZIP to a TeslaHelper export zip")
	}
	data, err := os.ReadFile(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	_, h, token, _ := teslaSetup(t)
	w := serve(h, otaUploadReq(t, data, map[string]string{"runtime_version": "0.3.0", "publish": "true"}, token))
	if w.Code != 200 {
		t.Fatalf("upload %d %s", w.Code, w.Body)
	}
	w = serve(h, manifestReq("0.3.0", ""))
	m := multipartPart(t, w, "manifest")
	launch := m["launchAsset"].(map[string]any)
	assets := m["assets"].([]any)
	ar := httptest.NewRequest("GET", strings.TrimPrefix(launch["url"].(string), "https://j.example.net"), nil)
	if w := serve(h, ar); w.Code != 200 || w.Body.Len() < 1000 {
		t.Fatalf("launch asset %d (%d bytes)", w.Code, w.Body.Len())
	}
	for _, a := range assets {
		u := a.(map[string]any)["url"].(string)
		if w := serve(h, httptest.NewRequest("GET", strings.TrimPrefix(u, "https://j.example.net"), nil)); w.Code != 200 {
			t.Fatalf("asset %s: %d", u, w.Code)
		}
	}
	t.Logf("manifest ok: %d assets, launch %s", len(assets), launch["contentType"])
}
