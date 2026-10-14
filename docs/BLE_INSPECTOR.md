# BLE Inspector for Raspberry Pi

Use the **BLE Inspector** dashboard tab (`/#ble`) or `ble_inspector.py` to investigate a nearby Sileli Bluetooth module attached to a JJN MPPT controller. The dashboard server must run on the Pi: a browser on your laptop uses the **server's** adapter, not the laptop's Bluetooth.

This tool performs BLE advertisement scanning and GATT-client inspection. It is **not a passive over-the-air packet sniffer**. BlueZ/Bleak exposes parsed advertisement fields; these are not complete radio packets. Scanning is active and may request scan responses. Connecting and reading generate BLE traffic.

There are no characteristic-value writes, descriptor-write calls, controller commands, setting changes, protocol replay, or automatic pairing. Structure discovery is the default; reads require `--read-all` or the dashboard checkbox. Notifications require explicit characteristic selection. Starting notifications lets BlueZ configure the client subscription descriptor (CCCD); this is the sole intentional subscription configuration, not a controller command.

## Raspberry Pi OS setup

Use Python 3.11 or newer for this repository and current Raspberry Pi OS with BlueZ 5.55 or newer. From the repository directory:

```bash
sudo apt update
sudo apt install bluez dbus rfkill python3-venv python3-pip
sudo systemctl enable --now bluetooth
systemctl status bluetooth --no-pager
rfkill list bluetooth
sudo rfkill unblock bluetooth
bluetoothctl list
bluetoothctl show
bluetoothctl power on

python3 -m venv .venv
source .venv/bin/activate
# CLI alone:
python -m pip install 'bleak>=0.22'
# Full dashboard instead:
python -m pip install -r requirements.txt
```

Run the CLI as your ordinary user. Bleak talks to the system `bluetoothd` over D-Bus; it does not require raw HCI socket privileges. Access depends on the OS D-Bus/BlueZ policy and the user running the process. If access is denied, check `journalctl -u bluetooth`, system D-Bus policy, and the service user's group membership. If your distribution's policy uses a `bluetooth` group, add that user with `sudo usermod -aG bluetooth USERNAME` and log out/in or restart its service. Group membership alone is not a universal fix. Do not run pip with sudo or grant broad capabilities to Python.

Missing adapter/power, permission, dependency, discovery, and connection errors appear in the event capture and dashboard. An installed Bleak package alone does not prove the adapter is usable. Keep the module powered and nearby; disconnect the vendor app before testing. **Connecting here may prevent the vendor phone app from connecting simultaneously.**

For Docker, the existing Compose configuration mounts `/var/run/dbus:/var/run/dbus:ro`; host `bluetoothd` and its adapter must be available, and its D-Bus policy must permit the container process. The container does not run its own Bluetooth service. Native Pi execution is simplest for diagnosis. Run one dashboard server worker per adapter: the shared BMS/inspector lock coordinates only operations within that process. Stop competing CLI tools, dashboard instances, or other Bluetooth clients while using the standalone CLI.

## CLI examples

```bash
python ble_inspector.py --help
python ble_inspector.py scan --help
python ble_inspector.py inspect --help
python ble_inspector.py scan --seconds 15
python ble_inspector.py scan --name Sileli --output scan.json
python ble_inspector.py scan --address AA:BB:CC:DD:EE:FF --output scan.jsonl
python ble_inspector.py inspect --address AA:BB:CC:DD:EE:FF
python ble_inspector.py inspect --name Sileli --read-all --output reads.json
python ble_inspector.py inspect --address AA:BB:CC:DD:EE:FF \
  --notify CHARACTERISTIC_UUID --duration 60 --output capture.jsonl
# Select multiple characteristics, or use a decimal / 0x-prefixed handle:
python ble_inspector.py inspect --address AA:BB:CC:DD:EE:FF \
  --notify 0x12 --notify 23 --duration 30 --output notifications.jsonl
```

Replace example addresses, UUIDs, and handles with values from your own scan/inspection. Names match case-insensitive substrings; addresses match exactly, ignoring case. Inspection always performs a fresh scan and requires exactly one match; ambiguous names produce candidate addresses. Unknown names remain null, and addresses can rotate. Rescan and select the current observation; the tool does not claim that two addresses belong to the same module. UUIDs repeated within a GATT database require handle selectors.

