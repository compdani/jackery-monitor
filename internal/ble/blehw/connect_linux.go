package blehw

import (
	"context"
	"strings"
	"time"

	"github.com/godbus/dbus/v5"
	"tinygo.org/x/bluetooth"
)

// Establish the link with a cancellable BlueZ call before asking tinygo to
// wrap it. tinygo's Linux Connect waits for a Connected signal after its
// synchronous Connect call, which can miss the signal and leave a pending call.
func (a *Adapter) connect(ctx context.Context, addr bluetooth.Address) (bluetooth.Device, error) {
	bus, err := dbus.SystemBus()
	if err != nil {
		return bluetooth.Device{}, err
	}
	// New uses bluetooth.DefaultAdapter, which is hci0 on Linux.
	path := dbus.ObjectPath("/org/bluez/hci0/dev_" + strings.ReplaceAll(addr.String(), ":", "_"))
	device := bus.Object("org.bluez", path)
	for attempt := 0; attempt < 3; attempt++ {
		err = device.CallWithContext(ctx, "org.bluez.Device1.Connect", 0).Err
		if err == nil {
			return a.bt.Connect(addr, bluetooth.ConnectionParams{})
		}
		cleanup, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_ = device.CallWithContext(cleanup, "org.bluez.Device1.Disconnect", 0).Err
		cancel()
		if ctx.Err() != nil {
			return bluetooth.Device{}, ctx.Err()
		}
		if !strings.Contains(err.Error(), "le-connection-abort-by-local") || attempt == 2 {
			break
		}
		select {
		case <-ctx.Done():
			return bluetooth.Device{}, ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
	return bluetooth.Device{}, err
}
