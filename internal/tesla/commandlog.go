package tesla

import (
	"sync"
	"time"
)

// CommandRecord is one command sent to the car, as it went out.
type CommandRecord struct {
	TS        int64  `json:"ts"` // unix milliseconds
	Command   string `json:"command"`
	Transport string `json:"transport"` // "signed" or "rest"
	// Endpoint is the Fleet API path (REST) or the signed-command domain.
	Endpoint string         `json:"endpoint"`
	Params   map[string]any `json:"params"`
	// PayloadHex is the serialized carserver.Action before signing and
	// encryption (signed transport only).
	PayloadHex string `json:"payload_hex,omitempty"`
	// Response is the car's reply: the Fleet API "response" JSON (REST) or
	// the decoded carserver.Response (signed).
	Response   string `json:"response,omitempty"`
	OK         bool   `json:"ok"`
	Error      string `json:"error,omitempty"`
	DurationMs int64  `json:"duration_ms"`
}

// DefaultCommandLogSize is how many records a CommandLog keeps.
const DefaultCommandLogSize = 100

// CommandLog keeps the most recent commands sent to the car, in memory.
type CommandLog struct {
	mu   sync.Mutex
	max  int
	recs []CommandRecord
}

// NewCommandLog returns a log keeping the last max records (0: default).
func NewCommandLog(max int) *CommandLog {
	if max <= 0 {
		max = DefaultCommandLogSize
	}
	return &CommandLog{max: max}
}

// Add records a command. A nil log ignores it.
func (l *CommandLog) Add(r CommandRecord) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.recs = append(l.recs, r)
	if over := len(l.recs) - l.max; over > 0 {
		l.recs = append([]CommandRecord(nil), l.recs[over:]...)
	}
}

// List returns up to limit records (0: all), newest first.
func (l *CommandLog) List(limit int) []CommandRecord {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := len(l.recs)
	if limit > 0 && limit < n {
		n = limit
	}
	out := make([]CommandRecord, 0, n)
	for i := len(l.recs) - 1; i >= 0 && len(out) < n; i-- {
		out = append(out, l.recs[i])
	}
	return out
}

// Clear removes every record.
func (l *CommandLog) Clear() {
	l.mu.Lock()
	l.recs = nil
	l.mu.Unlock()
}

func record(start time.Time, r CommandRecord, err error) CommandRecord {
	r.TS = start.UnixMilli()
	r.DurationMs = time.Since(start).Milliseconds()
	if err != nil {
		r.Error = err.Error()
	} else {
		r.OK = true
	}
	return r
}
