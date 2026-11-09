package ble

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newSessions(t *testing.T, r Adapter) *Sessions {
	s := NewSessions(r, t.TempDir())
	s.Lock = NewAdapterLock()
	return s
}

func TestValidation(t *testing.T) {
	bodies := []string{
		`{"operation":"inspect"}`,
		`{"operation":"scan","seconds":0}`,
		`{"operation":"scan","notify":["uuid"]}`,
		`{"operation":"inspect","name":"a","address":"b"}`,
		`{"operation":"bogus"}`,
		`{"operation":"scan","seconds":121}`,
		`{"operation":"scan","duration":0.5}`,
		`{"operation":"scan","timeout":999}`,
		`{"operation":"inspect","name":""}`,
		`not json`,
	}
	for _, b := range bodies {
		_, err := ParseStartRequest(strings.NewReader(b))
		he, ok := err.(*HTTPError)
		if !ok || he.Status != 422 {
			t.Errorf("body %s → %v, want 422", b, err)
		}
	}
	o, err := ParseStartRequest(strings.NewReader(`{"operation":"inspect","address":"AA","notify":["2"]}`))
	if err != nil || o.Seconds != 10 || o.Duration != 30 || o.Timeout != 15 || o.Address != "AA" || len(o.Notify) != 1 {
		t.Fatalf("o=%+v err=%v", o, err)
	}
}

func TestExportsPaginationAndDelete(t *testing.T) {
	s := newSessions(t, newRadio(t))
	_ = os.MkdirAll(s.Root, 0o755)
	id := newUUID()
	path := filepath.Join(s.Root, id+".jsonl")
	cap, _ := NewCapture(path, map[string]any{}, nil)
	cap.Emit("read", Payload([]byte{0x00, 0xff}))
	cap.Emit("subscription_active", map[string]any{"uuid": "notify", "handle": 2,
		"subscription_properties": []string{"notify"}, "status": "active", "event_count": 0})
	cap.Emit("listening_ended", map[string]any{"reason": "duration_elapsed", "duration_s": 30, "elapsed_s": 30.01,
		"subscribed_count": 1, "failed_count": 0, "event_count": 0,
		"channels": []map[string]any{{"uuid": "notify", "handle": 2, "status": "active", "event_count": 0}}})
	cap.Finish("complete", nil)
	h := s.Handler()

	do := func(method, url string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(method, url, nil))
		return w
	}
	w := do("GET", "/api/ble/captures/"+id+"/events?after=1&limit=1")
	var ev EventsReply
	_ = json.Unmarshal(w.Body.Bytes(), &ev)
	if w.Code != 200 || ev.Next != 2 || len(ev.Events) != 1 || ev.Events[0]["hex"] != "00ff" {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	for _, format := range []string{"json", "jsonl"} {
		w := do("GET", "/api/ble/captures/"+id+"/download?format="+format)
		if w.Code != 200 {
			t.Fatalf("%s: %d", format, w.Code)
		}
		var data []map[string]any
		if format == "json" {
			var doc struct {
				Events []map[string]any `json:"events"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &doc); err != nil {
				t.Fatal(err)
			}
			data = doc.Events
		} else {
			for _, line := range strings.Split(strings.TrimSpace(w.Body.String()), "\n") {
				var m map[string]any
				_ = json.Unmarshal([]byte(line), &m)
				data = append(data, m)
			}
		}
		if data[len(data)-1]["status"] != "complete" {
			t.Fatalf("%s tail = %v", format, data[len(data)-1])
		}
		var sum map[string]any
		for _, e := range data {
			if e["type"] == "listening_ended" {
				sum = e
			}
		}
		if sum["subscribed_count"] != float64(1) || sum["event_count"] != float64(0) || sum["elapsed_s"] != 30.01 {
			t.Fatalf("%s summary = %v", format, sum)
		}
	}
	var st StatusReply
	_ = json.Unmarshal(do("GET", "/api/ble/status").Body.Bytes(), &st)
	if len(st.Captures) != 1 || st.Captures[0].Bytes <= 0 || st.Captures[0].Status != "complete" || !st.Available {
		t.Fatalf("status = %+v", st)
	}
	if do("DELETE", "/api/ble/captures/"+id).Code != 200 {
		t.Fatal("delete")
	}
	if do("GET", "/api/ble/captures/"+id+"/events").Code != 404 || do("GET", "/api/ble/captures/not-a-uuid/events").Code != 404 {
		t.Fatal("404s")
	}
	if do("GET", "/api/ble/captures/"+id+"/events?limit=9999").Code != 404 { // unknown id wins
		t.Fatal("unknown id must 404 first")
	}
}

func TestStartStopAndBusy(t *testing.T) {
	r := newRadio(t)
	s := newSessions(t, r)
	h := s.Handler()
	post := func(url, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("POST", url, strings.NewReader(body)))
		return w
	}
	w := post("/api/ble/sessions", `{"operation":"inspect","address":"AA","seconds":1,"duration":60,"notify":["notify"]}`)
	if w.Code != 200 {
		t.Fatalf("start: %d %s", w.Code, w.Body)
	}
	var out map[string]string
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	id := out["id"]
	if s.Active() != id {
		t.Fatalf("active = %q", s.Active())
	}
	// Second start while one is running → 409.
	if got := post("/api/ble/sessions", `{"operation":"scan"}`); got.Code != 409 {
		t.Fatalf("second start: %d", got.Code)
	}
	// Stopping while it scans: not yet listening, still cancels cleanly.
	if got := post("/api/ble/sessions/"+id+"/stop", ""); got.Code != 200 {
		t.Fatalf("stop: %d", got.Code)
	}
	if s.Active() != "" || s.Lock.Locked() {
		t.Fatalf("active=%q locked=%v", s.Active(), s.Lock.Locked())
	}
	rows, _ := Events(filepath.Join(s.Root, id+".jsonl"), 0, 0)
	tail := rows[len(rows)-1]
	if tail["type"] != "completion" || (tail["status"] != "cancelled" && tail["status"] != "complete" && tail["status"] != "incomplete") {
		t.Fatalf("tail = %v", tail)
	}
	// Download after stop works; delete removes it.
	w2 := httptest.NewRecorder()
	h.ServeHTTP(w2, httptest.NewRequest("GET", "/api/ble/captures/"+id+"/download", nil))
	if w2.Code != 200 {
		t.Fatalf("download: %d", w2.Code)
	}
	// Busy because the adapter lock is held elsewhere (BMS poll).
	s.Lock.TryAcquire()
	if _, err := s.Start(Options{Operation: "scan", Seconds: 1}); err != ErrBusy {
		t.Fatalf("err = %v", err)
	}
	s.Lock.Release()
}

func TestSessionRunsToCompletion(t *testing.T) {
	r := newRadio(t)
	s := newSessions(t, r)
	id, err := s.Start(Options{Operation: "scan", Seconds: 0.01})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for s.Active() != "" {
		if time.Now().After(deadline) {
			t.Fatal("capture never finished")
		}
		time.Sleep(2 * time.Millisecond)
	}
	for s.Lock.Locked() {
		time.Sleep(time.Millisecond)
	}
	st := s.Status()
	if st.Active != nil || len(st.Captures) != 1 || st.Captures[0].ID != id || st.Captures[0].Status != "complete" {
		t.Fatalf("%+v", st)
	}
	if err := s.Delete(id); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Path(id); err != ErrUnknownCapture {
		t.Fatalf("err = %v", err)
	}
}
