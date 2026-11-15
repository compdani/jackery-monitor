// Package blehw is the hardware ble.Adapter built on tinygo.org/x/bluetooth
// (CoreBluetooth on macOS, BlueZ over D-Bus on Linux, WinRT on Windows).
//
// It lives apart from package ble so that importing ble never needs cgo or
// a Bluetooth stack. Limits of the underlying library, handled here:
//
//   - GATT properties are not exposed, so every characteristic is reported
//     as read/write/write-without-response/notify/indicate capable. Reads or
//     subscriptions that the peripheral refuses surface as per-channel errors.
//   - ATT handles are not exposed, so Characteristic.Handle is a stable
//     1-based index in discovery order.
//   - Connecting needs the Address object from a prior scan (on macOS the
//     address is a CoreBluetooth UUID); Connect scans briefly when the
//     address has not been seen yet.
package blehw

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"tinygo.org/x/bluetooth"

	"github.com/compdani/jackery-monitor/internal/ble"
)

// Adapter implements ble.Adapter.
type Adapter struct {
	bt *bluetooth.Adapter

	enableOnce sync.Once
	enableErr  error

	mu   sync.Mutex
	seen map[string]bluetooth.Address // upper-cased address string → address
	// ConnectScan bounds the lookup scan when an address is unknown.
	ConnectScan time.Duration
}

// New returns an adapter on the default system Bluetooth controller.
func New() *Adapter {
	return &Adapter{bt: bluetooth.DefaultAdapter, seen: map[string]bluetooth.Address{}, ConnectScan: 8 * time.Second}
}

var _ ble.Adapter = (*Adapter)(nil)

func (a *Adapter) enable() error {
	a.enableOnce.Do(func() { a.enableErr = a.bt.Enable() })
	return a.enableErr
}

// Available enables the controller (once) and reports why it is unusable.
func (a *Adapter) Available() error {
	if err := a.enable(); err != nil {
		return fmt.Errorf("bluetooth unavailable: %w", err)
	}
	return nil
}

func uuidStrings(us []bluetooth.UUID) []string {
	out := make([]string, 0, len(us))
	for _, u := range us {
		out = append(out, strings.ToLower(u.String()))
	}
	return out
}

func toAdv(r bluetooth.ScanResult) ble.Advertisement {
	adv := ble.Advertisement{
		Address:      strings.ToUpper(r.Address.String()),
		Name:         r.LocalName(),
		LocalName:    r.LocalName(),
		RSSI:         int(r.RSSI),
		ServiceUUIDs: uuidStrings(r.ServiceUUIDs()),
	}
	if md := r.ManufacturerData(); len(md) > 0 {
		adv.ManufacturerData = map[uint16][]byte{}
		for _, e := range md {
			adv.ManufacturerData[e.CompanyID] = append([]byte(nil), e.Data...)
		}
	}
	if sd := r.ServiceData(); len(sd) > 0 {
		adv.ServiceData = map[string][]byte{}
		for _, e := range sd {
			adv.ServiceData[strings.ToLower(e.UUID.String())] = append([]byte(nil), e.Data...)
		}
	}
	return adv
}

// Scan reports advertisements until ctx is done.
func (a *Adapter) Scan(ctx context.Context, onAdv func(ble.Advertisement)) error {
	if err := a.enable(); err != nil {
		return err
	}
	errc := make(chan error, 1)
	go func() {
		errc <- a.bt.Scan(func(_ *bluetooth.Adapter, r bluetooth.ScanResult) {
			key := strings.ToUpper(r.Address.String())
			a.mu.Lock()
			a.seen[key] = r.Address
			a.mu.Unlock()
			onAdv(toAdv(r))
		})
	}()
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
		_ = a.bt.StopScan()
		<-errc
		return nil
	}
}

func (a *Adapter) lookup(ctx context.Context, address string) (bluetooth.Address, error) {
	key := strings.ToUpper(address)
	a.mu.Lock()
	addr, ok := a.seen[key]
	a.mu.Unlock()
	if ok {
		return addr, nil
	}
	sctx, cancel := context.WithTimeout(ctx, a.ConnectScan)
	defer cancel()
	found := make(chan struct{})
	scanDone := make(chan struct{})
	var once sync.Once
	go func() {
		defer close(scanDone)
		_ = a.Scan(sctx, func(adv ble.Advertisement) {
			if strings.EqualFold(adv.Address, key) {
				once.Do(func() { close(found) })
			}
		})
	}()
	select {
	case <-found:
		cancel()
	case <-sctx.Done():
	}
	// BlueZ connects unreliably while discovery is still running
	// (le-connection-abort-by-local), so wait for StopScan to finish.
	<-scanDone
	a.mu.Lock()
	addr, ok = a.seen[key]
	a.mu.Unlock()
	if !ok {
		return addr, fmt.Errorf("device %s not found while scanning", address)
	}
	return addr, nil
}

type conn struct {
	dev     bluetooth.Device
	svcs    []ble.Service
	chars   map[int]*bluetooth.DeviceCharacteristic
	closed  chan struct{}
	closeMu sync.Once
}

