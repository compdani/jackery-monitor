package ble

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// MaxBytes is the per-capture size limit (100 MB). Tests lower it.
var MaxBytes int64 = 100_000_000

// summaryReserve is held back for bounded cleanup records and the final
// summary at the size limit.
const summaryReserve = 131072

// Options are the inspector/capture request options (StartRequest body).
type Options struct {
	Operation      string            `json:"operation"` // "scan" | "inspect"
	Seconds        float64           `json:"seconds"`
	Name           string            `json:"name,omitempty"`
	Address        string            `json:"address,omitempty"`
	ReadAll        bool              `json:"read_all"`
	Notify         []string          `json:"notify"`
	NotifyExpected map[string]string `json:"notify_expected"`
	Duration       float64           `json:"duration"`
	Timeout        float64           `json:"timeout"`
}

func (o Options) seconds() float64 {
	if o.Seconds <= 0 {
		return 10
	}
	return o.Seconds
}
func (o Options) duration() float64 {
	if o.Duration <= 0 {
		return 30
	}
	return o.Duration
}
func (o Options) timeout() time.Duration {
	t := o.Timeout
	if t <= 0 {
		t = 15
	}
	return time.Duration(t * float64(time.Second))
}

func secs(f float64) time.Duration { return time.Duration(f * float64(time.Second)) }

// Payload is the hex/text view of a raw byte value.
type PayloadView struct {
	Hex            string `json:"hex"`
	Length         int    `json:"length"`
	TextGuess      string `json:"text_guess"`
	Interpretation string `json:"interpretation"`
}

// Payload describes bytes the way ble_inspector.payload() does
// (UTF-8 guess with backslashreplace).
func Payload(data []byte) PayloadView {
	raw := append([]byte(nil), data...)
	var sb strings.Builder
	for i := 0; i < len(raw); {
		r, size := utf8.DecodeRune(raw[i:])
		if r == utf8.RuneError && size == 1 {
			fmt.Fprintf(&sb, "\\x%02x", raw[i])
			i++
			continue
		}
		sb.WriteRune(r)
		i += size
	}
	return PayloadView{
		Hex: fmt.Sprintf("%x", raw), Length: len(raw), TextGuess: sb.String(),
		Interpretation: "UTF-8 interpretation guess",
	}
}

// Capture appends and flushes each event as JSONL (flush == write-through;
// the file is unbuffered).
type Capture struct {
	Path string
	// File is replaceable (tests inject a failing writer).
	File    io.WriteCloser
	Echo    io.Writer
	mu      sync.Mutex
	seq     int
	size    int64
	failure string
	failed  chan struct{}
	failOnc sync.Once
	closed  bool
	Now     func() time.Time
}

// NewCapture exclusively creates path ("x" mode) and writes the metadata
// event.
func NewCapture(path string, options any, echo io.Writer) (*Capture, error) {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return nil, err
	}
	c := &Capture{Path: path, File: f, Echo: echo, failed: make(chan struct{}), Now: time.Now}
	if err := c.Emit("metadata", map[string]any{"schema_version": 1, "options": options}); err != nil {
		_ = f.Close()
		return nil, err
	}
	return c, nil
}

// Failed is closed once a write fails.
func (c *Capture) Failed() <-chan struct{} { return c.failed }

// Failure is the storage failure text ("" when none).
func (c *Capture) Failure() string { c.mu.Lock(); defer c.mu.Unlock(); return c.failure }

// Size is the bytes written so far.
func (c *Capture) Size() int64 { c.mu.Lock(); defer c.mu.Unlock(); return c.size }

// Closed reports whether finish closed the file.
func (c *Capture) Closed() bool { c.mu.Lock(); defer c.mu.Unlock(); return c.closed }

// SetMaxBytes is a convenience for tests.
func SetMaxBytes(n int64) (restore func()) {
	old := MaxBytes
	MaxBytes = n
	return func() { MaxBytes = old }
}

