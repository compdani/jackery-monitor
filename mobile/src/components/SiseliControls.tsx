import { useEffect, useState } from "react";
import { Text, View } from "react-native";
import { endpoints } from "../api/client";
import type { SiseliControl } from "../api/types";
import { applySiseliControls, patchDevicePrefs, pinnedControls } from "../lib/siseli";
import { colors } from "../theme";
import { Btn, Field, Hint, RowSwitch, Segmented } from "./ui";

function errText(e: unknown): string {
  return e instanceof Error ? e.message : "save failed";
}

function switchWriteValue(c: SiseliControl, on: boolean): number | string {
  const raw = on ? c.on_value : c.off_value;
  const n = Number(raw);
  return Number.isFinite(n) ? n : String(raw ?? (on ? 1 : 0));
}

export async function writeSiseliSetting(
  deviceId: string,
  key: string,
  value: number | string,
): Promise<void> {
  const j = await endpoints.setSiseliSetting(deviceId, key, value);
  if (Array.isArray(j.controls)) applySiseliControls(j.controls);
}

export async function saveLivePins(deviceId: string, live_controls: string[]): Promise<void> {
  await endpoints.saveDevicePrefs({ device_id: deviceId, live_controls });
  patchDevicePrefs({ live_controls });
}

export function SiseliLiveControls({
  controls,
  pins,
  deviceId,
}: {
  controls: SiseliControl[] | null | undefined;
  pins: string[] | null | undefined;
  deviceId: string;
}) {
  const list = pinnedControls(controls, pins);
  const [status, setStatus] = useState(list.length ? `${list.length} on Live` : "");
  if (!list.length || !deviceId) return null;
  return (
    <View style={{ gap: 10 }}>
      <Hint>Writes go to the Siseli portal. Pin which controls appear here from the Device tab.</Hint>
      {status ? <Hint>{status}</Hint> : null}
      {list.map((c) => (
        <ControlEditor
          key={c.canonical}
          control={c}
          onStatus={setStatus}
          onWrite={(key, value) => writeSiseliSetting(deviceId, key, value)}
        />
      ))}
    </View>
  );
}

export function SiseliSettingsList({
  controls,
  pins,
  deviceId,
}: {
  controls: SiseliControl[];
  pins: string[];
  deviceId: string;
}) {
  const [status, setStatus] = useState(`${controls.length} fields`);
  return (
    <View style={{ gap: 12 }}>
      <Hint>
        All writable inverter fields. Turn Live on to show a control on the Live page. Known
        switches and limits save on change; other firmware fields use Send.
      </Hint>
      {status ? <Hint>{status}</Hint> : null}
      {controls.map((c) => {
        const pinned = pins.includes(c.canonical);
        return (
          <View key={c.canonical} style={{ gap: 6, paddingBottom: 8, borderBottomWidth: 1, borderBottomColor: colors.borderSoft }}>
            <ControlEditor
              control={c}
              onStatus={setStatus}
              onWrite={(key, value) => writeSiseliSetting(deviceId, key, value)}
            />
            <RowSwitch
              label="Live"
              value={pinned}
              onValueChange={(on) => {
                const next = on
                  ? [...pins.filter((k) => k !== c.canonical), c.canonical]
                  : pins.filter((k) => k !== c.canonical);
                patchDevicePrefs({ live_controls: next });
                setStatus("saving…");
                void saveLivePins(deviceId, next)
                  .then(() => setStatus("saved"))
                  .catch((e: unknown) => {
                    patchDevicePrefs({ live_controls: pins });
                    setStatus(errText(e));
                  });
              }}
            />
          </View>
        );
      })}
    </View>
  );
}

function ControlEditor({
  control,
  onWrite,
  onStatus,
}: {
  control: SiseliControl;
  onWrite: (key: string, value: number | string) => Promise<void>;
  onStatus: (s: string) => void;
}) {
  const name = control.name || control.canonical;
  const unit = control.unit ? ` (${control.unit})` : "";

  async function commit(value: number | string) {
    onStatus("saving…");
    try {
      await onWrite(control.canonical, value);
      onStatus("saved");
    } catch (e) {
      onStatus(errText(e));
    }
  }

  if (control.kind === "switch") {
    const on = !!control.value;
    return (
      <RowSwitch
        label={name}
        hint={control.hint || undefined}
        value={on}
        onValueChange={(next) => void commit(switchWriteValue(control, next))}
      />
    );
  }

  if (control.kind === "select") {
    const options = (control.options || []).map((o) => ({ id: String(o.value), label: o.label }));
    return (
      <View style={{ gap: 6 }}>
        <Text style={{ color: colors.textDim, fontSize: 12, fontWeight: "600" }}>{name}</Text>
        {control.hint ? <Hint>{control.hint}</Hint> : null}
        <Segmented
          options={options}
          value={String(control.value ?? "")}
          onChange={(id) => void commit(Number(id))}
        />
      </View>
    );
  }

  return (
    <NumberField
      label={`${name}${unit}`}
      hint={control.hint}
      value={control.value}
      dynamic={!!control.dynamic}
      onCommit={(n) => commit(n)}
    />
  );
}

function NumberField({
  label,
  hint,
  value,
  dynamic,
  onCommit,
}: {
  label: string;
  hint?: string;
  value: SiseliControl["value"];
  dynamic: boolean;
  onCommit: (n: number) => Promise<void>;
}) {
  const server = value == null ? "" : String(value);
  const [text, setText] = useState(server);
  const [dirty, setDirty] = useState(false);

  useEffect(() => {
    if (!dirty) setText(server);
  }, [server, dirty]);

  return (
    <View style={{ gap: 8 }}>
      <Field
        label={label}
        value={text}
        keyboardType="decimal-pad"
        onChangeText={(v) => {
          setText(v);
          setDirty(v !== server);
        }}
        onEndEditing={() => {
          if (dynamic || !dirty) return;
          const n = Number(text);
          if (!Number.isFinite(n)) return;
          void onCommit(n).then(() => setDirty(false));
        }}
      />
      {hint ? <Hint>{hint}</Hint> : null}
      {dynamic ? (
        <View style={{ flexDirection: "row", gap: 8 }}>
          <Btn
            title="Reset"
            kind="ghost"
            onPress={() => {
              setText(server);
              setDirty(false);
            }}
          />
          <Btn
            title="Send"
            onPress={() => {
              const n = Number(text);
              if (!Number.isFinite(n)) return;
              void onCommit(n).then(() => setDirty(false));
            }}
          />
        </View>
      ) : null}
    </View>
  );
}
