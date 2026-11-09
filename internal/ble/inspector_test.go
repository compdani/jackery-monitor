package ble

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeRadio is a hardware-free Adapter that records calls and forbids
// controller writes.
type fakeRadio struct {
	t       *testing.T
	mu      sync.Mutex
	calls   []string
	devices []Advertisement
	chars   []Characteristic

	// overridable behaviours
	connect     func(ctx context.Context) error
	read        func(ctx context.Context, c Characteristic) ([]byte, error)
	startNotify func(r *fakeRadio, ctx context.Context, c Characteristic, cb NotifyFunc) error
	stopNotify  func(r *fakeRadio, ctx context.Context, c Characteristic) error
	scanStart   func(r *fakeRadio, cb func(Advertisement))

	disc chan struct{}
}

func newRadio(t *testing.T) *fakeRadio {
	return &fakeRadio{
		t: t,
		chars: []Characteristic{
			{UUID: "read", Handle: 1, Properties: []string{"read"}},
			{UUID: "notify", Handle: 2, Properties: []string{"notify"}},
			{UUID: "write", Handle: 3, Properties: []string{"write"}},
			{UUID: "bad-read", Handle: 4, Properties: []string{"read"}},
		},
		devices: []Advertisement{{
			Address: "AA", RSSI: 0,
			ManufacturerData: map[uint16][]byte{1: {0x00, 0xff}},
			ServiceData:      map[string][]byte{"unknown": {}},
		}},
		disc: make(chan struct{}),
	}
}

func (r *fakeRadio) record(s string) { r.mu.Lock(); r.calls = append(r.calls, s); r.mu.Unlock() }
func (r *fakeRadio) has(s string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, c := range r.calls {
		if c == s {
			return true
		}
	}
	return false
}
func (r *fakeRadio) last() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls[len(r.calls)-1]
}
func (r *fakeRadio) index(s string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i, c := range r.calls {
		if c == s {
			return i
		}
	}
	return -1
}
func (r *fakeRadio) anyPrefix(p string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, c := range r.calls {
		if strings.HasPrefix(c, p) {
			return true
		}
	}
	return false
}

func (r *fakeRadio) Available() error { return nil }

func (r *fakeRadio) Scan(ctx context.Context, onAdv func(Advertisement)) error {
	if r.scanStart != nil {
		r.scanStart(r, onAdv)
	} else {
		for _, d := range r.devices {
			onAdv(d)
			onAdv(d)
		}
	}
	<-ctx.Done()
	r.record("scanner_stop")
	return nil
}

func (r *fakeRadio) Connect(ctx context.Context, address string) (Conn, error) {
	if r.connect != nil {
		if err := r.connect(ctx); err != nil {
			r.record("disconnect") // adapter contract: no half-open link
			return nil, err
		}
	}
	r.record("connect")
	return &fakeConn{r: r}, nil
}

type fakeConn struct{ r *fakeRadio }

func (c *fakeConn) Services() []Service {
	return []Service{{UUID: "service", Handle: 0, Characteristics: c.r.chars}}
}
func (c *fakeConn) Read(ctx context.Context, ch Characteristic) ([]byte, error) {
	c.r.record(fmt.Sprintf("read:%d", ch.Handle))
	if c.r.read != nil {
		return c.r.read(ctx, ch)
	}
	if ch.Handle == 4 {
		return nil, errors.New("unreadable now")
	}
	return []byte("\x00\xffhello"), nil
}
func (c *fakeConn) StartNotify(ctx context.Context, ch Characteristic, cb NotifyFunc) error {
	c.r.record(fmt.Sprintf("notify:%d", ch.Handle))
	if c.r.startNotify != nil {
		return c.r.startNotify(c.r, ctx, ch, cb)
	}
	cb(ch, []byte{0xff, 0x00})
	cb(ch, nil)
	return nil
}
func (c *fakeConn) StopNotify(ctx context.Context, ch Characteristic) error {
	if c.r.stopNotify != nil {
		if err := c.r.stopNotify(c.r, ctx, ch); err != nil {
			c.r.record(fmt.Sprintf("stop_notify:%d", ch.Handle))
			return err
		}
	}
	c.r.record(fmt.Sprintf("stop_notify:%d", ch.Handle))
	return nil
}
func (c *fakeConn) Write(context.Context, Characteristic, []byte, bool) error {
	c.r.t.Error("forbidden write")
	return errors.New("forbidden write")
}
func (c *fakeConn) Disconnect(context.Context) error { c.r.record("disconnect"); return nil }
func (c *fakeConn) Disconnected() <-chan struct{}    { return c.r.disc }