// asciiJSON marshals like Python json.dumps(ensure_ascii=True).
func asciiJSON(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	raw := bytes.TrimRight(buf.Bytes(), "\n")
	var out bytes.Buffer
	for i := 0; i < len(raw); {
		r, size := utf8.DecodeRune(raw[i:])
		switch {
		case r < 0x80:
			out.WriteByte(raw[i])
		case r >= 0x10000:
			r -= 0x10000
			fmt.Fprintf(&out, "\\u%04x\\u%04x", 0xD800+(r>>10), 0xDC00+(r&0x3FF))
		default:
			fmt.Fprintf(&out, "\\u%04x", r)
		}
		i += size
	}
	return out.Bytes(), nil
}

// Emit appends one event. data may be a struct or a map; its fields are
// merged next to seq/timestamp/type. On a write or size-limit failure the
// capture is marked failed and the error returned.
func (c *Capture) Emit(kind string, data any) error {
	fields := map[string]any{}
	if data != nil {
		b, err := json.Marshal(data)
		if err != nil {
			return err
		}
		if err := json.Unmarshal(b, &fields); err != nil {
			return err
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	fields["seq"] = c.seq + 1
	fields["timestamp"] = c.Now().UTC().Format("2006-01-02T15:04:05.000000+00:00")
	fields["type"] = kind
	line, err := asciiJSON(fields)
	if err != nil {
		return err
	}
	line = append(line, '\n')
	terminal := kind == "completion" || kind == "listening_ended" || kind == "unsubscribe_result"
	reserve := int64(summaryReserve)
	if terminal {
		reserve = 0
	}
	if c.size+int64(len(line)) > MaxBytes-reserve {
		return c.fail(errors.New("100 MB capture limit reached"))
	}
	if _, err := c.File.Write(line); err != nil {
		return c.fail(err)
	}
	c.seq++
	c.size += int64(len(line))
	if c.Echo != nil {
		_, _ = c.Echo.Write(line)
	}
	return nil
}

func (c *Capture) fail(err error) error {
	c.failure = err.Error()
	c.failOnc.Do(func() { close(c.failed) })
	return err
}

// Finish writes the completion event and closes the file. The completion
// write is best-effort: already-flushed events remain recoverable.
func (c *Capture) Finish(status string, errText *string) {
	data := map[string]any{"status": status, "error": errText}
	_ = c.Emit("completion", data)
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.closed {
		c.closed = true
		_ = c.File.Close()
	}
}

// Events reads events with seq > after, up to limit (0 = all). A trailing
// partial line (a reader racing the final append) ends the read.
func Events(path string, after int, limit int) ([]map[string]any, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	r := bufio.NewReaderSize(f, 1<<16)
	out := []map[string]any{}
	for {
		line, err := r.ReadBytes('\n')
		if len(bytes.TrimSpace(line)) > 0 {
			var ev map[string]any
			if json.Unmarshal(line, &ev) != nil {
				break
			}
			if seq, _ := ev["seq"].(float64); int(seq) > after {
				out = append(out, ev)
				if limit > 0 && len(out) >= limit {
					break
				}
			}
		}
		if err != nil {
			break
		}
	}
	return out, nil
}

// WriteJSONExport streams {"schema_version":1,"events":[…]} without loading
// the capture into memory (Pi RAM).
func WriteJSONExport(w io.Writer, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := io.WriteString(w, `{"schema_version":1,"events":[`); err != nil {
		return err
	}
	r := bufio.NewReaderSize(f, 1<<16)
	first := true
	for {
		line, rerr := r.ReadBytes('\n')
		trimmed := bytes.TrimSpace(line)
		if len(trimmed) > 0 {
			if !json.Valid(trimmed) {
				break
			}
			if !first {
				if _, err := io.WriteString(w, ","); err != nil {
					return err
				}
			}
			if _, err := w.Write(trimmed); err != nil {
				return err
			}
			first = false
		}
		if rerr != nil {
			break
		}
	}
	_, err = io.WriteString(w, "]}")
	return err
}

// errText is str(exc) or the exception type name, with timeouts rendered as
// Python's "TimeoutError".
func errText(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "TimeoutError"
	}
	if s := err.Error(); s != "" {
		return s
	}
	return fmt.Sprintf("%T", err)
}

// ---------------------------------------------------------------- scanning

// advRow is the advertisement event / scan_summary row.
type advRow struct {
	Address          string                 `json:"address"`
	Name             *string                `json:"name"`
	RSSI             int                    `json:"rssi"`
	ServiceUUIDs     []string               `json:"service_uuids"`
	ManufacturerData map[string]PayloadView `json:"manufacturer_data"`
	ServiceData      map[string]PayloadView `json:"service_data"`
}

func rowFromAdv(a Advertisement) advRow {
	name := a.LocalName
	if name == "" {
		name = a.Name
	}
	var np *string
	if name != "" {
		np = &name
	}
	md := map[string]PayloadView{}
	for k, v := range a.ManufacturerData {
		md[strconv.Itoa(int(k))] = Payload(v)
	}
	sd := map[string]PayloadView{}
	for k, v := range a.ServiceData {
		sd[k] = Payload(v)
	}
	uuids := a.ServiceUUIDs
	if uuids == nil {
		uuids = []string{}
	}
	return advRow{Address: a.Address, Name: np, RSSI: a.RSSI, ServiceUUIDs: uuids, ManufacturerData: md, ServiceData: sd}
}

// Inspector runs scan / inspect operations against an Adapter. It never
// writes to the peripheral.
type Inspector struct {
	Adapter Adapter
}

func (in *Inspector) scan(ctx context.Context, cap *Capture, seconds float64, name, address string) (map[string]advRow, error) {
	var mu sync.Mutex
	found := map[string]advRow{}
	var order []string
	onAdv := func(a Advertisement) {
		deviceName := a.LocalName
		if deviceName == "" {
			deviceName = a.Name
		}
		if name != "" && !strings.Contains(strings.ToLower(deviceName), strings.ToLower(name)) {
			return
		}
		if address != "" && !strings.EqualFold(address, a.Address) {
			return
		}
		row := rowFromAdv(a)
		if err := cap.Emit("advertisement", row); err != nil {
			return
		}
		mu.Lock()
		if _, ok := found[a.Address]; !ok {
			order = append(order, a.Address)
		}
		found[a.Address] = row
		mu.Unlock()
	}
	scanCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- in.Adapter.Scan(scanCtx, onAdv) }()
	timer := time.NewTimer(secs(seconds))
	var serr error
	select {
	case <-timer.C:
	case <-cap.Failed():
	case <-ctx.Done():
	case serr = <-done:
		done <- serr // keep for the drain below
	}
	timer.Stop()
	cancel()
	select {
	case e := <-done:
		if serr == nil {
			serr = e
		}
	case <-time.After(15 * time.Second):
		serr = errors.New("timed out stopping scanner")
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if serr != nil && !errors.Is(serr, context.Canceled) {
		return nil, serr
	}
	if f := cap.Failure(); f != "" {
		return nil, errors.New(f)
	}
	mu.Lock()
	rows := make([]advRow, 0, len(order))
	for _, a := range order {
		rows = append(rows, found[a])
	}
	copyFound := make(map[string]advRow, len(found))
	for k, v := range found {
		copyFound[k] = v
	}
	mu.Unlock()
	if err := cap.Emit("scan_summary", map[string]any{"devices": rows}); err != nil {
		return nil, err
	}
	return copyFound, nil
}

// SelectNotifications resolves --notify selectors (handle number or UUID)
// to characteristics that support notify/indicate. A selector must match
// exactly one characteristic; duplicates collapse.
func SelectNotifications(chars []Characteristic, selections []string) ([]Characteristic, error) {
	var selected []Characteristic
	for _, key := range selections {
		handle, herr := strconv.ParseInt(key, 0, 64)
		var matches []Characteristic
		for _, c := range chars {
			if herr == nil {
				if int64(c.Handle) == handle {
					matches = append(matches, c)
				}
			} else if strings.EqualFold(c.UUID, key) {
				matches = append(matches, c)
			}
		}
		if len(matches) != 1 {
			return nil, fmt.Errorf("Notification selector %s: expected one match, found %d; use handle", key, len(matches))
		}
		ch := matches[0]
		if !ch.HasProperty("notify") && !ch.HasProperty("indicate") {
			return nil, fmt.Errorf("Characteristic %s does not support notify/indicate", key)
		}
		dup := false
		for _, s := range selected {
			if s.Handle == ch.Handle && s.UUID == ch.UUID {
				dup = true
			}
		}
		if !dup {
			selected = append(selected, ch)
		}
	}
	return selected, nil
}

type charInfo struct {
	UUID       string   `json:"uuid"`
	Handle     int      `json:"handle"`
	Properties []string `json:"properties"`
}

func infoOf(c Characteristic) charInfo {
	p := c.Properties
	if p == nil {
		p = []string{}
	}
	return charInfo{UUID: c.UUID, Handle: c.Handle, Properties: p}
}

// Channel is one requested subscription (JSON keys match Python).
type Channel struct {
	UUID                   string   `json:"uuid"`
	Handle                 int      `json:"handle"`
	Properties             []string `json:"properties"`
	SubscriptionProperties []string `json:"subscription_properties"`
	Status                 string   `json:"status"`
	EventCount             int      `json:"event_count"`
	Error                  string   `json:"error,omitempty"`
	UnsubscribeStatus      string   `json:"unsubscribe_status,omitempty"`
	UnsubscribeError       string   `json:"unsubscribe_error,omitempty"`
}

func (in *Inspector) inspect(ctx context.Context, cap *Capture, o Options) error {
	found, err := in.scan(ctx, cap, o.seconds(), o.Name, o.Address)
	if err != nil {
		return err
	}
	if len(found) != 1 {
		var cands []advRow
		for _, r := range found {
			cands = append(cands, r)
		}
		b, _ := json.Marshal(cands)
		if cands == nil {
			b = []byte("[]")
		}
		return fmt.Errorf("Expected one device, found %d. Candidates: %s", len(found), b)
	}
	var row advRow
	for _, r := range found {
		row = r
	}
	if err := cap.Emit("target", row); err != nil {
		return err
	}
	timeout := o.timeout()

	connCtx, cancel := context.WithTimeout(ctx, timeout)
	conn, err := in.Adapter.Connect(connCtx, row.Address)
	cancel()
	if err != nil {
		// Adapter contract: a failed or timed-out Connect leaves no half-open
		// link (it disconnects internally), like the Python context manager.
		return err
	}
	defer in.disconnect(ctx, conn, timeout)

	var chars []Characteristic
	for _, svc := range conn.Services() {
		chars = append(chars, svc.Characteristics...)
		infos := make([]charInfo, len(svc.Characteristics))
		for i, c := range svc.Characteristics {
			infos[i] = infoOf(c)
		}
		if err := cap.Emit("service", map[string]any{"uuid": svc.UUID, "handle": svc.Handle, "characteristics": infos}); err != nil {
			return err
		}
	}
	selected, err := SelectNotifications(chars, o.Notify)
	if err != nil {
		return err
	}
	for _, c := range selected {
		if want, ok := o.NotifyExpected[strconv.Itoa(c.Handle)]; ok && !strings.EqualFold(want, c.UUID) {
			return errors.New("GATT structure changed; inspect again before subscribing")
		}
	}
	return in.inspectValues(ctx, cap, conn, chars, selected, o)
}

func (in *Inspector) disconnect(ctx context.Context, conn Conn, timeout time.Duration) {
	dctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), timeout)
	defer cancel()
	_ = conn.Disconnect(dctx)
}

