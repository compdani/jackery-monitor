// Package ble ports the Bluetooth side of the monitor: the Xiaoxiang / JBD /
// Overkill BMS codec and poller, the BMS pack registry + Siseli SOC overlay,
// the F7 AC-reset gate for Explorer 2000 Plus, and the read-only BLE
// inspector with its capture API.
//
// The package is pure Go. All radio access goes through the Adapter / Conn
// interfaces (see adapter.go); a real implementation using
// tinygo.org/x/bluetooth lives in the blehw sub-package so that importing
// this package never requires cgo or a Bluetooth stack. Tests use fakes.
package ble