func blockUntilDone(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }

func execute(t *testing.T, r *fakeRadio, mut func(*Options)) (string, []map[string]any) {
	t.Helper()
	o := Options{Operation: "inspect", Address: "AA", Seconds: 0.001, Duration: 0.001}
	if mut != nil {
		mut(&o)
	}
	path := filepath.Join(t.TempDir(), "capture.jsonl")
	cap, err := NewCapture(path, o, nil)
	if err != nil {
		t.Fatal(err)
	}
	status := (&Inspector{Adapter: r}).Run(context.Background(), cap, o)
	rows, err := Events(path, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	return status, rows
}

func ofType(rows []map[string]any, kind string) []map[string]any {
	var out []map[string]any
	for _, r := range rows {
		if r["type"] == kind {
			out = append(out, r)
		}
	}
	return out
}

func listening(t *testing.T, rows []map[string]any) map[string]any {
	t.Helper()
	l := ofType(rows, "listening_ended")
	if len(l) == 0 {
		t.Fatalf("no listening_ended in %v", rows)
	}
	return l[0]
}

func channels(sum map[string]any) []map[string]any {
	var out []map[string]any
	for _, c := range sum["channels"].([]any) {
		out = append(out, c.(map[string]any))
	}
	return out
}

func num(v any) float64 { f, _ := v.(float64); return f }

func TestStructureOnlyPreservesAdvertisements(t *testing.T) {
	r := newRadio(t)
	status, rows := execute(t, r, nil)
	if status != "complete" {
		t.Fatalf("status = %s rows=%v", status, rows)
	}
	adv := ofType(rows, "advertisement")
	if len(adv) != 2 || adv[0]["name"] != nil || num(adv[0]["rssi"]) != 0 {
		t.Fatalf("adv = %v", adv)
	}
	md := adv[0]["manufacturer_data"].(map[string]any)["1"].(map[string]any)
	if md["hex"] != "00ff" {
		t.Fatalf("md = %v", md)
	}
	sd := adv[0]["service_data"].(map[string]any)["unknown"].(map[string]any)
	if num(sd["length"]) != 0 {
		t.Fatalf("sd = %v", sd)
	}
	if r.anyPrefix("read:") || r.anyPrefix("notify:") || r.last() != "disconnect" {
		t.Fatalf("calls = %v", r.calls)
	}
	for i, row := range rows {
		if int(num(row["seq"])) != i+1 {
			t.Fatalf("seq gap at %d: %v", i, row["seq"])
		}
	}
}

func TestReadAndExplicitNotifications(t *testing.T) {
	r := newRadio(t)
	status, rows := execute(t, r, func(o *Options) { o.ReadAll = true; o.Notify = []string{"notify"} })
	if status != "complete" {
		t.Fatalf("status = %s", status)
	}
	if !r.has("read:1") || !r.has("read:4") || r.index("read:4") > r.index("notify:2") {
		t.Fatalf("calls = %v", r.calls)
	}
	if len(ofType(rows, "read_error")) == 0 {
		t.Fatal("expected read_error")
	}
	notes := ofType(rows, "notification")
	if len(notes) != 2 || notes[0]["hex"] != "ff00" || notes[1]["hex"] != "" {
		t.Fatalf("notes = %v", notes)
	}
	if !r.has("stop_notify:2") {
		t.Fatal("stop_notify missing")
	}
}

func TestDuplicateAndUnsupportedSelectors(t *testing.T) {
	r := newRadio(t)
	if _, err := SelectNotifications(r.chars, []string{"write"}); err == nil {
		t.Fatal("write is not notifiable")
	}
	chars := append(append([]Characteristic(nil), r.chars...), Characteristic{UUID: "notify", Handle: 5, Properties: []string{"indicate"}})
	if _, err := SelectNotifications(chars, []string{"notify"}); err == nil {
		t.Fatal("ambiguous uuid must fail")
	}
	got, err := SelectNotifications(chars, []string{"0x5"})
	if err != nil || len(got) != 1 || got[0].Handle != 5 {
		t.Fatalf("got=%v err=%v", got, err)
	}
	got, _ = SelectNotifications(chars, []string{"2", "2"})
	if len(got) != 1 {
		t.Fatalf("duplicates must collapse: %v", got)
	}
}

func TestAmbiguousName(t *testing.T) {
	r := newRadio(t)
	r.devices[0].LocalName = "SILELI"
	r.devices = append(r.devices, Advertisement{Address: "BB", Name: "sileli", LocalName: "SILELI"})
	status, rows := execute(t, r, func(o *Options) { o.Address = ""; o.Name = "sile" })
	last := rows[len(rows)-1]
	errStr, _ := ofType(rows, "error")[0]["error"].(string)
	if status != "incomplete" || !strings.Contains(errStr, "Candidates") || !strings.Contains(errStr, "BB") || r.has("connect") {
		t.Fatalf("status=%s last=%v calls=%v", status, last, r.calls)
	}
}

func TestNoMatch(t *testing.T) {
	r := newRadio(t)
	status, rows := execute(t, r, func(o *Options) { o.Address = "missing" })
	errStr, _ := ofType(rows, "error")[0]["error"].(string)
	if status != "incomplete" || !strings.Contains(errStr, "found 0") {
		t.Fatalf("status=%s err=%q", status, errStr)
	}
}

func TestCancellationFlushesAndDisconnects(t *testing.T) {
	r := newRadio(t)
	path := filepath.Join(t.TempDir(), "partial.jsonl")
	o := Options{Operation: "inspect", Address: "AA", Seconds: 0.001, Notify: []string{"notify"}, Duration: 100}
	cap, _ := NewCapture(path, o, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan string, 1)
	go func() { done <- (&Inspector{Adapter: r}).Run(ctx, cap, o) }()
	deadline := time.Now().Add(2 * time.Second)
	for {
		rows, _ := Events(path, 0, 0)
		if len(ofType(rows, "listening_started")) > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("never started listening")
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	if got := <-done; got != "cancelled" {
		t.Fatalf("status = %s", got)
	}
	rows, _ := Events(path, 0, 0)
	sum := listening(t, rows)
	if rows[len(rows)-1]["status"] != "cancelled" || sum["reason"] != "cancelled" || num(sum["subscribed_count"]) != 1 {
		t.Fatalf("rows tail=%v sum=%v", rows[len(rows)-1], sum)
	}
	if r.last() != "disconnect" || !cap.Closed() {
		t.Fatalf("calls = %v closed=%v", r.calls, cap.Closed())
	}
}

func TestDisconnect(t *testing.T) {
	r := newRadio(t)
	r.startNotify = func(r *fakeRadio, ctx context.Context, c Characteristic, cb NotifyFunc) error {
		close(r.disc)
		return nil
	}
	status, rows := execute(t, r, func(o *Options) { o.Notify = []string{"notify"} })
	errStr, _ := ofType(rows, "error")[0]["error"].(string)
	if status != "incomplete" || !strings.Contains(errStr, "disconnected") || listening(t, rows)["reason"] != "disconnected" {
		t.Fatalf("status=%s err=%q sum=%v", status, errStr, listening(t, rows))
	}
}

func TestReadTimeout(t *testing.T) {
	r := newRadio(t)
	r.read = func(ctx context.Context, c Characteristic) ([]byte, error) { return nil, blockUntilDone(ctx) }
	status, rows := execute(t, r, func(o *Options) { o.ReadAll = true; o.Timeout = 0.001 })
	if status != "complete" || len(ofType(rows, "read_error")) != 2 {
		t.Fatalf("status=%s read_errors=%d", status, len(ofType(rows, "read_error")))
	}
	if ofType(rows, "read_error")[0]["error"] != "TimeoutError" {
		t.Fatalf("%v", ofType(rows, "read_error")[0])
	}
}

func TestLimitAndPagination(t *testing.T) {
	path := filepath.Join(t.TempDir(), "limited.jsonl")
	cap, _ := NewCapture(path, map[string]any{}, nil)
	cap.Emit("read", Payload([]byte{0x00, 0xff}))
	ev, _ := Events(path, 1, 1)
	if len(ev) != 1 || ev[0]["hex"] != "00ff" {
		t.Fatalf("ev = %v", ev)
	}
	defer SetMaxBytes(cap.Size() + 4100)()
	if err := cap.Emit("advertisement", map[string]any{"extra": strings.Repeat("x", 100)}); err == nil {
		t.Fatal("expected size-limit error")
	}
	m := cap.Failure()
	cap.Finish("incomplete", &m)
	all, _ := Events(path, 0, 0)
	if all[len(all)-1]["status"] != "incomplete" {
		t.Fatalf("tail = %v", all[len(all)-1])
	}
	select {
	case <-cap.Failed():
	default:
		t.Fatal("failed channel must be closed")
	}
}

func TestChangingAdvertisements(t *testing.T) {
	r := newRadio(t)
	r.scanStart = func(r *fakeRadio, cb func(Advertisement)) {
		d := r.devices[0]
		cb(d)
		d.ManufacturerData = map[uint16][]byte{1: {0x02}}
		d.LocalName = "new name"
		cb(d)
	}
	status, rows := execute(t, r, func(o *Options) { o.Operation = "scan" })
	adv := ofType(rows, "advertisement")
	if status != "complete" || len(adv) != 2 {
		t.Fatalf("status=%s adv=%d", status, len(adv))
	}
	hexes := []string{}
	for _, a := range adv {
		hexes = append(hexes, a["manufacturer_data"].(map[string]any)["1"].(map[string]any)["hex"].(string))
	}
	if hexes[0] != "00ff" || hexes[1] != "02" {
		t.Fatalf("hexes = %v", hexes)
	}
	summary := rows[len(rows)-2]
	dev := summary["devices"].([]any)[0].(map[string]any)
	if dev["name"] != "new name" {
		t.Fatalf("summary = %v", summary)
	}
}

func TestConnectTimeoutDisconnects(t *testing.T) {
	r := newRadio(t)
	r.connect = blockUntilDone
	status, _ := execute(t, r, func(o *Options) { o.Timeout = 0.001 })
	if status != "incomplete" || r.last() != "disconnect" {
		t.Fatalf("status=%s calls=%v", status, r.calls)
	}
}

func TestChangedHandleRejected(t *testing.T) {
	r := newRadio(t)
	status, rows := execute(t, r, func(o *Options) {
		o.Notify = []string{"2"}
		o.NotifyExpected = map[string]string{"2": "old-uuid"}
	})
	errStr, _ := ofType(rows, "error")[0]["error"].(string)
	if status != "incomplete" || !strings.Contains(errStr, "structure changed") || r.has("notify:2") {
		t.Fatalf("status=%s err=%q calls=%v", status, errStr, r.calls)
	}
}

type failWriter struct {
	inner    *os.File
	failWhen func(line string) bool
	closed   bool
}

func (w *failWriter) Write(p []byte) (int, error) {
	if w.failWhen(string(p)) {
		return 0, errors.New("disk full")
	}
	return w.inner.Write(p)
}
func (w *failWriter) Close() error { w.closed = true; return w.inner.Close() }

func TestStorageFailureRetainsJournal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "disk.jsonl")
	o := Options{Operation: "scan", Seconds: 0.001}
	cap, _ := NewCapture(path, o, nil)
	fw := &failWriter{inner: cap.File.(*os.File), failWhen: func(string) bool { return true }}
	cap.File = fw
	status := (&Inspector{Adapter: newRadio(t)}).Run(context.Background(), cap, o)
	rows, _ := Events(path, 0, 0)
	if status != "incomplete" || cap.Failure() != "disk full" || rows[0]["type"] != "metadata" || !fw.closed {
		t.Fatalf("status=%s failure=%q closed=%v", status, cap.Failure(), fw.closed)
	}
}

