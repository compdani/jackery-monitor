import { useCallback, useEffect, useState } from "react";
import { Alert, Pressable, Text, View } from "react-native";
import { endpoints } from "../api/client";
import type {
  BmsInverterFlag,
  BmsPack,
  BmsScanDevice,
  SiseliControl,
  SiseliCreds,
  SiseliLocalBody,
  SiseliMqttStream,
  SiseliReadings,
  StatusPayload,
} from "../api/types";
import { isSiseliView, patchDevicePrefs, readingsLine, siseliDeviceId, siseliFleet } from "../lib/siseli";
import { colors } from "../theme";
import { SiseliSettingsList } from "./SiseliControls";
import { Btn, Card, Eyebrow, Field, Hint, RowSwitch } from "./ui";

function errText(e: unknown): string {
  return e instanceof Error ? e.message : "request failed";
}

function formatLocalAge(ts: number | null | undefined): string {
  if (!ts) return "no decode yet";
  const sec = Math.max(0, Math.round(Date.now() / 1000 - ts));
  if (sec < 60) return `decoded ${sec}s ago`;
  return `decoded ${Math.round(sec / 60)} min ago`;
}

function localStatusText(j: {
  local_read?: boolean;
  local_error?: string | null;
  local_running?: boolean;
  local_last_decode_ts?: number | null;
}): string {
  if (!j.local_read) return "LAN read off";
  if (j.local_error && !j.local_running) return j.local_error;
  if (j.local_running) return `running · ${formatLocalAge(j.local_last_decode_ts)}`;
  return j.local_error || "stopped";
}

function streamNote(row: SiseliMqttStream): string {
  const watts = readingsLine(row.readings);
  if (watts) return watts;
  if (row.mqtt_packets) return "MQTT publish, not decoded";
  if (row.saw_mqtt) return "MQTT, no data publish yet";
  if (row.payload_bytes) return "TCP data, no MQTT publish";
  return "no data yet";
}

export function SiseliControllerCard({
  controls,
  pins,
  deviceId,
}: {
  controls: SiseliControl[];
  pins: string[];
  deviceId: string;
}) {
  if (!deviceId || !controls.length) return null;
  return (
    <Card>
      <Eyebrow>Controller settings</Eyebrow>
      <SiseliSettingsList controls={controls} pins={pins} deviceId={deviceId} />
    </Card>
  );
}

export function SiseliAccountCards({ status }: { status: StatusPayload | null }) {
  return (
    <>
      <SiseliAccountCard />
      <LanReadCard />
      <BmsCard status={status} />
    </>
  );
}