// inspectValues tracks requested subscriptions independently, including
// silent successful streams.
func (in *Inspector) inspectValues(ctx context.Context, cap *Capture, conn Conn, chars, selected []Characteristic, o Options) (retErr error) {
	timeout := o.timeout()
	duration := o.duration()
	var mu sync.Mutex
	channels := map[int]*Channel{}
	var chOrder []int
	for _, c := range selected {
		subProps := []string{}
		for _, p := range c.Properties {
			if p == "notify" || p == "indicate" {
				subProps = append(subProps, p)
			}
		}
		info := infoOf(c)
		channels[c.Handle] = &Channel{UUID: info.UUID, Handle: info.Handle, Properties: info.Properties,
			SubscriptionProperties: subProps, Status: "not_attempted"}
		chOrder = append(chOrder, c.Handle)
	}
	snap := func(h int) Channel { mu.Lock(); defer mu.Unlock(); return *channels[h] }
	setCh := func(h int, fn func(*Channel)) { mu.Lock(); fn(channels[h]); mu.Unlock() }

	var subscribed []Characteristic
	var listeningAt time.Time
	reason := "error"
	if len(selected) == 0 {
		reason = "no_subscriptions_selected"
	}
	failures, cleanupFailures := 0, 0
	disconnected := conn.Disconnected()

	received := func(c Characteristic, data []byte) {
		mu.Lock()
		ch, ok := channels[c.Handle]
		if ok {
			ch.EventCount++
		}
		mu.Unlock()
		view := Payload(data)
		payload := map[string]any{"uuid": c.UUID, "handle": c.Handle, "properties": infoOf(c).Properties,
			"hex": view.Hex, "length": view.Length, "text_guess": view.TextGuess, "interpretation": view.Interpretation}
		_ = cap.Emit("notification", payload) // capture.Failed wakes the listener
	}
	isDisc := func() bool {
		select {
		case <-disconnected:
			return true
		default:
			return false
		}
	}

	body := func() error {
		for _, c := range selected {
			setCh(c.Handle, func(ch *Channel) { ch.Status = "requested" })
			if err := cap.Emit("subscription_requested", snap(c.Handle)); err != nil {
				return err
			}
		}
		if o.ReadAll {
			for _, c := range chars {
				if isDisc() {
					return errors.New("Device disconnected")
				}
				if !c.HasProperty("read") {
					continue
				}
				rctx, cancel := context.WithTimeout(ctx, timeout)
				data, err := conn.Read(rctx, c)
				cancel()
				if err != nil {
					if ctx.Err() != nil {
						return ctx.Err()
					}
					if e := cap.Emit("read_error", map[string]any{"uuid": c.UUID, "handle": c.Handle,
						"properties": infoOf(c).Properties, "error": errText(err)}); e != nil {
						return e
					}
					continue
				}
				v := Payload(data)
				if e := cap.Emit("read", map[string]any{"uuid": c.UUID, "handle": c.Handle,
					"properties": infoOf(c).Properties, "hex": v.Hex, "length": v.Length,
					"text_guess": v.TextGuess, "interpretation": v.Interpretation}); e != nil {
					return e
				}
			}
		}
		for _, c := range selected {
			if f := cap.Failure(); f != "" {
				return errors.New(f)
			}
			if isDisc() {
				return errors.New("Device disconnected")
			}
			setCh(c.Handle, func(ch *Channel) { ch.Status = "starting" })
			nctx, cancel := context.WithTimeout(ctx, timeout)
			err := conn.StartNotify(nctx, c, received)
			cancel()
			if err != nil {
				if ctx.Err() != nil {
					return ctx.Err() // activation not confirmed → "interrupted" in cleanup
				}
				failures++
				setCh(c.Handle, func(ch *Channel) { ch.Status, ch.Error = "failed", errText(err) })
				if e := cap.Emit("subscription_error", snap(c.Handle)); e != nil {
					return e
				}
				continue
			}
			subscribed = append(subscribed, c)
			setCh(c.Handle, func(ch *Channel) { ch.Status = "active" })
			if e := cap.Emit("subscription_active", snap(c.Handle)); e != nil {
				return e
			}
		}
		if len(subscribed) > 0 {
			listeningAt = time.Now()
			chs := make([]Channel, len(subscribed))
			for i, c := range subscribed {
				chs[i] = snap(c.Handle)
			}
			if e := cap.Emit("listening_started", map[string]any{"duration_s": duration, "channels": chs}); e != nil {
				return e
			}
			timer := time.NewTimer(secs(duration))
			select {
			case <-timer.C:
			case <-disconnected:
			case <-cap.Failed():
			case <-ctx.Done():
			}
			timer.Stop()
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if f := cap.Failure(); f != "" {
			return errors.New(f)
		}
		if isDisc() {
			return errors.New("Device disconnected")
		}
		if len(subscribed) > 0 {
			reason = "duration_elapsed"
		} else if len(selected) > 0 {
			reason = "all_subscriptions_failed"
		} else {
			reason = "no_subscriptions_selected"
		}
		return nil
	}

	err := body()
	if err != nil {
		switch {
		case ctx.Err() != nil:
			reason = "cancelled"
		case cap.Failure() != "":
			reason = "storage_failure"
		case isDisc():
			reason = "disconnected"
		default:
			reason = "error"
		}
	}

	// Measure the listening window separately from scanning, reads and cleanup.
	elapsed := 0.0
	if !listeningAt.IsZero() {
		elapsed = time.Since(listeningAt).Seconds()
	}
	// Cleanup must complete even if the run is being cancelled.
	cctx := context.WithoutCancel(ctx)
	for _, c := range subscribed {
		sctx, cancel := context.WithTimeout(cctx, timeout)
		serr := conn.StopNotify(sctx, c)
		cancel()
		if serr != nil {
			cleanupFailures++
			setCh(c.Handle, func(ch *Channel) { ch.UnsubscribeStatus, ch.UnsubscribeError = "failed", errText(serr) })
		} else {
			setCh(c.Handle, func(ch *Channel) { ch.UnsubscribeStatus = "stopped" })
		}
		if e := cap.Emit("unsubscribe_result", snap(c.Handle)); e != nil {
			reason = "storage_failure"
		}
	}
	mu.Lock()
	for _, h := range chOrder {
		ch := channels[h]
		switch ch.Status {
		case "requested":
			ch.Status = "not_attempted"
		case "starting":
			ch.Status, ch.Error = "interrupted", "Subscription activation was not confirmed"
		}
	}
	events := 0
	chList := make([]Channel, 0, len(chOrder))
	for _, h := range chOrder {
		events += channels[h].EventCount
		chList = append(chList, *channels[h])
	}
	mu.Unlock()
	// A cancellation that lands during cleanup still wins the reason, and the
	// run reports as cancelled (the final summary is retained either way).
	if ctx.Err() != nil {
		reason = "cancelled"
		if err == nil {
			err = ctx.Err()
		}
	}
	_ = cap.Emit("listening_ended", map[string]any{
		"reason": reason, "duration_s": duration, "elapsed_s": elapsed,
		"subscribed_count": len(subscribed), "failed_count": failures,
		"event_count": events, "channels": chList,
	})

	if err != nil {
		return err
	}
	if f := cap.Failure(); f != "" {
		return errors.New(f)
	}
	if failures > 0 || cleanupFailures > 0 {
		return fmt.Errorf("%d subscription(s) failed; %d unsubscribe(s) failed", failures, cleanupFailures)
	}
	return nil
}

// Run executes the operation and finishes the capture. It returns the
// final status: "complete", "incomplete" or "cancelled".
func (in *Inspector) Run(ctx context.Context, cap *Capture, o Options) string {
	status := "complete"
	var errMsg *string
	var err error
	if o.Operation == "scan" {
		_, err = in.scan(ctx, cap, o.seconds(), o.Name, o.Address)
	} else {
		err = in.inspect(ctx, cap, o)
	}
	if err != nil {
		if ctx.Err() != nil && (errors.Is(err, context.Canceled) || errors.Is(err, ctx.Err())) {
			status = "cancelled"
			m := "Stopped by user or server shutdown"
			errMsg = &m
		} else {
			status = "incomplete"
			m := errText(err)
			errMsg = &m
			_ = cap.Emit("error", map[string]any{"error": m})
		}
	}
	cap.Finish(status, errMsg)
	return status
}