func TestSuccessfulNotifyAndIndicateSummary(t *testing.T) {
	r := newRadio(t)
	r.chars = append(r.chars, Characteristic{UUID: "indicate", Handle: 5, Properties: []string{"indicate"}})
	status, rows := execute(t, r, func(o *Options) { o.Notify = []string{"notify", "5"} })
	sum := listening(t, rows)
	chs := channels(sum)
	if status != "complete" || sum["reason"] != "duration_elapsed" || num(sum["subscribed_count"]) != 2 ||
		num(sum["failed_count"]) != 0 || num(sum["event_count"]) != 4 {
		t.Fatalf("status=%s sum=%v", status, sum)
	}
	if e := num(sum["elapsed_s"]); !(e > 0 && e < 1) {
		t.Fatalf("elapsed = %v", e)
	}
	if num(chs[0]["event_count"]) != 2 || num(chs[1]["event_count"]) != 2 ||
		fmt.Sprint(chs[1]["subscription_properties"]) != "[indicate]" {
		t.Fatalf("channels = %v", chs)
	}
	for _, c := range chs {
		if c["unsubscribe_status"] != "stopped" {
			t.Fatalf("channel = %v", c)
		}
	}
	for kind, n := range map[string]int{"subscription_requested": 2, "subscription_active": 2, "unsubscribe_result": 2} {
		if got := len(ofType(rows, kind)); got != n {
			t.Errorf("%s = %d, want %d", kind, got, n)
		}
	}
	for _, n := range ofType(rows, "notification") {
		if _, has := n["mode"]; has {
			t.Error("notification must not carry mode")
		}
	}
}

