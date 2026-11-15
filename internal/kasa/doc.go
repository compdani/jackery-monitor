// Package kasa controls TP-Link Kasa smart plugs by IP address.
//
// It ports kasa_client.py and kasa_devices.py. The package has no PocketBase
// dependency: credentials are supplied through a CredentialsFunc, and the
// device registry persists through a Store interface (a JSON FileStore is
// provided, matching the Python /data/kasa_devices.json layout).
//
// Transports are pluggable through the Backend interface. NativeBackend
// speaks the legacy XOR protocol (TCP 9999, HS/KP plugs) and KLAP over HTTP
// (KASA SMART line: KP125M, EP25, KP405, ...). Tests use fakes.
package kasa
