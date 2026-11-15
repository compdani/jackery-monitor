package ble

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestLooksLikeJBDNameAndUUID(t *testing.T) {
	const ff00 = "0000ff00-0000-1000-8000-00805f9b34fb"
	cases := []struct {
		name  string
		uuids []string
		sd    map[string][]byte
		want  bool
	}{
		{"JBD-SP04S020", nil, nil, true},
		{"Overkill Solar", nil, nil, true},
		{"Xiaoxiang BMS", nil, nil, true},
		{"", []string{ff00}, nil, true},
		{"", nil, map[string][]byte{ff00: {}}, true},
		{"iPhone", nil, nil, false},
		{"", []string{"0000180a-0000-1000-8000-00805f9b34fb"}, nil, false},
	}
	for _, c := range cases {
		if got := LooksLikeJBD(c.name, c.uuids, c.sd); got != c.want {
			t.Errorf("LooksLikeJBD(%q,%v,%v) = %v", c.name, c.uuids, c.sd, got)
		}
	}
}

func TestStatusIsStructured(t *testing.T) {
	st := (&Poller{}).Status()
	if st.Available || st.Error == nil {
		t.Fatalf("%+v", st)
	}
	st = (&Poller{Adapter: newRadio(t)}).Status()
	if !st.Available || st.Error != nil {
		t.Fatalf("%+v", st)
	}
}

