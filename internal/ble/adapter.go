package ble

import (
	"context"
	"errors"
	"sync"
	"time"
)

// Advertisement is one BLE advertisement (bleak's device + AdvertisementData).
type Advertisement struct {
	Address          string
	Name             string // device name ("" when unknown)
	LocalName        string // advertised local name ("" when absent)
	RSSI             int
	ServiceUUIDs     []string
	ManufacturerData map[uint16][]byte
	ServiceData      map[string][]byte
}

// Characteristic is one GATT characteristic. Handle is the ATT handle when
// the backend exposes it, otherwise a stable synthetic index.
type Characteristic struct {
	UUID       string
	Handle     int
	Properties []string
}

// HasProperty reports whether the characteristic advertises p.
func (c Characteristic) HasProperty(p string) bool {
	for _, x := range c.Properties {
		if x == p {
			return true
		}
	}
	return false
}

// Service is a GATT service with its characteristics.
type Service struct {
	UUID            string
	Handle          int
	Characteristics []Characteristic
}

// NotifyFunc receives characteristic notifications/indications.
type NotifyFunc func(c Characteristic, data []byte)

// Conn is one connected peripheral.
type Conn interface {
	Services() []Service
	Read(ctx context.Context, c Characteristic) ([]byte, error)
	StartNotify(ctx context.Context, c Characteristic, cb NotifyFunc) error
	StopNotify(ctx context.Context, c Characteristic) error
	// Write writes a characteristic value. The inspector never calls it.
	Write(ctx context.Context, c Characteristic, data []byte, withResponse bool) error
	Disconnect(ctx context.Context) error
	// Disconnected is closed when the peripheral drops the link.
	Disconnected() <-chan struct{}
}

// Adapter is the host Bluetooth radio.
type Adapter interface {
	// Available returns nil when the stack is usable, else a description
	// ("bleak not installed" analogue).
	Available() error
	// Scan reports every advertisement until ctx is done, then returns nil.
	// A setup failure returns an error.
	Scan(ctx context.Context, onAdv func(Advertisement)) error
	// Connect connects to a device by address (as seen in a scan). A failed
	// or timed-out Connect must leave no half-open link: the adapter
	// disconnects internally before returning the error.
	Connect(ctx context.Context, address string) (Conn, error)
}

// ErrUnavailable means the BLE stack is not usable in this process
// (BleUnavailable).
type ErrUnavailable struct{ Msg string }

func (e *ErrUnavailable) Error() string { return e.Msg }

// IsUnavailable reports whether err is an *ErrUnavailable.
func IsUnavailable(err error) bool {
	var e *ErrUnavailable
	return errors.As(err, &e)
}

// AdapterLock serialises radio use: BMS modules drop notifications if two
// centrals stay connected, and a single dongle is a single adapter.
type AdapterLock struct{ ch chan struct{} }

// NewAdapterLock returns an unlocked lock.
func NewAdapterLock() *AdapterLock { return &AdapterLock{ch: make(chan struct{}, 1)} }

// TryAcquire takes the lock without waiting.
func (l *AdapterLock) TryAcquire() bool {
	select {
	case l.ch <- struct{}{}:
		return true
	default:
		return false
	}
}

// Acquire waits up to timeout (0 = until ctx is done).
func (l *AdapterLock) Acquire(ctx context.Context, timeout time.Duration) bool {
	var tc <-chan time.Time
	if timeout > 0 {
		t := time.NewTimer(timeout)
		defer t.Stop()
		tc = t.C
	}
	select {
	case l.ch <- struct{}{}:
		return true
	case <-tc:
		return false
	case <-ctx.Done():
		return false
	}
}

// Release frees the lock.
func (l *AdapterLock) Release() {
	select {
	case <-l.ch:
	default:
	}
}

// Locked reports whether the lock is held.
func (l *AdapterLock) Locked() bool { return len(l.ch) > 0 }

var (
	globalLockOnce sync.Once
	globalLock     *AdapterLock
)

// GlobalAdapterLock is the process-wide lock shared by the BMS poller and
// the capture sessions (bms_ble._adapter_lock()).
func GlobalAdapterLock() *AdapterLock {
	globalLockOnce.Do(func() { globalLock = NewAdapterLock() })
	return globalLock
}
