# Go device libraries

Module: `github.com/compdani/jackery-monitor`. These packages do not depend on
PocketBase. The Python implementations remain reference material. Network/radio
operations accept contexts or injectable transports; persistence is exposed as
small interfaces so callers can use their own storage.

| Package | Public entry points | Transport and persistence |
| --- | --- | --- |
| `internal/ble` | `Inspector`, `Options`, `Capture`, `Events`, `WriteJSONExport`; BMS `Poller`, `BuildRead`, `ExtractFrames`, `ParseFrame`, `ParseBasicInfo`, `ParseCellVoltages`, `MergeReading`; `BmsRegistry`, `OverlayTelemetry`, `ApplyGridEstimate`, `WeightedSOC`; `F7`, `EvaluateF7`, daylight helpers | `Adapter`/`Conn` abstract scan, connection, reads and notifications. `BmsStore`/`F7Store` and file implementations store registry/config. Inspector writes JSONL captures. `Sessions.Handler` supplies standalone standard-library HTTP handlers. |
| `internal/ble/blehw` | `New` | Production TinyGo Bluetooth adapter. Hardware/OS permissions are required only when using this adapter. |
| `internal/ecoflow` | `Client`, `ClientOptions`, login/MQTT lifecycle, quota and output commands; `DecodePropertyPayload`, `BuildSetCommand`, `ParamsToTelemetry`; `Registry`, fleet/overlay helpers; `Credentials`, `MergeSave`, reconnect configuration | `HTTPDoer`, `MQTTTransport`/`MQTTSession`, production `PahoTransport`, registry/reconnect stores. Typed `Telemetry` and `Detail` preserve Python JSON names. |
| `internal/ecoflow/efpb` | Generated protobuf message types for Delta 3 | Wire codec only. Generated schema preserves field numbers and unknown fields. |
| `internal/kasa` | `NewClient`, `Discover`, `Status`, `SetState`; `Describe`; `Registry` CRUD, assignment and probe state; typed errors and classification helpers | `Backend`/`Device` support fakes. `NativeBackend` implements legacy TCP XOR and HTTP KLAP. `CredentialsFunc` supplies credentials; `Store`/`FileStore` persist the registry. |
| `internal/siseli` | `NewClient`, `Login`, `RefreshAccessToken`, `ListDevices`, `FetchLatestData`, `FetchEnergyFlow`, `FetchMonthlySummary`, `GetDeviceSettings`, `SetDeviceSetting`; pure mapping, controls, credentials and local APIs described below | `HTTPDoer`, `MQTTTransport`, `SnapshotSink`, `StateStore` isolate I/O. No credential persistence or encryption dependency. |

## Siseli portal

`NewClient(ClientOptions)` accepts credentials, an HTTP implementation, an optional
base URL, clock and token-update callback. Each request carries its context.
Token refresh is serialized; a 401 forces one refresh/login retry. Callbacks run
outside the client mutex. `Credentials()` returns a copy of the current session.
A supplied HTTP transport belongs to the caller.

`FetchLatestData` tries device latest-state, then history when SOC is absent,
then energy flow to fill missing readings. A real zero is preserved. Derived
battery power and load are added last. `MapLatestStateFields` respects explicit
kW/W tags; `MapEnergyFlowFields` implements signed grid and battery-current
rules. `ToTelemetry` returns dashboard dictionary keys, including separate solar
inputs and nullable readings. Dictionary APIs use `Values` (`map[string]any`);
JSON therefore uses the original Python keys directly.

`NormalizeControls`, `DefaultLiveControlKeys`, `ResolveSettingKey` and
`CoerceSettingWriteValue` implement known controls, firmware aliases and dynamic
settings. Setting writes preserve cached string types. `NormalizeDevice` and
SN helpers preserve the `siseli:` namespace. Errors distinguish authentication,
token expiry, portal failures and missing energy-flow configuration.

`Credentials.Normalize`, `Valid`, `Public`, `WithTokens` and `MergeCredentials`
port credential value logic. Partial updates ignore nulls and retain an empty
password; `Public` redacts all three secrets. Struct JSON tags match Python.
Encryption, file permissions, backup inclusion and deletion are deliberately the
persistence caller's responsibility, just as for the EcoFlow credential types.

## Siseli local

`NewLocalClient(LocalConfig, SnapshotSink)` owns state, telemetry cadence, stream
observations and a bounded diagnostic capture queue. `ParsePayload(ctx, Message)`
recursively extracts base64 blocks, decodes readings, merges a snapshot and calls
the sink. `Message.Broker` supplies the broker label; `BrokerIP` filters sink
publication. `DecodedToCanonical` and `ReadingsFromTelemetry` bridge dashboard
values. Probe/freshness/stream description helpers retain Python output text.

`DecodeModbus` validates function-03 length and CRC before interpreting 8eyo
(panel) and WfP8 (battery/current/load) registers. `DecodeASCII` handles the live
battery, PV, output, mains and temperature blocks. `VerifyPI30Frame`,
`DetectPI30`, `DecodePI30` enforce CRC, shape and protocol evidence before reading
the PI30 positional tables. Unknown register rows remain available in captures;
they do not become guessed telemetry. `MQTTStream.Feed` accepts ordered TCP bytes,
buffers fragmented MQTT packets and extracts publishes with bounded memory.

`MQTTTransport.Run` supplies publishes and must exit on context cancellation.
`ChannelTransport` is a channel adapter usable in tests and integration code.
`LocalClient.Run` and `Runner.Start`/`Stop` manage transport lifecycle.
`Snapshot`, `MQTTStreams`, `SetMQTTCapture`, `MQTTCaptures`, `Fresh`, `Running`
and `LastError` expose local status. Capture reads drain the queue.
`StateStore`/`FileStateStore`, `Save` and `Restore` retain decoded readings using
atomic replacement, without restoring liveness timestamps. `SensorRegistered`
accepts every key, matching the Python sensor catalog shim.

## Validation and remaining migration work

The existing BLE, EcoFlow and Kasa suites cover the requested Python registry,
JBD, F7, telemetry, protobuf and reconnect cases. Siseli tests port the portal
mock HTTP scenarios, credential value semantics and local fixtures. The
`testdata/python_parity.json` expectations were captured by running the original
pure-function tests from `test_siseli_client.py` and `test_siseli_local.py` against
Python, including all keys in their resulting dictionaries. A fixed signing
vector also comes from the Python implementation.

This is **not yet a complete replacement for the Python local sniffer**:

- No production raw-packet capture, ARP forwarding/restoration, route/MAC discovery
  or out-of-order TCP sequence reconstruction adapter is included. Callers must
  supply ordered MQTT bytes or decoded publishes through the transport boundary.
- Auxiliary ASCII configuration/BMS/cell diagnostic blocks, Python energy-counter
  accumulation/reset logic, Home Assistant discovery publication, and the full
  environment-variable configuration surface remain to be ported. The included
  ASCII decoder covers live telemetry blocks rather than that entire catalog.
- Automatic heartbeat publication, capture restart supervision, state-cache boot
  identity handling and PI30 diagnostic log formatting are not implemented.
- Local capture, live hardware/cloud endpoints and encrypted credential storage
  were not exercised by these device-package tests. Credential filesystem tests
  are outside the requested pure-types credential boundary.

Run:

```sh
go build ./internal/ble/... ./internal/ecoflow/... ./internal/kasa/... ./internal/siseli/...
go vet ./internal/ble/... ./internal/ecoflow/... ./internal/kasa/... ./internal/siseli/...
go test -race ./internal/ble/... ./internal/ecoflow/... ./internal/kasa/... ./internal/siseli/...
```