func TestPartialSubscriptionFailureKeepsOtherChannels(t *testing.T) {
	r := newRadio(t)
	r.chars = append(r.chars, Characteristic{UUID: "indicate", Handle: 5, Properties: []string{"indicate"}})
	r.startNotify = func(r *fakeRadio, ctx context.Context, c Characteristic, cb NotifyFunc) error {
		if c.Handle == 2 {
			return errors.New("subscription denied")
		}
		cb(c, []byte{0x00, 0xfe})
		return nil
	}
	status, rows := execute(t, r, func(o *Options) { o.Notify = []string{"notify", "5"} })
	sum := listening(t, rows)
	chs := channels(sum)
	if status != "incomplete" || sum["reason"] != "duration_elapsed" || num(sum["subscribed_count"]) != 1 ||
		num(sum["failed_count"]) != 1 || num(sum["event_count"]) != 1 {
		t.Fatalf("status=%s sum=%v", status, sum)
	}
	if chs[0]["status"] != "failed" || chs[0]["error"] != "subscription denied" {
		t.Fatalf("channels = %v", chs)
	}
	if !r.has("notify:5") || !r.has("stop_notify:5") || r.has("stop_notify:2") || len(ofType(rows, "listening_started")) == 0 {
		t.Fatalf("calls = %v", r.calls)
	}
}

