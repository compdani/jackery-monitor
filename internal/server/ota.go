package server

// Over-the-air JavaScript updates for the TeslaHelper app (Expo Updates
// protocol v0/v1), adapted from DREIA Members' cmd/ota.go.
//
// Each uploaded `expo export` lives unpacked in <data>/ota/<id>/ with an
// info.json describing it. Per runtime version at most one update is
// active: "published" (serve its bundle) or "rollback" (tell apps to go
// back to the bundle embedded in the binary). The manifest and asset
// endpoints are public, as expo-updates cannot send the app's token; the
// management endpoints need the usual session/app token.

import (
	"archive/zip"
	"bytes"
	"crypto/md5"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
)

const (
	otaMaxUploadBytes       = 30 << 20 // PocketBase's default body limit is 32 MB
	otaMaxUncompressedBytes = 120 << 20
	otaMaxZipFiles          = 5000
	otaStatusDraft          = "draft"
	otaStatusPublished      = "published"
	otaStatusRollback       = "rollback"
)

var (
	errOtaInvalidZip   = errors.New("invalid expo export zip")
	errOtaInvalidAsset = errors.New("invalid asset path")
	otaMu              sync.Mutex // serializes uploads and status changes
)

type otaExportMetadata struct {
	FileMetadata map[string]otaPlatformMetadata `json:"fileMetadata"`
}

type otaPlatformMetadata struct {
	Bundle string          `json:"bundle"`
	Assets []otaAssetEntry `json:"assets"`
}

type otaAssetEntry struct {
	Path string `json:"path"`
	Ext  string `json:"ext"`
}

type otaManifestAsset struct {
	Hash        string `json:"hash"`
	Key         string `json:"key"`
	ContentType string `json:"contentType"`
	// Always present: iOS expo-updates requires the key on every asset.
	FileExtension string `json:"fileExtension"`
	URL           string `json:"url"`
}

type otaManifest struct {
	ID             string             `json:"id"`
	CreatedAt      string             `json:"createdAt"`
	RuntimeVersion string             `json:"runtimeVersion"`
	LaunchAsset    otaManifestAsset   `json:"launchAsset"`
	Assets         []otaManifestAsset `json:"assets"`
	Metadata       map[string]string  `json:"metadata"`
	Extra          map[string]any     `json:"extra"`
}

// otaInfo is <data>/ota/<id>/info.json, and what the list endpoint returns.
type otaInfo struct {
	ID             string   `json:"id"`
	RuntimeVersion string   `json:"runtime_version"`
	Status         string   `json:"status"`
	Notes          string   `json:"notes"`
	UpdateID       string   `json:"update_id"`
	Created        string   `json:"created"`
	PublishedAt    string   `json:"published_at,omitempty"`
	Platforms      []string `json:"platforms"`
	Bytes          int64    `json:"bytes"`
}

const otaInfoFile = "ota-info.json"

func (s *Server) otaDir() string { return filepath.Join(s.Store.App.DataDir(), "ota") }

// otaPublic serves the two endpoints the app's expo-updates calls.
func (s *Server) otaPublic(w http.ResponseWriter, r *http.Request) bool {
	switch r.URL.Path {
	case "/api/expo-updates/manifest":
		if r.Method != http.MethodGet {
			fail(w, 405, "method not allowed")
			return true
		}
		s.otaServeManifest(w, r)
		return true
	case "/api/expo-updates/assets":
		if r.Method != http.MethodGet {
			fail(w, 405, "method not allowed")
			return true
		}
		s.otaServeAsset(w, r)
		return true
	}
	return false
}