Scans default to 10 seconds (1–120). Notification duration defaults to 30 seconds (1–3600). `--timeout` bounds connection and individual GATT operations, defaulting to 15 seconds (1–120). Only readable characteristics are read, and individual read failures remain in the capture. Read rejection (including `READ_NOT_PERMITTED`) does not prevent subscriptions; uncheck reads or omit `--read-all` for a subscription-only capture. Write-only characteristics are listed without accessing their values. Reads finish before notifications start. Ctrl+C cancels the operation and closes the scanner/client; there is no automatic reconnect. A silent notification stream is valid: some modules only report after a proprietary request, which this tool deliberately will not send. Read access can also require authentication or be denied despite advertised properties.

Events print as JSON lines, including device fields and raw payloads. Without `--output`, a timestamp-named JSONL file is created in the current directory. Existing files are never overwritten. JSON outputs retain an append-only `OUTPUT.json.jsonl` recovery journal as well as the final JSON document. If interrupted before JSON export, use that journal. An incomplete operation returns a nonzero exit code; per-characteristic read errors can coexist with successful completion.

## Dashboard workflow

1. Open **BLE Inspector**, optionally enter scan filters, and click **Scan devices**.
2. Select a device row (including unnamed devices) or enter its address. Click **Inspect GATT** to list structure without reads or subscriptions.
3. Optionally enable the independent **Read readable characteristics** checkbox. Explicitly check **Subscribe to events** on each notify/indicate characteristic, or click **Select all subscribable**. **Clear selections** unchecks every channel. Inspecting a device never checks channels automatically. Review the selected UUIDs/handles and listening duration, then click **Start selected capture**. With no selections, it only discovers structure and optionally reads.
4. The capture retains its requested channels and duration while showing each subscription as requested, active, or failed, with a received-event count. A rejected subscription does not prevent attempts on the remaining selected channels. Successful channels continue listening, but any subscription failure makes the final capture incomplete.
5. Click **Stop** to end early. Changing tabs does not stop a capture. Reopening the page can recover the active session.
6. Use saved capture **View**, **JSON**, **JSONL**, or **Delete** controls. Stop an active capture before downloading or deleting it.

The inspector shares the adapter lock with existing BMS scanning/polling. A busy adapter returns a clear conflict; retry after the current operation finishes. Long captures postpone BMS polling. The live event viewer and device list are bounded to 200 entries; the complete journal remains on disk. Handles selected in the page are checked against their previously discovered UUID before subscribing; inspect again if the structure changes.

Dashboard captures live under `$JACKERY_DATA_DIR/ble-captures` (default `/data/ble-captures`), persist across restart, and remain until explicitly deleted. Each capture stops at 100 MB. Storage errors mark the live operation incomplete where a final record can be written. If the disk cannot accept that record, or the process loses power, a missing completion record denotes an incomplete capture. The saved-capture list labels such files incomplete. Free disk space and retain the journal for analysis.

## Verifying event subscriptions

For the module observed at `74:E9:D8:A9:5B:25`, this is a **subscription-only** example for the three candidate channels. Verify the current address and these full UUIDs against a fresh GATT inspection; neither the address nor this characteristic map is assumed for other devices:

```bash
python ble_inspector.py inspect --address 74:E9:D8:A9:5B:25 \
  --notify 0000fec8-0000-1000-8000-00805f9b34fb \
  --notify 0000fed4-0000-1000-8000-00805f9b34fb \
  --notify 0000fed6-0000-1000-8000-00805f9b34fb \
  --duration 60 --output sileli-events.jsonl
```

`--notify` supports both notify and indicate characteristics. The tool records the advertised supported properties; Bleak's callback does not identify the actual delivery mode for a channel supporting both. The existing raw `notification` event format is used for either callback, without guessing the mode. No application initialization or request commands are sent. Subscribing lets BlueZ configure the BLE subscription descriptor and receives data only on the Pi's connection.

New additive events in schema version 1 make the subscription outcome auditable:

