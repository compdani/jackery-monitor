//go:build !linux

package blehw

import (
	"context"
	"tinygo.org/x/bluetooth"
)

func (a *Adapter) connect(_ context.Context, addr bluetooth.Address) (bluetooth.Device, error) {
	return a.bt.Connect(addr, bluetooth.ConnectionParams{})
}
