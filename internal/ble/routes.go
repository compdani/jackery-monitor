package ble

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// This file is the logic behind /api/ble/* (ble_routes.py) as plain funcs
// plus a stdlib http.Handler. Authentication is the caller's concern, as in
// the Python app (a global /api middleware).

// HTTPError carries the status code the Python route would raise.
type HTTPError struct {
	Status int
	Detail string
}

func (e *HTTPError) Error() string { return e.Detail }

var (
	// ErrBusy is returned by Start while the adapter is in use (409).
	ErrBusy = &HTTPError{409, "BLE adapter busy; stop capture or wait for BMS operation"}
	// ErrUnknownCapture is returned for unknown/invalid ids (404).
	ErrUnknownCapture = &HTTPError{404, "Unknown capture"}
)

// startRequest mirrors the pydantic StartRequest including defaults.
type startRequest struct {
	Operation      string            `json:"operation"`
	Seconds        *float64          `json:"seconds"`
	Name           *string           `json:"name"`
	Address        *string           `json:"address"`
	ReadAll        bool              `json:"read_all"`
	Notify         []string          `json:"notify"`
	NotifyExpected map[string]string `json:"notify_expected"`
	Duration       *float64          `json:"duration"`
	Timeout        *float64          `json:"timeout"`
}

func inRange(p *float64, def, lo, hi float64, name string) (float64, error) {
	if p == nil {
		return def, nil
	}
	if *p < lo || *p > hi {
		return 0, &HTTPError{422, fmt.Sprintf("%s must be between %g and %g", name, lo, hi)}
	}
	return *p, nil
}

// ParseStartRequest decodes and validates a POST /sessions body (422 on any
// violation: bad operation, ranges, name/address rules, scan with
// read_all/notify).
func ParseStartRequest(r io.Reader) (Options, error) {
	var req startRequest
	dec := json.NewDecoder(io.LimitReader(r, 1<<20))
	if err := dec.Decode(&req); err != nil {
		return Options{}, &HTTPError{422, "invalid JSON body: " + err.Error()}
	}
	return req.options()
}

func (req startRequest) options() (Options, error) {
	var o Options
	if req.Operation != "scan" && req.Operation != "inspect" {
		return o, &HTTPError{422, "operation must be 'scan' or 'inspect'"}
	}
	o.Operation = req.Operation
	var err error
	if o.Seconds, err = inRange(req.Seconds, 10, 1, 120, "seconds"); err != nil {
		return o, err
	}
	if o.Duration, err = inRange(req.Duration, 30, 1, 3600, "duration"); err != nil {
		return o, err
	}
	if o.Timeout, err = inRange(req.Timeout, 15, 1, 120, "timeout"); err != nil {
		return o, err
	}
	if req.Name != nil {
		if n := len(*req.Name); n < 1 || n > 248 {
			return o, &HTTPError{422, "name must be 1-248 characters"}
		}
		o.Name = *req.Name
	}
	if req.Address != nil {
		if n := len(*req.Address); n < 1 || n > 100 {
			return o, &HTTPError{422, "address must be 1-100 characters"}
		}
		o.Address = *req.Address
	}
	if len(req.Notify) > 100 {
		return o, &HTTPError{422, "notify has at most 100 entries"}
	}
	o.ReadAll = req.ReadAll
	o.Notify = req.Notify
	if o.Notify == nil {
		o.Notify = []string{}
	}
	o.NotifyExpected = req.NotifyExpected
	if o.NotifyExpected == nil {
		o.NotifyExpected = map[string]string{}
	}
	if o.Operation == "inspect" && (o.Name != "") == (o.Address != "") {
		return o, &HTTPError{422, "Inspect requires exactly one name or address"}
	}
	if o.Operation == "scan" && (o.ReadAll || len(o.Notify) > 0) {
		return o, &HTTPError{422, "Scan cannot read or subscribe"}
	}
	return o, nil
}

var captureIDRe = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func newUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// Sessions manages one capture at a time against the shared adapter lock.
type Sessions struct {
	// Root is the capture directory ($JACKERY_DATA_DIR/ble-captures).
	Root    string
	Adapter Adapter
	// Lock defaults to GlobalAdapterLock().
	Lock *AdapterLock

	mu     sync.Mutex
	cancel context.CancelFunc
	done   chan struct{}
	active string
}

// NewSessions returns a manager rooted at dataDir/ble-captures. An empty
// dataDir uses $JACKERY_DATA_DIR or /data like the Python app.
func NewSessions(adapter Adapter, dataDir string) *Sessions {
	if dataDir == "" {
		dataDir = os.Getenv("JACKERY_DATA_DIR")
	}
	if dataDir == "" {
		dataDir = "/data"
	}
	return &Sessions{Root: filepath.Join(dataDir, "ble-captures"), Adapter: adapter}
}