func TestScanFlagsAndSortsLikelyJBD(t *testing.T) {
	r := newRadio(t)
	r.scanStart = func(_ *fakeRadio, cb func(Advertisement)) {
		cb(Advertisement{Address: "aa:01", Name: "iPhone", RSSI: -40})
		cb(Advertisement{Address: "aa:02", LocalName: "JBD-SP04S", RSSI: -80})
		cb(Advertisement{Address: "AA:02", LocalName: "dup", RSSI: -10}) // dedupe by upper-cased address
		cb(Advertisement{Address: "aa:03", RSSI: -30, ServiceUUIDs: []string{"0000ff00-0000-1000-8000-00805f9b34fb"}})
		cb(Advertisement{Address: "aa:04", RSSI: -20})
	}
	rows, err := (&Poller{Adapter: r, Lock: NewAdapterLock()}).Scan(context.Background(), 20*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	var macs []string
	for _, row := range rows {
		macs = append(macs, row.MAC)
	}
	// likely first (strongest RSSI first), then the rest by RSSI.
	if strings.Join(macs, ",") != "AA:03,AA:02,AA:04,AA:01" {
		t.Fatalf("order = %v", macs)
	}
	if rows[1].Name != "JBD-SP04S" || !rows[1].LikelyJBD || rows[3].LikelyJBD || rows[2].Name != "AA:04" {
		t.Fatalf("rows = %+v", rows)
	}
}

func TestScanBusyAndUnavailable(t *testing.T) {
	lock := NewAdapterLock()
	lock.TryAcquire()
	p := &Poller{Adapter: newRadio(t), Lock: lock}
	// shrink the 2s wait by cancelling the context quickly
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, err := p.Scan(ctx, time.Millisecond); !IsUnavailable(err) || !strings.Contains(err.Error(), "busy") {
		t.Fatalf("err = %v", err)
	}
	if _, err := (&Poller{}).Scan(context.Background(), time.Millisecond); !IsUnavailable(err) {
		t.Fatalf("err = %v", err)
	}
}

// bmsConn answers JBD read requests like a real pack, fragmenting replies.
type bmsConn struct {
	mu      sync.Mutex
	notify  NotifyFunc
	writes  [][]byte
	basic   []byte
	cells   []byte
	silent  bool
	disc    chan struct{}
	stopped bool
}

func (c *bmsConn) Services() []Service {
	return []Service{{UUID: JBDServiceUUID, Characteristics: []Characteristic{
		{UUID: JBDNotifyUUID, Handle: 1, Properties: []string{"notify"}},
		{UUID: strings.ToUpper(JBDWriteUUID), Handle: 2, Properties: []string{"write-without-response"}},
	}}}
}
func (c *bmsConn) Read(context.Context, Characteristic) ([]byte, error) { return nil, errors.New("no") }
func (c *bmsConn) StartNotify(_ context.Context, _ Characteristic, cb NotifyFunc) error {
	c.mu.Lock()
	c.notify = cb
	c.mu.Unlock()
	return nil
}
func (c *bmsConn) StopNotify(context.Context, Characteristic) error { c.stopped = true; return nil }
func (c *bmsConn) Write(_ context.Context, ch Characteristic, data []byte, _ bool) error {
	c.mu.Lock()
	c.writes = append(c.writes, data)
	cb := c.notify
	c.mu.Unlock()
	if c.silent {
		return nil
	}
	var frame []byte
	switch data[2] {
	case CmdBasic:
		frame = BuildResponse(CmdBasic, c.basic, 0)
	case CmdCells:
		frame = BuildResponse(CmdCells, c.cells, 0)
	}
	go func() { // fragmented across two notifications
		cb(ch, frame[:6])
		cb(ch, frame[6:])
	}()
	return nil
}
func (c *bmsConn) Disconnect(context.Context) error { close(c.disc); return nil }
func (c *bmsConn) Disconnected() <-chan struct{}    { return c.disc }

type bmsAdapter struct {
	*fakeRadio
	conns map[string]*bmsConn
	err   map[string]error
}

func (a *bmsAdapter) Connect(_ context.Context, addr string) (Conn, error) {
	if e := a.err[addr]; e != nil {
		return nil, e
	}
	return a.conns[addr], nil
}

func newBmsConn() *bmsConn {
	var cells []byte
	for i := 0; i < 4; i++ {
		cells = append(cells, be16(3300+i)...)
	}
	return &bmsConn{basic: basicPayload(defaultBasic()), cells: cells, disc: make(chan struct{})}
}

func TestPollOneReadsBasicAndCells(t *testing.T) {
	conn := newBmsConn()
	p := &Poller{Adapter: &bmsAdapter{fakeRadio: newRadio(t), conns: map[string]*bmsConn{"AA": conn}},
		Now: func() time.Time { return time.Unix(1_700_000_000, 0) }}
	r, err := p.PollOne(context.Background(), "AA", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if r.SocPct == nil || *r.SocPct != 80 || *r.VoltageV != 53.21 || *r.CurrentA != -12.5 ||
		*r.MinCellMv != 3300 || *r.MaxCellMv != 3303 || *r.TS != 1_700_000_000 || r.Error != nil {
		t.Fatalf("%+v", r)
	}
	if len(conn.writes) != 2 || conn.writes[0][2] != CmdBasic || conn.writes[1][2] != CmdCells {
		t.Fatalf("writes = %x", conn.writes)
	}
	if !conn.stopped {
		t.Fatal("notify not stopped")
	}
	select {
	case <-conn.disc:
	default:
		t.Fatal("not disconnected")
	}
}

func TestPollOneTimeoutWhenSilent(t *testing.T) {
	conn := newBmsConn()
	conn.silent = true
	p := &Poller{Adapter: &bmsAdapter{fakeRadio: newRadio(t), conns: map[string]*bmsConn{"AA": conn}}}
	_, err := p.PollOne(context.Background(), "AA", 50*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "timeout waiting for 0x03") {
		t.Fatalf("err = %v", err)
	}
}

func TestPollAllRecordsFailuresAsErrorReadings(t *testing.T) {
	good := newBmsConn()
	a := &bmsAdapter{fakeRadio: newRadio(t), conns: map[string]*bmsConn{"GOOD": good},
		err: map[string]error{"BAD": errors.New(strings.Repeat("x", 300))}}
	p := &Poller{Adapter: a, Lock: NewAdapterLock()}
	out := p.PollAll(context.Background(), []string{"BAD", "GOOD"}, time.Second)
	if out["GOOD"].SocPct == nil || out["GOOD"].Error != nil {
		t.Fatalf("good = %+v", out["GOOD"])
	}
	bad := out["BAD"]
	if bad.SocPct != nil || bad.Error == nil || len(*bad.Error) != 240 || bad.TS == nil {
		t.Fatalf("bad = %+v", bad)
	}
	if p.Lock.Locked() {
		t.Fatal("lock must be released")
	}
}

func TestAdapterLock(t *testing.T) {
	l := NewAdapterLock()
	if l.Locked() || !l.TryAcquire() || !l.Locked() || l.TryAcquire() {
		t.Fatal("basic lock semantics")
	}
	if l.Acquire(context.Background(), 10*time.Millisecond) {
		t.Fatal("must time out")
	}
	l.Release()
	if !l.Acquire(context.Background(), time.Second) {
		t.Fatal("must acquire")
	}
	if GlobalAdapterLock() != GlobalAdapterLock() {
		t.Fatal("global lock must be a singleton")
	}
}