// otaRoutes are the authenticated management endpoints:
//
//	GET    /api/ota/updates               list
//	POST   /api/ota/updates               upload (multipart: bundle_zip, runtime_version, notes, publish)
//	POST   /api/ota/updates/{id}/publish  make it the active update for its runtime
//	POST   /api/ota/updates/{id}/rollback tell apps on its runtime to use the embedded bundle
//	DELETE /api/ota/updates/{id}
func (s *Server) otaRoutes(w http.ResponseWriter, r *http.Request) bool {
	p := r.URL.Path
	if p != "/api/ota/updates" && !strings.HasPrefix(p, "/api/ota/updates/") {
		return false
	}
	if p == "/api/ota/updates" {
		switch r.Method {
		case http.MethodGet:
			items, err := s.otaList()
			if err != nil {
				fail(w, 500, "could not list updates")
				return true
			}
			jsonOut(w, 200, items)
		case http.MethodPost:
			s.otaUpload(w, r)
		default:
			fail(w, 405, "method not allowed")
		}
		return true
	}
	rest := strings.TrimPrefix(p, "/api/ota/updates/")
	id, action, _ := strings.Cut(rest, "/")
	if !validOtaID(id) {
		fail(w, 404, "update not found")
		return true
	}
	switch {
	case r.Method == http.MethodDelete && action == "":
		otaMu.Lock()
		defer otaMu.Unlock()
		dir := filepath.Join(s.otaDir(), id)
		if _, err := os.Stat(filepath.Join(dir, otaInfoFile)); err != nil {
			fail(w, 404, "update not found")
			return true
		}
		if err := os.RemoveAll(dir); err != nil {
			fail(w, 500, "could not delete update")
			return true
		}
		jsonOut(w, 200, map[string]any{"ok": true})
	case r.Method == http.MethodPost && (action == "publish" || action == "rollback"):
		status := otaStatusPublished
		if action == "rollback" {
			status = otaStatusRollback
		}
		info, err := s.otaActivate(id, status)
		if errors.Is(err, os.ErrNotExist) {
			fail(w, 404, "update not found")
			return true
		}
		if err != nil {
			fail(w, 500, "could not change the update")
			return true
		}
		jsonOut(w, 200, info)
	default:
		fail(w, 405, "method not allowed")
	}
	return true
}

func validOtaID(id string) bool {
	if len(id) != 16 {
		return false
	}
	for _, c := range id {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func (s *Server) otaReadInfo(id string) (otaInfo, error) {
	var info otaInfo
	raw, err := os.ReadFile(filepath.Join(s.otaDir(), id, otaInfoFile))
	if err != nil {
		return info, err
	}
	err = json.Unmarshal(raw, &info)
	return info, err
}

func (s *Server) otaWriteInfo(info otaInfo) error {
	raw, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(s.otaDir(), info.ID, otaInfoFile+".tmp")
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(s.otaDir(), info.ID, otaInfoFile))
}

// otaList returns every stored update, newest first.
func (s *Server) otaList() ([]otaInfo, error) {
	entries, err := os.ReadDir(s.otaDir())
	if errors.Is(err, os.ErrNotExist) {
		return []otaInfo{}, nil
	}
	if err != nil {
		return nil, err
	}
	items := []otaInfo{}
	for _, e := range entries {
		if !e.IsDir() || !validOtaID(e.Name()) {
			continue
		}
		if info, err := s.otaReadInfo(e.Name()); err == nil {
			items = append(items, info)
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Created > items[j].Created })
	return items, nil
}

func (s *Server) otaUpload(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, otaMaxUploadBytes)
	if err := r.ParseMultipartForm(8 << 20); err != nil {
		fail(w, 400, "upload must be a multipart form under 30 MB with bundle_zip")
		return
	}
	upload, _, err := r.FormFile("bundle_zip")
	if err != nil {
		fail(w, 400, "bundle_zip is required")
		return
	}
	defer upload.Close()
	runtimeVersion := sanitizeOtaRuntimeVersion(r.FormValue("runtime_version"))
	if runtimeVersion == "" {
		fail(w, 400, "runtime_version is required (the app version, e.g. 0.3.0)")
		return
	}
	notes := strings.TrimSpace(r.FormValue("notes"))
	if len(notes) > 2000 {
		notes = notes[:2000]
	}

	tmpZip, err := os.CreateTemp("", "ota-upload-*.zip")
	if err != nil {
		fail(w, 500, "could not store upload")
		return
	}
	defer os.Remove(tmpZip.Name())
	if _, err := io.Copy(tmpZip, upload); err != nil {
		tmpZip.Close()
		fail(w, 400, "could not read the zip")
		return
	}
	tmpZip.Close()

	tmpDir, err := os.MkdirTemp("", "ota-unpack-*")
	if err != nil {
		fail(w, 500, "could not unpack upload")
		return
	}
	defer os.RemoveAll(tmpDir)
	if err := unzipOtaExport(tmpZip.Name(), tmpDir); err != nil {
		fail(w, 400, err.Error())
		return
	}
	root, err := findOtaExportRoot(tmpDir)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	metaBytes, err := os.ReadFile(filepath.Join(root, "metadata.json"))
	if err != nil {
		fail(w, 400, "zip is missing metadata.json; upload the output of npm run ota")
		return
	}
	platforms, err := validateOtaExport(root, metaBytes)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}

	id := randomHex(8)
	info := otaInfo{
		ID:             id,
		RuntimeVersion: runtimeVersion,
		Status:         otaStatusDraft,
		Notes:          notes,
		UpdateID:       sha256HexToUUID(sha256Hex(metaBytes)),
		Created:        formatOtaISO8601(time.Now()),
		Platforms:      platforms,
		Bytes:          dirSize(root),
	}
	otaMu.Lock()
	dest := filepath.Join(s.otaDir(), id)
	if err := copyDir(root, dest); err != nil {
		otaMu.Unlock()
		_ = os.RemoveAll(dest)
		fail(w, 500, "could not store the update")
		return
	}
	err = s.otaWriteInfo(info)
	otaMu.Unlock()
	if err != nil {
		_ = os.RemoveAll(dest)
		fail(w, 500, "could not store the update")
		return
	}
	if parseOtaBool(r.FormValue("publish")) {
		if info, err = s.otaActivate(id, otaStatusPublished); err != nil {
			fail(w, 500, "uploaded, but publishing failed")
			return
		}
	}
	jsonOut(w, 200, info)
}