func TestAllSubscriptionsFailWithoutListening(t *testing.T) {
	r := newRadio(t)
	r.startNotify = func(*fakeRadio, context.Context, Characteristic, NotifyFunc) error { return errors.New("denied") }
	status, rows := execute(t, r, func(o *Options) { o.Notify = []string{"notify"} })
	sum := listening(t, rows)
	if status != "incomplete" || sum["reason"] != "all_subscriptions_failed" || num(sum["elapsed_s"]) != 0 ||
		num(sum["subscribed_count"]) != 0 || num(sum["failed_count"]) != 1 || num(sum["event_count"]) != 0 ||
		len(ofType(rows, "listening_started")) != 0 {
		t.Fatalf("status=%s sum=%v", status, sum)
	}
}

func TestZeroEventsAfterSuccessIsComplete(t *testing.T) {
	r := newRadio(t)
	r.startNotify = func(*fakeRadio, context.Context, Characteristic, NotifyFunc) error { return nil }
	status, rows := execute(t, r, func(o *Options) { o.Notify = []string{"notify"} })
	sum := listening(t, rows)
	if status != "complete" || num(sum["subscribed_count"]) != 1 || num(sum["event_count"]) != 0 ||
		sum["reason"] != "duration_elapsed" || channels(sum)[0]["status"] != "active" {
		t.Fatalf("status=%s sum=%v", status, sum)
	}
}

func TestNoSelectionIsDistinctFromSilentSubscription(t *testing.T) {
	status, rows := execute(t, newRadio(t), nil)
	sum := listening(t, rows)
	if status != "complete" || sum["reason"] != "no_subscriptions_selected" || len(channels(sum)) != 0 ||
		num(sum["elapsed_s"]) != 0 || len(ofType(rows, "listening_started")) != 0 {
		t.Fatalf("status=%s sum=%v", status, sum)
	}
}