func (s *Sessions) lock() *AdapterLock {
	if s.Lock != nil {
		return s.Lock
	}
	return GlobalAdapterLock()
}

// Active returns the id of the running capture ("" when idle).
func (s *Sessions) Active() string { s.mu.Lock(); defer s.mu.Unlock(); return s.active }

// Path resolves a capture id to its file (404 for unknown/invalid).
func (s *Sessions) Path(id string) (string, error) {
	if !captureIDRe.MatchString(id) {
		return "", ErrUnknownCapture
	}
	p := filepath.Join(s.Root, id+".jsonl")
	if st, err := os.Stat(p); err != nil || st.IsDir() {
		return "", ErrUnknownCapture
	}
	return p, nil
}

// Start validates nothing (use ParseStartRequest) and launches a capture.
func (s *Sessions) Start(o Options) (string, error) {
	lock := s.lock()
	s.mu.Lock()
	running := s.done != nil
	if running {
		select {
		case <-s.done:
			running = false
		default:
		}
	}
	s.mu.Unlock()
	if running || lock.Locked() {
		return "", ErrBusy
	}
	if !lock.TryAcquire() {
		return "", ErrBusy
	}
	if err := os.MkdirAll(s.Root, 0o755); err != nil {
		lock.Release()
		return "", &HTTPError{503, fmt.Sprintf("Cannot create BLE capture: %v", err)}
	}
	id := newUUID()
	capt, err := NewCapture(filepath.Join(s.Root, id+".jsonl"), o, nil)
	if err != nil {
		lock.Release()
		return "", &HTTPError{503, fmt.Sprintf("Cannot create BLE capture: %v", err)}
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	s.mu.Lock()
	s.cancel, s.done, s.active = cancel, done, id
	s.mu.Unlock()
	go func() {
		defer func() {
			// Also covers cancellation before Run begins.
			if !capt.Closed() {
				m := "Stopped"
				capt.Finish("cancelled", &m)
			}
			s.mu.Lock()
			s.active = ""
			s.mu.Unlock()
			lock.Release()
			close(done)
		}()
		(&Inspector{Adapter: s.Adapter}).Run(ctx, capt, o)
	}()
	return id, nil
}

// Stop cancels the running capture and waits for its cleanup.
func (s *Sessions) Stop() {
	s.mu.Lock()
	cancel, done := s.cancel, s.done
	s.mu.Unlock()
	if cancel == nil || done == nil {
		return
	}
	cancel()
	<-done
}

// CaptureInfo is one row of the status listing.
type CaptureInfo struct {
	ID     string  `json:"id"`
	Bytes  int64   `json:"bytes"`
	Active bool    `json:"active"`
	Status string  `json:"status"`
	Error  *string `json:"error"`
}

// StatusReply is GET /status.
type StatusReply struct {
	Active    *string       `json:"active"`
	Captures  []CaptureInfo `json:"captures"`
	Available bool          `json:"available"`
	Error     *string       `json:"error"`
}

func lastEvent(path string, size int64) map[string]any {
	f, err := os.Open(path)
	if err != nil {
		return map[string]any{}
	}
	defer f.Close()
	off := size - 16384
	if off < 0 {
		off = 0
	}
	_, _ = f.Seek(off, io.SeekStart)
	data, _ := io.ReadAll(bufio.NewReader(f))
	lines := bytes.Split(bytes.TrimRight(data, "\n"), []byte("\n"))
	if len(lines) == 0 {
		return map[string]any{}
	}
	var last map[string]any
	if json.Unmarshal(lines[len(lines)-1], &last) != nil || last == nil {
		return map[string]any{}
	}
	return last
}

// Status lists captures (newest first) and BLE availability.
func (s *Sessions) Status() StatusReply {
	active := s.Active()
	reply := StatusReply{Captures: []CaptureInfo{}}
	if active != "" {
		reply.Active = &active
	}
	if entries, err := os.ReadDir(s.Root); err == nil {
		type row struct {
			info CaptureInfo
			mod  int64
		}
		var rows []row
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
				continue
			}
			st, err := e.Info()
			if err != nil {
				continue
			}
			id := strings.TrimSuffix(e.Name(), ".jsonl")
			path := filepath.Join(s.Root, e.Name())
			last := lastEvent(path, st.Size())
			isActive := id == active
			info := CaptureInfo{ID: id, Bytes: st.Size(), Active: isActive, Status: "running"}
			if !isActive {
				info.Status = "incomplete"
				if v, ok := last["status"].(string); ok {
					info.Status = v
				}
				if v, ok := last["error"].(string); ok {
					info.Error = &v
				}
			}
			rows = append(rows, row{info, st.ModTime().UnixNano()})
		}
		sort.SliceStable(rows, func(i, j int) bool { return rows[i].mod > rows[j].mod })
		for _, r := range rows {
			reply.Captures = append(reply.Captures, r.info)
		}
	}
	st := (&Poller{Adapter: s.Adapter}).Status()
	reply.Available, reply.Error = st.Available, st.Error
	return reply
}

