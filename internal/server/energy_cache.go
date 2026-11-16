package server

import (
	"bytes"
	"net/http"
	"sync"
	"time"
)

// Coalesce simultaneous dashboard reads and keep computed responses for 30s.
// No upstream calls or lifetime history scans run in the WebSocket path.
type energyReadCache struct {
	mu         sync.Mutex
	entries    map[string]*energyReadEntry
	lastTotals map[string][]byte
}
type energyReadEntry struct {
	ready   chan struct{}
	expires time.Time
	status  int
	header  http.Header
	body    []byte
}
type energyResponse struct {
	header http.Header
	bytes.Buffer
	status int
}

func (w *energyResponse) Header() http.Header    { return w.header }
func (w *energyResponse) WriteHeader(status int) { w.status = status }
func (w *energyResponse) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = 200
	}
	return w.Buffer.Write(b)
}

func (s *Server) energyRead(w http.ResponseWriter, r *http.Request) bool {
	sn := s.energySerial(r, nil)
	key := r.URL.Path + "?" + r.URL.Query().Encode() + "|" + sn
	cache := &s.energyReads
	cache.mu.Lock()
	if cache.entries == nil {
		cache.entries = map[string]*energyReadEntry{}
	}
	entry := cache.entries[key]
	leader := entry == nil || (!entry.expires.IsZero() && time.Now().After(entry.expires))
	if leader {
		for k, v := range cache.entries {
			if !v.expires.IsZero() && time.Now().After(v.expires) {
				delete(cache.entries, k)
			}
		}
		old := entry
		entry = &energyReadEntry{ready: make(chan struct{}), status: 500, header: make(http.Header)}
		if old != nil {
			entry.body = old.body
			entry.header = old.header
			entry.status = old.status
		}
		cache.entries[key] = entry
	}
	cache.mu.Unlock()
	if leader {
		published := false
		defer func() {
			if !published {
				close(entry.ready)
			}
		}()
		response := &energyResponse{header: make(http.Header)}
		// Resolve once; this also pins the viewed serial if selection changes.
		copy := r.Clone(r.Context())
		urlCopy := *r.URL
		copy.URL = &urlCopy
		query := copy.URL.Query()
		if sn != "" {
			query.Set("device_sn", sn)
		}
		copy.URL.RawQuery = query.Encode()
		s.energyReadNow(response, copy)
		cache.mu.Lock()
		entry.status, entry.header, entry.body = response.status, response.header, response.Bytes()
		entry.expires = time.Now().Add(30 * time.Second)
		if entry.status == http.StatusOK && r.URL.Path == "/api/energy/totals" {
			if cache.lastTotals == nil {
				cache.lastTotals = map[string][]byte{}
			}
			cache.lastTotals[sn] = entry.body
		}
		if entry.status != http.StatusOK || sn == "" {
			delete(cache.entries, key)
		}
		cache.mu.Unlock()
		close(entry.ready)
		published = true
		if entry.status == 200 && r.URL.Path == "/api/energy/totals" {
			s.publishEnergy(sn, entry.body)
		}
	} else {
		select {
		case <-entry.ready:
		case <-r.Context().Done():
			return true
		}
	}
	for key, values := range entry.header {
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}
	w.WriteHeader(entry.status)
	_, _ = w.Write(entry.body)
	return true
}

func (c *energyReadCache) invalidate() {
	c.mu.Lock()
	c.entries = nil
	c.mu.Unlock()
}