func TestReadRejectionsDoNotBlockSubscriptions(t *testing.T) {
	r := newRadio(t)
	r.read = func(context.Context, Characteristic) ([]byte, error) { return nil, errors.New("READ_NOT_PERMITTED") }
	status, rows := execute(t, r, func(o *Options) { o.ReadAll = true; o.Notify = []string{"notify"} })
	if status != "complete" || len(ofType(rows, "read_error")) != 2 ||
		num(listening(t, rows)["event_count"]) != 2 || len(ofType(rows, "subscription_active")) == 0 {
		t.Fatalf("status=%s", status)
	}
}

func TestDuplicateUUIDSubscriptionsUseHandles(t *testing.T) {
	r := newRadio(t)
	r.chars = append(r.chars, Characteristic{UUID: "notify", Handle: 5, Properties: []string{"indicate"}})
	status, rows := execute(t, r, func(o *Options) { o.Notify = []string{"2", "5"} })
	chs := channels(listening(t, rows))
	if status != "complete" || len(chs) != 2 || num(chs[0]["handle"]) != 2 || num(chs[1]["handle"]) != 5 {
		t.Fatalf("status=%s chs=%v", status, chs)
	}
}

func TestSubscriptionTimeoutIsRecorded(t *testing.T) {
	r := newRadio(t)
	r.startNotify = func(_ *fakeRadio, ctx context.Context, _ Characteristic, _ NotifyFunc) error {
		return blockUntilDone(ctx)
	}
	status, rows := execute(t, r, func(o *Options) { o.Notify = []string{"notify"}; o.Timeout = 0.001 })
	e := ofType(rows, "subscription_error")
	if status != "incomplete" || len(e) != 1 || e[0]["error"] != "TimeoutError" ||
		listening(t, rows)["reason"] != "all_subscriptions_failed" {
		t.Fatalf("status=%s rows=%v", status, rows)
	}
}

func TestUnsubscribeErrorRecordedAndOtherChannelsCleaned(t *testing.T) {
	r := newRadio(t)
	r.chars = append(r.chars, Characteristic{UUID: "indicate", Handle: 5, Properties: []string{"indicate"}})
	r.stopNotify = func(_ *fakeRadio, _ context.Context, c Characteristic) error {
		if c.Handle == 2 {
			return errors.New("stop failed")
		}
		return nil
	}
	status, rows := execute(t, r, func(o *Options) { o.Notify = []string{"2", "5"} })
	chs := channels(listening(t, rows))
	if status != "incomplete" || !r.has("stop_notify:5") || chs[0]["unsubscribe_error"] != "stop failed" ||
		chs[1]["unsubscribe_status"] != "stopped" {
		t.Fatalf("status=%s chs=%v", status, chs)
	}
}

func TestStorageFailureWhileListeningStillCleansUp(t *testing.T) {
	r := newRadio(t)
	path := filepath.Join(t.TempDir(), "failed.jsonl")
	o := Options{Operation: "inspect", Address: "AA", Seconds: 0.001, Notify: []string{"notify"}, Duration: 100}
	cap, _ := NewCapture(path, o, nil)
	cap.File = &failWriter{inner: cap.File.(*os.File), failWhen: func(l string) bool { return strings.Contains(l, `"type":"notification"`) }}
	status := (&Inspector{Adapter: r}).Run(context.Background(), cap, o)
	rows, _ := Events(path, 0, 0)
	if status != "incomplete" || !cap.Closed() || listening(t, rows)["reason"] != "storage_failure" ||
		!r.has("stop_notify:2") || r.last() != "disconnect" {
		t.Fatalf("status=%s calls=%v", status, r.calls)
	}
}