// EventsReply is GET /captures/{id}/events.
type EventsReply struct {
	Events []map[string]any `json:"events"`
	Active bool             `json:"active"`
	Next   int              `json:"next"`
	Bytes  int64            `json:"bytes"`
}

// Events pages through a capture (limit 1..500, default 200).
func (s *Sessions) Events(id string, after, limit int) (EventsReply, error) {
	path, err := s.Path(id)
	if err != nil {
		return EventsReply{}, err
	}
	if limit < 1 || limit > 500 {
		return EventsReply{}, &HTTPError{422, "limit must be between 1 and 500"}
	}
	if after < 0 {
		return EventsReply{}, &HTTPError{422, "after must be >= 0"}
	}
	rows, err := Events(path, after, limit)
	if err != nil {
		return EventsReply{}, err
	}
	next := after
	if len(rows) > 0 {
		if seq, ok := rows[len(rows)-1]["seq"].(float64); ok {
			next = int(seq)
		}
	}
	st, _ := os.Stat(path)
	var size int64
	if st != nil {
		size = st.Size()
	}
	return EventsReply{Events: rows, Active: s.Active() == id, Next: next, Bytes: size}, nil
}

// CheckDownloadable returns the file path, or 409 while the capture runs.
func (s *Sessions) CheckDownloadable(id string) (string, error) {
	path, err := s.Path(id)
	if err != nil {
		return "", err
	}
	if s.Active() == id {
		return "", &HTTPError{409, "Stop capture before downloading"}
	}
	return path, nil
}

// Delete removes a stopped capture.
func (s *Sessions) Delete(id string) error {
	path, err := s.Path(id)
	if err != nil {
		return err
	}
	if s.Active() == id {
		return &HTTPError{409, "Stop capture before deleting"}
	}
	return os.Remove(path)
}

// ------------------------------------------------------------------ http

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, err error) {
	var he *HTTPError
	if errors.As(err, &he) {
		writeJSON(w, he.Status, map[string]string{"detail": he.Detail})
		return
	}
	writeJSON(w, 500, map[string]string{"detail": err.Error()})
}

func queryInt(r *http.Request, key string, def int) (int, error) {
	v := r.URL.Query().Get(key)
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, &HTTPError{422, key + " must be an integer"}
	}
	return n, nil
}

// Handler returns the /api/ble/* routes (mount it with
// http.Handle("/api/ble/", sessions.Handler()), or wrap it in your auth
// middleware first).
func (s *Sessions) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/ble/status", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, s.Status())
	})
	mux.HandleFunc("POST /api/ble/sessions", func(w http.ResponseWriter, r *http.Request) {
		o, err := ParseStartRequest(r.Body)
		if err != nil {
			writeErr(w, err)
			return
		}
		id, err := s.Start(o)
		if err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, 200, map[string]string{"id": id})
	})
	mux.HandleFunc("POST /api/ble/sessions/{id}/stop", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if _, err := s.Path(id); err != nil {
			writeErr(w, err)
			return
		}
		if s.Active() == id {
			s.Stop()
		}
		writeJSON(w, 200, map[string]bool{"stopped": true})
	})
	mux.HandleFunc("GET /api/ble/captures/{id}/events", func(w http.ResponseWriter, r *http.Request) {
		after, err := queryInt(r, "after", 0)
		if err == nil {
			var limit int
			limit, err = queryInt(r, "limit", 200)
			if err == nil {
				var reply EventsReply
				if reply, err = s.Events(r.PathValue("id"), after, limit); err == nil {
					writeJSON(w, 200, reply)
					return
				}
			}
		}
		writeErr(w, err)
	})
	mux.HandleFunc("GET /api/ble/captures/{id}/download", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		path, err := s.CheckDownloadable(id)
		if err != nil {
			writeErr(w, err)
			return
		}
		switch format := r.URL.Query().Get("format"); format {
		case "", "jsonl":
			w.Header().Set("Content-Type", "application/x-ndjson")
			w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filepath.Base(path)))
			http.ServeFile(w, r, path)
		case "json":
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s.json"`, id))
			_ = WriteJSONExport(w, path)
		default:
			writeErr(w, &HTTPError{422, "format must be json or jsonl"})
		}
	})
	mux.HandleFunc("DELETE /api/ble/captures/{id}", func(w http.ResponseWriter, r *http.Request) {
		if err := s.Delete(r.PathValue("id")); err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, 200, map[string]bool{"deleted": true})
	})
	return mux
}