// otaActivate makes id the active update for its runtime (published or
// rollback) and returns any other active one for that runtime to draft.
// Each activation mints a new update ID and time, so apps always see it as
// newer (expo-updates only takes an update newer than what it runs).
func (s *Server) otaActivate(id, status string) (otaInfo, error) {
	otaMu.Lock()
	defer otaMu.Unlock()
	info, err := s.otaReadInfo(id)
	if err != nil {
		return info, err
	}
	// Write the new active one first: lookups take the newest published_at,
	// so phones never see a moment with no active update.
	info.Status = status
	info.PublishedAt = formatOtaISO8601(time.Now())
	info.UpdateID = newOtaUpdateID()
	if err := s.otaWriteInfo(info); err != nil {
		return info, err
	}
	all, err := s.otaList()
	if err != nil {
		return info, err
	}
	for _, other := range all {
		if other.ID != id && other.RuntimeVersion == info.RuntimeVersion && other.Status != otaStatusDraft {
			other.Status = otaStatusDraft
			if err := s.otaWriteInfo(other); err != nil {
				return info, err
			}
		}
	}
	return info, nil
}

// otaActive is the newest published/rollback update for a runtime, if any.
func (s *Server) otaActive(runtimeVersion string) (*otaInfo, error) {
	all, err := s.otaList()
	if err != nil {
		return nil, err
	}
	var best *otaInfo
	for i := range all {
		u := &all[i]
		if u.RuntimeVersion != runtimeVersion || u.Status == otaStatusDraft {
			continue
		}
		if best == nil || u.PublishedAt > best.PublishedAt {
			best = u
		}
	}
	return best, nil
}