function SiseliAccountCard() {
  const [userId, setUserId] = useState("");
  const [password, setPassword] = useState("");
  const [stationId, setStationId] = useState("");
  const [timeZone, setTimeZone] = useState("");
  const [status, setStatus] = useState("—");
  const [msg, setMsg] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const load = useCallback(async () => {
    try {
      const j = await endpoints.siseliCreds();
      setUserId(j.user_id || "");
      setStationId(j.station_id || "");
      setTimeZone(j.time_zone || "");
      setStatus(
        j.has_credentials
          ? j.state === "connected"
            ? "connected"
            : j.error || j.state || "saved"
          : "not configured",
      );
    } catch {
      setStatus("unavailable");
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  return (
    <Card>
      <View style={{ flexDirection: "row", justifyContent: "space-between", alignItems: "center" }}>
        <Eyebrow>Siseli account</Eyebrow>
        <Hint>{status}</Hint>
      </View>
      <Hint>
        Optional house-inverter connection via solar.siseli.com. Station ID is the numeric stationId
        from the portal Network tab, not the inverter serial.
      </Hint>
      <Field label="User ID" value={userId} onChangeText={setUserId} autoComplete="username" />
      <Field
        label="Password"
        value={password}
        onChangeText={setPassword}
        secureTextEntry
        placeholder="unchanged if blank"
      />
      <Field label="Station ID" value={stationId} onChangeText={setStationId} keyboardType="number-pad" />
      <Field
        label="Time zone"
        value={timeZone}
        onChangeText={setTimeZone}
        placeholder="uses Forecast location if set"
      />
      {msg ? <Hint>{msg}</Hint> : null}
      <Btn
        title="Save credentials"
        loading={busy}
        onPress={() => {
          setBusy(true);
          setMsg(null);
          void endpoints
            .saveSiseliCreds({
              user_id: userId.trim(),
              password,
              station_id: stationId.trim(),
              time_zone: timeZone.trim(),
            })
            .then((j) => {
              setPassword("");
              setStatus(`saved · ${j.device_count || 0} device(s)`);
              setMsg("Saved. Polling the portal…");
            })
            .catch((e: unknown) => {
              setStatus("error");
              setMsg(errText(e));
            })
            .finally(() => setBusy(false));
        }}
      />
      <Btn
        title="Forget"
        kind="ghost"
        onPress={() => {
          Alert.alert("Forget Siseli credentials?", "Inverters will drop from the fleet.", [
            { text: "Cancel", style: "cancel" },
            {
              text: "Forget",
              style: "destructive",
              onPress: () => {
                void endpoints.forgetSiseliCreds().then(() => {
                  setPassword("");
                  setStatus("not configured");
                  setMsg(null);
                });
              },
            },
          ]);
        }}
      />
    </Card>
  );
}

function LanReadCard() {
  const [on, setOn] = useState(false);
  const [inverterIp, setInverterIp] = useState("");
  const [routerIp, setRouterIp] = useState("");
  const [sniffIface, setSniffIface] = useState("");
  const [inverterMac, setInverterMac] = useState("");
  const [routerMac, setRouterMac] = useState("");
  const [brokerIp, setBrokerIp] = useState("");
  const [streams, setStreams] = useState<SiseliMqttStream[]>([]);
  const [status, setStatus] = useState("LAN read off");
  const [readings, setReadings] = useState<SiseliReadings | null>(null);
  const [busy, setBusy] = useState<"save" | "test" | null>(null);

  const apply = useCallback((j: SiseliCreds) => {
    setOn(!!j.local_read);
    setInverterIp(j.inverter_ip || "");
    setRouterIp(j.router_ip || "");
    setSniffIface(j.sniff_iface || "");
    setInverterMac(j.inverter_mac || "");
    setRouterMac(j.router_mac || "");
    setBrokerIp(j.mqtt_broker_ip || "");
    setStreams(Array.isArray(j.mqtt_streams) ? j.mqtt_streams : []);
    setReadings(j.local_readings || null);
    setStatus(localStatusText(j));
  }, []);

  useEffect(() => {
    void endpoints.siseliCreds().then(apply).catch(() => setStatus("unavailable"));
  }, [apply]);

  function body(): SiseliLocalBody {
    return {
      local_read: on,
      inverter_ip: inverterIp.trim(),
      router_ip: routerIp.trim(),
      sniff_iface: sniffIface.trim(),
      inverter_mac: inverterMac.trim(),
      router_mac: routerMac.trim(),
      mqtt_broker_ip: brokerIp.trim(),
    };
  }

  const visibleStreams = (() => {
    const list = streams.slice();
    if (brokerIp && !list.some((row) => row.ip === brokerIp && !row.encrypted)) {
      list.unshift({ ip: brokerIp, port: 1883, encrypted: false });
    }
    return list.filter((row) => row.ip);
  })();
  const line = readingsLine(readings);

  return (
    <Card>
      <View style={{ flexDirection: "row", justifyContent: "space-between", alignItems: "center" }}>
        <Eyebrow>LAN read</Eyebrow>
        <Btn title={on ? "on" : "off"} kind={on ? "primary" : "ghost"} onPress={() => setOn((v) => !v)} />
      </View>
      <Hint>
        Sniff the inverter dongle’s MQTT on the local network for Live watts. The portal is still
        used for login, the device list, history, and settings. The NAS must be on the same network.
      </Hint>
      <Field label="Inverter IP" value={inverterIp} onChangeText={setInverterIp} placeholder="192.168.1.50" keyboardType="decimal-pad" />
      <Field label="Router IP" value={routerIp} onChangeText={setRouterIp} placeholder="192.168.1.1" keyboardType="decimal-pad" />
      <Field label="Sniff interface" value={sniffIface} onChangeText={setSniffIface} placeholder="blank = auto" />
      <Field label="Inverter MAC" value={inverterMac} onChangeText={setInverterMac} placeholder="blank = auto" autoCapitalize="none" />
      <Field label="Router MAC" value={routerMac} onChangeText={setRouterMac} placeholder="blank = auto" autoCapitalize="none" />
      <Hint>{status}</Hint>
      {visibleStreams.length ? (
        <View style={{ gap: 8 }}>
          {visibleStreams.map((row) => {
            const label = `${row.ip}:${row.port || 1883}`;
            if (row.encrypted) {
              return (
                <Hint key={`enc-${label}`}>{label} encrypted, cannot decode</Hint>
              );
            }
            const selected = row.ip === brokerIp;
            return (
              <Pressable
                key={label}
                onPress={() => setBrokerIp(row.ip || "")}
                style={{
                  padding: 10,
                  borderRadius: 10,
                  borderWidth: 1,
                  borderColor: selected ? colors.accent : colors.border,
                }}
              >
                <Text style={{ color: colors.text, fontWeight: "600" }}>{label}</Text>
                <Text style={{ color: colors.textDim, fontSize: 12 }}>{streamNote(row)}</Text>
              </Pressable>
            );
          })}
        </View>
      ) : null}
      {line ? <Hint>{line}</Hint> : null}
      <Btn
        title="Save LAN read"
        loading={busy === "save"}
        disabled={busy != null}
        onPress={() => {
          setBusy("save");
          setStatus("Saving…");
          void endpoints
            .saveSiseliLocal(body())
            .then((j) => {
              apply(j);
              if (j.local_read && j.local_running) {
                setStatus(`LAN read saved · ${localStatusText(j)}`);
              } else if (j.local_error && !j.local_running) {
                setStatus(j.local_error);
              } else {
                setStatus(j.local_read ? "LAN read saved." : "LAN read turned off.");
              }
            })
            .catch((e: unknown) => setStatus(errText(e)))
            .finally(() => setBusy(null));
        }}
      />
      <Btn
        title="Test MQTT"
        kind="ghost"
        loading={busy === "test"}
        disabled={busy != null}
        onPress={() => {
          setBusy("test");
          setStatus("Listening for up to 25 seconds…");
          void endpoints
            .testSiseliLocal(body())
            .then(async (j) => {
              const creds = await endpoints.siseliCreds().catch(() => null);
              if (creds) apply(creds);
              setStatus(j.detail || "Test finished.");
              if (j.readings) setReadings(j.readings);
            })
            .catch((e: unknown) => setStatus(errText(e)))
            .finally(() => setBusy(null));
        }}
      />
    </Card>
  );
}

function BmsCard({ status }: { status: StatusPayload | null }) {
  const fleet = siseliFleet(status);
  const fleetKey = fleet.map((d) => d.sn).join("\n");
  const viewing = siseliDeviceId(status);
  const viewingSiseli = isSiseliView(status);
  const ignoreSoc = !!status?.device_prefs?.ignore_inverter_soc;
  const calcGrid = !!status?.device_prefs?.calc_grid;
  const [packs, setPacks] = useState<BmsPack[]>([]);
  const [inverters, setInverters] = useState<Record<string, BmsInverterFlag>>({});
  const [bleStatus, setBleStatus] = useState("—");
  const [mac, setMac] = useState("");
  const [alias, setAlias] = useState("");
  const [capacity, setCapacity] = useState("");
  const [assignSn, setAssignSn] = useState("");
  const [msg, setMsg] = useState<string | null>(null);
  const [scan, setScan] = useState<BmsScanDevice[] | null>(null);
  const [scanning, setScanning] = useState(false);
  const [useMain, setUseMain] = useState(true);

  const load = useCallback(async () => {
    try {
      const j = await endpoints.bmsSaved();
      const nextPacks = j.packs || [];
      const nextInv = j.inverters || {};
      setPacks(nextPacks);
      setInverters(nextInv);
      const ble = j.ble || {};
      setBleStatus(ble.available === false ? ble.error || "BLE unavailable" : `${nextPacks.length} pack(s)`);
      return nextInv;
    } catch {
      setBleStatus("unavailable");
      return null;
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  useEffect(() => {
    const sns = fleetKey ? fleetKey.split("\n") : [];
    setAssignSn((cur) => cur || (sns.includes(viewing) ? viewing : ""));
  }, [viewing, fleetKey]);

  useEffect(() => {
    const sn = assignSn;
    const inv = sn ? inverters[sn] : null;
    setUseMain(inv ? inv.use_as_main !== false : true);
  }, [assignSn, inverters]);

  const mainSn = fleet.some((d) => d.sn === assignSn) ? assignSn : fleet.some((d) => d.sn === viewing) ? viewing : "";

  return (
    <Card>
      <View style={{ flexDirection: "row", justifyContent: "space-between", alignItems: "center" }}>
        <Eyebrow>Bluetooth BMS</Eyebrow>
        <Hint>{bleStatus}</Hint>
      </View>
      <Hint>
        Overkill / JBD / XiaoXiang packs assigned to a Siseli inverter replace the portal SOC as the
        Live battery headline. Scan needs a USB Bluetooth adapter on the NAS. If scan finds nothing,
        type the MAC.
      </Hint>
      <RowSwitch
        label="Use as main battery display"
        value={useMain}
        disabled={!mainSn}
        onValueChange={(next) => {
          if (!mainSn) {
            setMsg("Pick a Siseli inverter first.");
            return;
          }
          setUseMain(next);
          void endpoints.bmsUseAsMain(mainSn, next).catch((e: unknown) => {
            setUseMain(!next);
            setMsg(errText(e));
          });
        }}
      />
      {viewingSiseli ? (
        <>
          <RowSwitch
            label="Ignore inverter SOC"
            hint="Siseli inverter SOC is voltage-based and often disagrees with the BMS. When on, Live and history use pack SOC only."
            value={ignoreSoc}
            onValueChange={(next) => {
              const id = siseliDeviceId(status);
              if (!id) return;
              patchDevicePrefs({ ignore_inverter_soc: next });
              void endpoints.saveDevicePrefs({ device_id: id, ignore_inverter_soc: next }).catch((e: unknown) => {
                patchDevicePrefs({ ignore_inverter_soc: !next });
                setMsg(errText(e));
              });
            }}
          />
          <RowSwitch
            label="Calculate grid"
            hint="Estimates grid watts on this dashboard when solar plus battery discharge is less than the load. Does not write anything to Siseli."
            value={calcGrid}
            onValueChange={(next) => {
              const id = siseliDeviceId(status);
              if (!id) return;
              patchDevicePrefs({ calc_grid: next });
              void endpoints.saveDevicePrefs({ device_id: id, calc_grid: next }).catch((e: unknown) => {
                patchDevicePrefs({ calc_grid: !next });
                setMsg(errText(e));
              });
            }}
          />
        </>
      ) : null}
      {packs.length ? (
        packs.map((p) => {
          const soc = p.reading?.soc_pct != null ? `${Math.round(p.reading.soc_pct)}%` : "—";
          const assigned = fleet.find((d) => d.sn === p.siseli_device_sn);
          const assign = p.siseli_device_sn ? assigned?.name || p.siseli_device_sn : "unassigned";
          const cap = p.capacity_wh ? `${p.capacity_wh} Wh` : "capacity unset";
          return (
            <View key={p.mac} style={{ gap: 4, paddingVertical: 8 }}>
              <Text style={{ color: colors.text, fontWeight: "700" }}>
                {soc} · {p.alias || p.mac}
              </Text>
              <Hint>
                {p.mac} · {cap} · → {assign}
              </Hint>
              {p.error || p.reading?.error ? (
                <Text style={{ color: colors.danger, fontSize: 12 }}>{String(p.error || p.reading?.error)}</Text>
              ) : null}
              <View style={{ flexDirection: "row", gap: 8 }}>
                <Btn
                  title="Edit"
                  kind="ghost"
                  onPress={() => {
                    setMac(p.mac || "");
                    setAlias(p.alias || "");
                    setCapacity(p.capacity_wh ? String(p.capacity_wh) : "");
                    setAssignSn(p.siseli_device_sn || "");
                  }}
                />
                <Btn
                  title="Delete"
                  kind="ghost"
                  onPress={() => {
                    Alert.alert("Remove BMS", `Remove ${p.mac}?`, [
                      { text: "Cancel", style: "cancel" },
                      {
                        text: "Remove",
                        style: "destructive",
                        onPress: () => {
                          void endpoints.deleteBmsPack(p.mac).then(() => load());
                        },
                      },
                    ]);
                  }}
                />
              </View>
            </View>
          );
        })
      ) : (
        <Hint>No BMS packs saved. Scan or enter a MAC.</Hint>
      )}
      <Field label="MAC" value={mac} onChangeText={setMac} placeholder="AA:BB:CC:DD:EE:FF" autoCapitalize="characters" />
      <Field label="Alias" value={alias} onChangeText={setAlias} placeholder="Pack 1" />
      <Field label="Capacity (Wh)" value={capacity} onChangeText={setCapacity} keyboardType="number-pad" placeholder="5120" />
      <Text style={{ color: colors.textDim, fontSize: 12, fontWeight: "600" }}>Siseli inverter</Text>
      <View style={{ flexDirection: "row", flexWrap: "wrap", gap: 8 }}>
        <Pressable
          onPress={() => setAssignSn("")}
          style={{
            paddingHorizontal: 10,
            paddingVertical: 8,
            borderRadius: 10,
            borderWidth: 1,
            borderColor: assignSn === "" ? colors.accent : colors.border,
          }}
        >
          <Text style={{ color: colors.text }}>Unassigned</Text>
        </Pressable>
        {fleet.map((d) => (
          <Pressable
            key={d.sn}
            onPress={() => setAssignSn(d.sn)}
            style={{
              paddingHorizontal: 10,
              paddingVertical: 8,
              borderRadius: 10,
              borderWidth: 1,
              borderColor: assignSn === d.sn ? colors.accent : colors.border,
            }}
          >
            <Text style={{ color: colors.text }}>{d.name}</Text>
          </Pressable>
        ))}
      </View>
      {msg ? <Hint>{msg}</Hint> : null}
      <Btn
        title="Save pack"
        onPress={() => {
          setMsg("Saving…");
          void endpoints
            .saveBmsPack({
              mac: mac.trim(),
              alias: alias.trim(),
              capacity_wh: capacity.trim() ? Number(capacity) : null,
              siseli_device_sn: assignSn,
            })
            .then(() => {
              setMsg("Saved.");
              setMac("");
              setAlias("");
              setCapacity("");
              return load();
            })
            .catch((e: unknown) => setMsg(errText(e)));
        }}
      />
      <Btn
        title="Scan BLE"
        kind="ghost"
        loading={scanning}
        onPress={() => {
          setScanning(true);
          setScan([]);
          setMsg("Scanning Bluetooth (~8s)…");
          void endpoints
            .bmsScan(8)
            .then((j) => {
              if (j.ble_available === false) {
                const err = j.error || "BLE unavailable on this host.";
                setMsg(err);
                setBleStatus("BLE unavailable");
                setScan(null);
                return;
              }
              const devices = j.devices || [];
              setScan(devices);
              if (!devices.length) {
                setMsg(j.error || "No Bluetooth devices seen. Type the MAC.");
                return;
              }
              const likely = devices.filter((d) => d.likely_jbd).length;
              setMsg(
                likely
                  ? `${likely} likely JBD/Overkill · ${devices.length} nearby`
                  : `${devices.length} nearby BLE device(s). None advertised a JBD name — pick the pack MAC.`,
              );
            })
            .catch((e: unknown) => setMsg(errText(e)))
            .finally(() => setScanning(false));
        }}
      />
      <Hint>Takes ~8s. Lists nearby Bluetooth devices so you can pick a MAC.</Hint>
      {scan?.map((d) => (
        <View key={d.mac} style={{ flexDirection: "row", alignItems: "center", gap: 8, paddingVertical: 4 }}>
          <View style={{ flex: 1 }}>
            <Text style={{ color: colors.text }}>
              {d.name || d.mac}
              {d.likely_jbd ? " · JBD?" : ""}
            </Text>
            <Hint>
              {d.mac}
              {d.rssi != null ? ` · ${d.rssi} dBm` : ""}
            </Hint>
          </View>
          <Btn
            title="Use"
            kind="ghost"
            onPress={() => {
              setMac(d.mac);
              if (!alias && d.name && d.name !== d.mac) setAlias(d.name);
            }}
          />
        </View>
      ))}
    </Card>
  );
}