func TestLimitReservesSpaceForSummary(t *testing.T) {
	r := newRadio(t)
	path := filepath.Join(t.TempDir(), "limit-listen.jsonl")
	o := Options{Operation: "inspect", Address: "AA", Seconds: 0.001, Notify: []string{"notify"}, Duration: 100}
	cap, _ := NewCapture(path, o, nil)
	defer SetMaxBytes(MaxBytes)()
	r.startNotify = func(_ *fakeRadio, _ context.Context, c Characteristic, cb NotifyFunc) error {
		MaxBytes = cap.Size() + 4096
		cb(c, []byte{0xff, 0x00})
		return nil
	}
	status := (&Inspector{Adapter: r}).Run(context.Background(), cap, o)
	rows, _ := Events(path, 0, 0)
	st, _ := os.Stat(path)
	if status != "incomplete" || listening(t, rows)["reason"] != "storage_failure" ||
		rows[len(rows)-1]["status"] != "incomplete" || st.Size() > MaxBytes {
		t.Fatalf("status=%s size=%d max=%d tail=%v", status, st.Size(), MaxBytes, rows[len(rows)-1])
	}
}

func TestCancelDuringCleanupRetainsFinalSummary(t *testing.T) {
	r := newRadio(t)
	entered, release := make(chan struct{}), make(chan struct{})
	r.stopNotify = func(_ *fakeRadio, _ context.Context, _ Characteristic) error {
		close(entered)
		<-release
		return nil
	}
	path := filepath.Join(t.TempDir(), "cleanup.jsonl")
	o := Options{Operation: "inspect", Address: "AA", Seconds: 0.001, Duration: 0.001, Notify: []string{"notify"}}
	cap, _ := NewCapture(path, o, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan string, 1)
	go func() { done <- (&Inspector{Adapter: r}).Run(ctx, cap, o) }()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("cleanup never started")
	}
	cancel()
	close(release)
	if got := <-done; got != "cancelled" {
		t.Fatalf("status = %s", got)
	}
	rows, _ := Events(path, 0, 0)
	sum := listening(t, rows)
	if sum["reason"] != "cancelled" || channels(sum)[0]["unsubscribe_status"] != "stopped" || r.last() != "disconnect" {
		t.Fatalf("sum=%v calls=%v", sum, r.calls)
	}
}

func TestCancelDuringSubscriptionDistinguishesUnconfirmed(t *testing.T) {
	r := newRadio(t)
	entered := make(chan struct{})
	r.startNotify = func(_ *fakeRadio, ctx context.Context, _ Characteristic, _ NotifyFunc) error {
		close(entered)
		return blockUntilDone(ctx)
	}
	path := filepath.Join(t.TempDir(), "unconfirmed.jsonl")
	o := Options{Operation: "inspect", Address: "AA", Seconds: 0.001, Notify: []string{"notify"}}
	cap, _ := NewCapture(path, o, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan string, 1)
	go func() { done <- (&Inspector{Adapter: r}).Run(ctx, cap, o) }()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("never entered")
	}
	cancel()
	if got := <-done; got != "cancelled" {
		t.Fatalf("status = %s", got)
	}
	rows, _ := Events(path, 0, 0)
	sum := listening(t, rows)
	if sum["reason"] != "cancelled" || channels(sum)[0]["status"] != "interrupted" ||
		num(sum["subscribed_count"]) != 0 || num(sum["elapsed_s"]) != 0 {
		t.Fatalf("sum = %v", sum)
	}
}

func TestPayloadViewAndASCIIJSON(t *testing.T) {
	p := Payload([]byte{0x00, 0xff, 'h', 'i'})
	if p.Hex != "00ff6869" || p.Length != 4 || p.TextGuess != "\x00\\xffhi" {
		t.Fatalf("%+v", p)
	}
	b, _ := asciiJSON(map[string]any{"s": "é😀<>&"})
	if string(b) != "{\"s\":\"\\u00e9\\ud83d\\ude00<>&\"}" {
		t.Fatalf("%s", b)
	}
}

func TestWriteJSONExport(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.jsonl")
	cap, _ := NewCapture(path, map[string]any{}, nil)
	cap.Emit("read", Payload([]byte{1}))
	cap.Finish("complete", nil)
	var sb strings.Builder
	if err := WriteJSONExport(&sb, path); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(sb.String(), `{"schema_version":1,"events":[`) || !strings.HasSuffix(sb.String(), "]}") ||
		!strings.Contains(sb.String(), `"status":"complete"`) {
		t.Fatalf("%s", sb.String())
	}
	if _, err := NewCapture(path, nil, nil); err == nil {
		t.Fatal("capture file must be created exclusively")
	}
}