func (s *Server) otaServeManifest(w http.ResponseWriter, r *http.Request) {
	protocolVersion, err := parseOtaProtocolVersion(firstHeaderOrQuery(r, "expo-protocol-version", "protocolVersion"))
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	platform := strings.ToLower(firstHeaderOrQuery(r, "expo-platform", "platform"))
	if platform != "ios" && platform != "android" {
		fail(w, 400, "unsupported platform; expected ios or android")
		return
	}
	runtimeVersion := firstHeaderOrQuery(r, "expo-runtime-version", "runtime-version")
	if runtimeVersion == "" {
		fail(w, 400, "no runtime version")
		return
	}
	currentID := strings.TrimSpace(r.Header.Get("expo-current-update-id"))
	embeddedID := strings.TrimSpace(r.Header.Get("expo-embedded-update-id"))

	info, err := s.otaActive(runtimeVersion)
	if err != nil {
		log.Printf("ota manifest lookup: %v", err)
		fail(w, 500, "could not look up updates")
		return
	}
	if info == nil {
		log.Printf("ota manifest platform=%s runtime=%s current=%s decision=none", platform, runtimeVersion, currentID)
		otaNoUpdate(w, protocolVersion)
		return
	}
	if info.Status == otaStatusRollback {
		if protocolVersion == 0 {
			fail(w, 400, "rollbacks need protocol version 1")
			return
		}
		if embeddedID != "" && strings.EqualFold(currentID, embeddedID) {
			writeOtaDirective(w, protocolVersion, map[string]any{"type": "noUpdateAvailable"})
			return
		}
		log.Printf("ota manifest platform=%s runtime=%s decision=rollback", platform, runtimeVersion)
		writeOtaDirective(w, protocolVersion, map[string]any{
			"type":       "rollBackToEmbedded",
			"parameters": map[string]any{"commitTime": info.PublishedAt},
		})
		return
	}

	root := filepath.Join(s.otaDir(), info.ID)
	metaBytes, err := os.ReadFile(filepath.Join(root, "metadata.json"))
	if err != nil {
		fail(w, 404, "update files are missing")
		return
	}
	var metadata otaExportMetadata
	if err := json.Unmarshal(metaBytes, &metadata); err != nil {
		fail(w, 500, "update metadata is invalid")
		return
	}
	pm, ok := metadata.FileMetadata[platform]
	if !ok || strings.TrimSpace(pm.Bundle) == "" {
		otaNoUpdate(w, protocolVersion)
		return
	}
	if protocolVersion == 1 && currentID != "" && strings.EqualFold(currentID, info.UpdateID) {
		writeOtaDirective(w, protocolVersion, map[string]any{"type": "noUpdateAvailable"})
		return
	}

	origin := requestOrigin(r)
	launch, err := otaFileToManifestAsset(root, pm.Bundle, "", true, origin, info.ID)
	if err != nil {
		fail(w, 500, "launch asset is missing")
		return
	}
	assets := make([]otaManifestAsset, 0, len(pm.Assets))
	for _, a := range pm.Assets {
		item, err := otaFileToManifestAsset(root, a.Path, a.Ext, false, origin, info.ID)
		if err != nil {
			fail(w, 500, "update asset is missing")
			return
		}
		assets = append(assets, item)
	}
	expoConfig, err := readJSONFile(filepath.Join(root, "expoConfig.json"))
	if err != nil {
		fail(w, 500, "update is missing expoConfig.json")
		return
	}
	body, err := json.Marshal(otaManifest{
		ID:             info.UpdateID,
		CreatedAt:      info.PublishedAt,
		RuntimeVersion: runtimeVersion,
		LaunchAsset:    launch,
		Assets:         assets,
		Metadata:       map[string]string{},
		Extra:          map[string]any{"scopeKey": otaScopeKey(origin), "expoClient": expoConfig},
	})
	if err != nil {
		fail(w, 500, "could not encode manifest")
		return
	}
	log.Printf("ota manifest platform=%s runtime=%s current=%s decision=manifest updateId=%s assets=%d", platform, runtimeVersion, currentID, info.UpdateID, len(assets))
	writeOtaMultipart(w, protocolVersion, "manifest", body)
}

func (s *Server) otaServeAsset(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.URL.Query().Get("updateId"))
	asset := strings.TrimSpace(r.URL.Query().Get("asset"))
	if !validOtaID(id) || asset == "" {
		fail(w, 400, "updateId and asset are required")
		return
	}
	root := filepath.Join(s.otaDir(), id)
	if _, err := os.Stat(filepath.Join(root, otaInfoFile)); err != nil {
		fail(w, 404, "update not found")
		return
	}
	// Only the bundle and assets the export lists are served (not its
	// metadata, config or anything else that was in the zip).
	listed, launch, ext := otaListedAsset(root, asset)
	if !listed {
		fail(w, 404, "asset not found")
		return
	}
	full, err := resolveOtaAsset(root, asset)
	if err != nil {
		fail(w, 400, "invalid asset path")
		return
	}
	st, err := os.Stat(full)
	if err != nil || st.IsDir() {
		fail(w, 404, "asset not found")
		return
	}
	w.Header().Set("Content-Type", assetContentType(ext, launch))
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	http.ServeFile(w, r, full)
}