| Event | Evidence recorded |
| --- | --- |
| `subscription_requested` | Explicitly selected UUID, handle, supported subscription properties, and requested state |
| `subscription_active` | Bleak successfully returned from enabling the subscription; received-event count so far |
| `subscription_error` | Failed subscription, error/timeout text, and received-event count; other channels are still attempted |
| `listening_started` | Confirmed active channels and requested listening duration |
| `unsubscribe_result` | Per-channel stop result, error if any, and received-event count |
| `listening_ended` | Actual listening seconds, termination reason, successful/failed channel totals, total received-event count, and final per-channel results |

Each event has its own UTC timestamp and sequence number. A requested channel is not evidence of an active subscription; look for `subscription_active`. If cancellation interrupts activation, its final state is `interrupted` because success was not confirmed. Channels whose attempts never started are `not_attempted`.

The listening timer begins after all selected subscription attempts finish, excluding discovery, reads, subscription setup, and unsubscribe cleanup. Event counts include every callback received during subscription setup, listening, and cleanup, with empty payloads counted as events. Raw payload records remain unchanged. Storage failure can prevent new payload/summary records from being saved; retain the flushed partial journal and treat an absent final summary as unconfirmed, rather than assuming zero events.

The dashboard distinguishes these outcomes:

- **No subscriptions selected:** no channel was requested and listening time is zero.
- **All subscriptions failed:** each failure is recorded, listening never starts, and the capture is incomplete.
- **Subscriptions succeeded, zero events:** activation was confirmed and the actual listening window ended without events. This is a valid successful result when no subscription or cleanup failed; it does not supply controller telemetry.
- **Some subscriptions failed:** successful channels continue listening; the capture still finishes incomplete, even if they receive data.

Termination reasons include duration expiry, Stop/Ctrl+C/server shutdown, disconnect, storage failure, and operation failure. The summary and unsubscribe results are flushed whenever storage permits, including early Stop. A cleanup failure also makes a normally completed session incomplete. The dashboard keeps the result and per-channel counts visible after the session ends. The tool cannot guarantee that a proprietary module will stream spontaneously; it deliberately does not write controller characteristic values to provoke data.

## Identifying the module and sharing evidence

Do not assume a particular name, manufacturer ID, or service UUID identifies Sileli. Start with an unfiltered scan. Compare repeated scans close to the controller and, when appropriate for your installation, compare observations before and after powering just the Bluetooth module off/on. RSSI is a proximity clue, not proof. Record the module/controller model, firmware if known, and whether the phone app was disconnected. Similar names or rotating addresses are reasons to verify manually.

Capture a structure-only inspection, readable values, and a short explicit notification session. Note the actual start/end time and controller conditions (for example displayed battery voltage and solar watts). Preserve each original file and share the JSONL journal or JSON export plus those notes for protocol analysis. Captures contain nearby device identifiers and potentially private payloads; review before sharing. If redaction is needed, share a clearly labeled copy and retain the original locally.

Schema version 1 uses sequenced UTC-timestamped metadata, advertisement, scan summary, target, service, read/read-error, raw notification, subscription/listening/unsubscribe lifecycle, error, and completion events. Characteristic records include UUID and handle. Manufacturer data keys are decimal company IDs and service data keys are UUIDs. Each binary value contains reversible `hex`, `length`, and a **UTF-8 interpretation guess** with invalid bytes escaped. JSON also escapes control characters. Empty payloads are preserved. No unknown bytes are discarded, frames merged, register map assumed, or guessed text used to replace raw bytes. BlueZ/Bleak may aggregate or suppress radio observations before callbacks reach this tool; callback records do not establish packet-level completeness.

## Optional phone-app traffic investigation

A separate BLE hardware sniffer with Wireshark can investigate over-the-air exchanges between the vendor app and module. It is not a dependency of this tool. Capture success depends on following the connection, radio support, starting capture at the right time, and encryption/key availability; a sniffer does not guarantee readable application payloads. The inspector's Pi connection cannot passively observe another central's private GATT exchange.

API reference: [Bleak scanner](https://bleak.readthedocs.io/en/latest/api/scanner.html) and [Bleak client](https://bleak.readthedocs.io/en/latest/api/client.html).