var allProps = []string{"read", "write", "write-without-response", "notify", "indicate"}

// Connect connects and discovers all services/characteristics.
func (a *Adapter) Connect(ctx context.Context, address string) (ble.Conn, error) {
	if err := a.enable(); err != nil {
		return nil, err
	}
	addr, err := a.lookup(ctx, address)
	if err != nil {
		return nil, err
	}
	type res struct {
		dev bluetooth.Device
		err error
	}
	rc := make(chan res, 1)
	go func() {
		d, err := a.bt.Connect(addr, bluetooth.ConnectionParams{})
		rc <- res{d, err}
	}()
	var dev bluetooth.Device
	select {
	case r := <-rc:
		if r.err != nil {
			return nil, r.err
		}
		dev = r.dev
	case <-ctx.Done():
		// Adapter contract: no half-open link after a failed Connect.
		go func() {
			if r := <-rc; r.err == nil {
				_ = r.dev.Disconnect()
			}
		}()
		return nil, ctx.Err()
	}
	c := &conn{dev: dev, chars: map[int]*bluetooth.DeviceCharacteristic{}, closed: make(chan struct{})}
	services, err := dev.DiscoverServices(nil)
	if err != nil {
		_ = dev.Disconnect()
		return nil, err
	}
	handle := 0
	for si, svc := range services {
		chars, err := svc.DiscoverCharacteristics(nil)
		if err != nil {
			_ = dev.Disconnect()
			return nil, err
		}
		bs := ble.Service{UUID: strings.ToLower(svc.UUID().String()), Handle: si}
		for i := range chars {
			// Keep one addressable value per characteristic: tinygo stores the
			// notification subscription on it, so StopNotify must see the
			// same value StartNotify changed.
			ch := &chars[i]
			handle++
			c.chars[handle] = ch
			bs.Characteristics = append(bs.Characteristics, ble.Characteristic{
				UUID: strings.ToLower(ch.UUID().String()), Handle: handle, Properties: append([]string(nil), allProps...),
			})
		}
		c.svcs = append(c.svcs, bs)
	}
	return c, nil
}

func (c *conn) Services() []ble.Service { return c.svcs }

func (c *conn) char(ch ble.Characteristic) (*bluetooth.DeviceCharacteristic, error) {
	dc, ok := c.chars[ch.Handle]
	if !ok {
		return nil, fmt.Errorf("unknown characteristic handle %d", ch.Handle)
	}
	return dc, nil
}

// withCtx runs fn but gives up waiting when ctx is done.
func withCtx[T any](ctx context.Context, fn func() (T, error)) (T, error) {
	type r struct {
		v   T
		err error
	}
	ch := make(chan r, 1)
	go func() { v, err := fn(); ch <- r{v, err} }()
	select {
	case x := <-ch:
		return x.v, x.err
	case <-ctx.Done():
		var zero T
		return zero, ctx.Err()
	}
}

func (c *conn) Read(ctx context.Context, ch ble.Characteristic) ([]byte, error) {
	dc, err := c.char(ch)
	if err != nil {
		return nil, err
	}
	return withCtx(ctx, func() ([]byte, error) {
		buf := make([]byte, 512)
		n, err := dc.Read(buf)
		if err != nil {
			return nil, err
		}
		return buf[:n], nil
	})
}

func (c *conn) StartNotify(ctx context.Context, ch ble.Characteristic, cb ble.NotifyFunc) error {
	dc, err := c.char(ch)
	if err != nil {
		return err
	}
	_, err = withCtx(ctx, func() (struct{}, error) {
		return struct{}{}, dc.EnableNotifications(func(buf []byte) { cb(ch, append([]byte(nil), buf...)) })
	})
	return err
}

func (c *conn) StopNotify(ctx context.Context, ch ble.Characteristic) error {
	dc, err := c.char(ch)
	if err != nil {
		return err
	}
	_, err = withCtx(ctx, func() (struct{}, error) { return struct{}{}, dc.EnableNotifications(nil) })
	return err
}

func (c *conn) Write(ctx context.Context, ch ble.Characteristic, data []byte, withResponse bool) error {
	dc, err := c.char(ch)
	if err != nil {
		return err
	}
	_, err = withCtx(ctx, func() (int, error) {
		if withResponse {
			return dc.Write(data)
		}
		return dc.WriteWithoutResponse(data)
	})
	return err
}

func (c *conn) Disconnect(ctx context.Context) error {
	var err error
	c.closeMu.Do(func() {
		_, err = withCtx(ctx, func() (struct{}, error) { return struct{}{}, c.dev.Disconnect() })
		close(c.closed)
	})
	if errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return err
}

// Disconnected is closed after an explicit Disconnect. tinygo's connection
// state callbacks are adapter-global and unreliable across platforms, so an
// unexpected link drop surfaces as I/O errors instead.
func (c *conn) Disconnected() <-chan struct{} { return c.closed }