// otaListedAsset reports whether asset is a bundle or asset listed in the
// update's metadata.json, whether it is a launch bundle, and its extension.
func otaListedAsset(root, asset string) (listed, launch bool, ext string) {
	raw, err := os.ReadFile(filepath.Join(root, "metadata.json"))
	if err != nil {
		return false, false, ""
	}
	var metadata otaExportMetadata
	if json.Unmarshal(raw, &metadata) != nil {
		return false, false, ""
	}
	want := path.Clean(strings.ReplaceAll(asset, "\\", "/"))
	for _, pm := range metadata.FileMetadata {
		if path.Clean(pm.Bundle) == want {
			return true, true, strings.TrimPrefix(path.Ext(want), ".")
		}
		for _, a := range pm.Assets {
			if path.Clean(a.Path) == want {
				return true, false, a.Ext
			}
		}
	}
	return false, false, ""
}

// otaNoUpdate answers "nothing for you": 204 on protocol 1, 404 on 0.
func otaNoUpdate(w http.ResponseWriter, protocolVersion int) {
	setOtaManifestHeaders(w, protocolVersion)
	if protocolVersion == 0 {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---------------------------------------------------------------- export files

func unzipOtaExport(zipPath, dest string) error {
	reader, err := zip.OpenReader(zipPath)
	if err != nil {
		return fmt.Errorf("%w: not a zip file", errOtaInvalidZip)
	}
	defer reader.Close()
	if len(reader.File) > otaMaxZipFiles {
		return fmt.Errorf("%w: too many files", errOtaInvalidZip)
	}
	var written int64 // actual bytes written; zip headers are not trusted
	for _, f := range reader.File {
		name := filepath.ToSlash(f.Name)
		if base := path.Base(name); base == ".DS_Store" || strings.HasPrefix(name, "__MACOSX/") || strings.Contains(name, "/__MACOSX/") {
			continue
		}
		target, err := resolveOtaAsset(dest, name)
		if err != nil {
			return fmt.Errorf("%w: illegal path %q", errOtaInvalidZip, f.Name)
		}
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			continue
		}
		if f.UncompressedSize64 > otaMaxUncompressedBytes {
			return fmt.Errorf("%w: too large when unpacked", errOtaInvalidZip)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		src, err := f.Open()
		if err != nil {
			return err
		}
		out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
		if err != nil {
			src.Close()
			return err
		}
		n, err := io.Copy(out, io.LimitReader(src, otaMaxUncompressedBytes-written+1))
		out.Close()
		src.Close()
		if err != nil {
			return err
		}
		written += n
		if written > otaMaxUncompressedBytes {
			return fmt.Errorf("%w: too large when unpacked", errOtaInvalidZip)
		}
	}
	return nil
}

func findOtaExportRoot(unpacked string) (string, error) {
	var found []string
	err := filepath.Walk(unpacked, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			if b := info.Name(); b == "__MACOSX" || (strings.HasPrefix(b, ".") && p != unpacked) {
				return filepath.SkipDir
			}
			return nil
		}
		if info.Name() == "metadata.json" {
			found = append(found, filepath.Dir(p))
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if len(found) == 0 {
		return "", errors.New("zip is missing metadata.json; upload the output of npm run ota")
	}
	sort.Slice(found, func(i, j int) bool { return len(found[i]) < len(found[j]) })
	return found[0], nil
}

// validateOtaExport checks the export is complete and safe and returns its
// platforms. An iOS-only export is fine (the app is iPhone-only).
func validateOtaExport(root string, metaBytes []byte) ([]string, error) {
	var metadata otaExportMetadata
	if err := json.Unmarshal(metaBytes, &metadata); err != nil {
		return nil, errors.New("metadata.json is not valid JSON")
	}
	raw, err := os.ReadFile(filepath.Join(root, "expoConfig.json"))
	if err != nil {
		return nil, errors.New("zip is missing expoConfig.json; use npm run ota (expo export alone is not enough)")
	}
	var expoConfig map[string]any
	if err := json.Unmarshal(raw, &expoConfig); err != nil {
		return nil, errors.New("expoConfig.json is not valid JSON")
	}
	if !hasOtaScheme(expoConfig["scheme"]) {
		return nil, errors.New("expoConfig.json is missing scheme (expo-linking needs it under OTA)")
	}
	var platforms []string
	for _, platform := range []string{"ios", "android"} {
		meta, ok := metadata.FileMetadata[platform]
		if !ok {
			continue
		}
		if strings.TrimSpace(meta.Bundle) == "" {
			return nil, fmt.Errorf("metadata.json has no %s bundle", platform)
		}
		files := []string{meta.Bundle}
		for _, a := range meta.Assets {
			if strings.TrimSpace(a.Path) != "" {
				files = append(files, a.Path)
			}
		}
		for _, f := range files {
			full, err := resolveOtaAsset(root, f)
			if err != nil {
				return nil, fmt.Errorf("illegal path %s", f)
			}
			if _, err := os.Stat(full); err != nil {
				return nil, fmt.Errorf("zip is missing %s", f)
			}
		}
		platforms = append(platforms, platform)
	}
	if len(platforms) == 0 {
		return nil, errors.New("metadata.json has no ios or android bundle")
	}
	return platforms, nil
}

// The app config may list several schemes; any non-empty one will do.
func hasOtaScheme(v any) bool {
	switch s := v.(type) {
	case string:
		return strings.TrimSpace(s) != ""
	case []any:
		for _, x := range s {
			if str, ok := x.(string); ok && strings.TrimSpace(str) != "" {
				return true
			}
		}
	}
	return false
}

func resolveOtaAsset(root, asset string) (string, error) {
	trimmed := strings.TrimSpace(asset)
	if trimmed == "" {
		return "", errOtaInvalidAsset
	}
	normalized := strings.ReplaceAll(trimmed, "\\", "/")
	if path.IsAbs(normalized) || filepath.IsAbs(trimmed) {
		return "", errOtaInvalidAsset
	}
	for _, part := range strings.Split(normalized, "/") {
		if part == ".." {
			return "", errOtaInvalidAsset
		}
	}
	rel := strings.TrimPrefix(path.Clean("/"+normalized), "/")
	if rel == "" || rel == "." {
		return "", errOtaInvalidAsset
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	absFull, err := filepath.Abs(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		return "", err
	}
	if absFull != absRoot && !strings.HasPrefix(absFull, absRoot+string(os.PathSeparator)) {
		return "", errOtaInvalidAsset
	}
	return absFull, nil
}

func otaFileToManifestAsset(root, filePath, ext string, isLaunch bool, origin, id string) (otaManifestAsset, error) {
	full, err := resolveOtaAsset(root, filePath)
	if err != nil {
		return otaManifestAsset{}, err
	}
	data, err := os.ReadFile(full)
	if err != nil {
		return otaManifestAsset{}, err
	}
	if ext == "" {
		ext = strings.TrimPrefix(filepath.Ext(full), ".")
	}
	sum := sha256.Sum256(data)
	q := url.Values{"updateId": {id}, "asset": {filepath.ToSlash(filePath)}}
	a := otaManifestAsset{
		Hash:        base64.RawURLEncoding.EncodeToString(sum[:]),
		Key:         fmt.Sprintf("%x", md5.Sum(data)),
		ContentType: assetContentType(ext, isLaunch),
		URL:         strings.TrimRight(origin, "/") + "/api/expo-updates/assets?" + q.Encode(),
	}
	if isLaunch {
		a.FileExtension = ".bundle"
	} else if ext != "" {
		a.FileExtension = "." + strings.TrimPrefix(ext, ".")
	}
	return a, nil
}

func assetContentType(ext string, isLaunch bool) string {
	if isLaunch {
		return "application/javascript"
	}
	switch strings.ToLower(strings.TrimPrefix(ext, ".")) {
	case "png":
		return "image/png"
	case "jpg", "jpeg":
		return "image/jpeg"
	case "gif":
		return "image/gif"
	case "webp":
		return "image/webp"
	case "svg":
		return "image/svg+xml"
	case "ttf":
		return "font/ttf"
	case "otf":
		return "font/otf"
	case "json":
		return "application/json"
	case "js", "hbc":
		return "application/javascript"
	}
	if t := mime.TypeByExtension("." + ext); t != "" {
		return t
	}
	return "application/octet-stream"
}

// ---------------------------------------------------------------- protocol

func writeOtaDirective(w http.ResponseWriter, protocolVersion int, directive map[string]any) {
	body, _ := json.Marshal(directive)
	writeOtaMultipart(w, protocolVersion, "directive", body)
}

func writeOtaMultipart(w http.ResponseWriter, protocolVersion int, part string, body []byte) {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	h := make(textproto.MIMEHeader)
	h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="%s"`, part))
	h.Set("Content-Type", "application/json; charset=utf-8")
	pw, err := mw.CreatePart(h)
	if err == nil {
		_, err = pw.Write(body)
	}
	if err == nil {
		err = mw.Close()
	}
	if err != nil {
		fail(w, 500, "could not encode response")
		return
	}
	setOtaManifestHeaders(w, protocolVersion)
	w.Header().Set("Content-Type", "multipart/mixed; boundary="+mw.Boundary())
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(buf.Bytes())
}

func setOtaManifestHeaders(w http.ResponseWriter, protocolVersion int) {
	w.Header().Set("expo-protocol-version", fmt.Sprintf("%d", protocolVersion))
	w.Header().Set("expo-sfv-version", "0")
	w.Header().Set("cache-control", "private, max-age=0")
}

func parseOtaProtocolVersion(raw string) (int, error) {
	switch strings.TrimSpace(raw) {
	case "", "1":
		return 1, nil
	case "0":
		return 0, nil
	}
	return 0, errors.New("unsupported protocol version; expected 0 or 1")
}

func firstHeaderOrQuery(r *http.Request, header, query string) string {
	if v := strings.TrimSpace(r.Header.Get(header)); v != "" {
		return v
	}
	return strings.TrimSpace(r.URL.Query().Get(query))
}

// requestOrigin is the public origin the phone used (behind the tunnel the
// forwarded headers carry it).
func requestOrigin(r *http.Request) string {
	first := func(v string) string {
		v, _, _ = strings.Cut(v, ",")
		return strings.TrimSpace(v)
	}
	proto := strings.ToLower(first(r.Header.Get("X-Forwarded-Proto")))
	if proto != "http" && proto != "https" {
		proto = "http"
		if r.TLS != nil {
			proto = "https"
		}
	}
	host := first(r.Header.Get("X-Forwarded-Host"))
	if host == "" || strings.ContainsAny(host, " /\\@?#") {
		host = r.Host
	}
	return proto + "://" + host
}

func otaScopeKey(origin string) string {
	u, err := url.Parse(strings.TrimSpace(origin))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return strings.TrimRight(strings.TrimSpace(origin), "/")
	}
	return u.Scheme + "://" + u.Host
}

func sanitizeOtaRuntimeVersion(v string) string {
	var b strings.Builder
	for _, r := range strings.TrimSpace(v) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '.' || r == '-' || r == '_' {
			b.WriteRune(r)
		}
	}
	out := b.String()
	if len(out) > 64 {
		out = out[:64]
	}
	return out
}

func parseOtaBool(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "on", "yes":
		return true
	}
	return false
}

// iOS parses createdAt as YYYY-MM-DDTHH:mm:ss.sssZ; anything else crashes it.
func formatOtaISO8601(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func sha256HexToUUID(h string) string {
	if len(h) < 32 {
		h += strings.Repeat("0", 32-len(h))
	}
	return fmt.Sprintf("%s-%s-%s-%s-%s", h[0:8], h[8:12], h[12:16], h[16:20], h[20:32])
}

func newOtaUpdateID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return sha256HexToUUID(sha256Hex([]byte(time.Now().UTC().Format(time.RFC3339Nano))))
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func readJSONFile(p string) (any, error) {
	data, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	var v any
	return v, json.Unmarshal(data, &v)
}

func copyDir(src, dest string) error {
	return filepath.Walk(src, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dest, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		in, err := os.Open(p)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
		if err != nil {
			return err
		}
		defer out.Close()
		_, err = io.Copy(out, in)
		return err
	})
}

func dirSize(root string) int64 {
	var n int64
	_ = filepath.Walk(root, func(_ string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			n += info.Size()
		}
		return nil
	})
	return n
}
