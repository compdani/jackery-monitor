# Raspberry Pi deployment (64-bit Linux)

Use a 64-bit Raspberry Pi OS installation and Docker Compose. From the repository:

```sh
docker compose up -d --build
```

The image builds natively for the host architecture, including linux/arm64,
with a static Go binary (`CGO_ENABLED=0`). For an arm64 image built elsewhere:

```sh
docker buildx build --platform linux/arm64 --target go-runtime --load -t jackery-monitor-go:local .
```

The dashboard listens on port 8123 (override JACKERY_HTTP_PORT). Data remains
in the jackery-data volume. Existing Python JSON configuration/history still
requires migration; retain a copy and the original encryption key. Compose
uses `/data/.jackery-creds.key`. Do not initialize a replacement key for
existing encrypted data.

BLE uses tinygo Bluetooth's Linux BlueZ D-Bus adapter, without cgo. Enable the
host's Bluetooth service and adapter; Compose exposes `/var/run/dbus` read-only.
The image uses the host BlueZ service rather than running bluetoothd. Fleet
and inspector mounting in the Go application is still pending: compiling the
adapter does not establish that BLE features are operational. Hardware testing
must be done separately by the owner.

Backups are local PocketBase ZIP archives. Remote SMB/rsync/SSH backup and
network discovery are retired. Local scheduling, retention, and setup restore
remain migration work. Preserve the encryption key separately until verified
key-inclusive backup/recovery is implemented. Do not remove volumes during
updates (`docker compose down -v` deletes persistent data).
