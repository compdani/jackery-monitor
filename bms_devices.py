"""
Persisted registry of Bluetooth BMS packs assigned to Siseli inverters.

Storage: /data/bms_devices.json. Format:
{
  "packs": [
    {"mac": "AA:BB:...", "alias": "Pack 1", "capacity_wh": 5120,
     "siseli_device_sn": "siseli:42", "added": <unix-ts>}
  ],
  "inverters": {
    "siseli:42": {"use_as_main": true}
  }
}

Live readings live in server.state.bms_live (MAC → last poll), not here.
Overlay helpers are pure so they can be unit-tested without FastAPI.
"""

from __future__ import annotations

import json
import logging
import os
import re
import time
from typing import Any

log = logging.getLogger("bms_devices")

DEVICES_PATH = os.environ.get("JACKERY_BMS_DEVICES_FILE", "/data/bms_devices.json")

STALE_AFTER_S = 90.0
_MAC_HEX = re.compile(r"[^0-9A-Fa-f]")


def normalize_mac(mac: str) -> str:
    """AA:BB:CC:DD:EE:FF. Raises ValueError if not 6 bytes."""
    hex_only = _MAC_HEX.sub("", mac or "")
    if len(hex_only) != 12:
        raise ValueError("MAC must be 6 bytes (AA:BB:CC:DD:EE:FF)")
    hex_only = hex_only.upper()
    return ":".join(hex_only[i:i + 2] for i in range(0, 12, 2))


def weighted_soc(items: list[tuple[float, float]]) -> float | None:
    """Capacity-weighted SOC from (soc_pct, capacity_wh) pairs. Pack-only."""
    total_wh = 0.0
    stored = 0.0
    for soc, wh in items:
        try:
            soc_f = float(soc)
            wh_f = float(wh)
        except (TypeError, ValueError):
            continue
        if wh_f <= 0:
            continue
        total_wh += wh_f
        stored += soc_f * wh_f / 100.0
    if total_wh <= 0:
        return None
    return max(0.0, min(100.0, stored / total_wh * 100.0))


def infer_capacity_wh(reading: dict[str, Any] | None) -> int | None:
    """full_ah * pack_voltage from a 0x03 reading, if both look real."""
    if not reading:
        return None
    try:
        full_ah = float(reading.get("full_ah") or 0)
        voltage_v = float(reading.get("voltage_v") or 0)
    except (TypeError, ValueError):
        return None
    if full_ah <= 0 or voltage_v <= 0:
        return None
    return max(1, int(round(full_ah * voltage_v)))


class BmsRegistry:
    def __init__(self) -> None:
        self.packs: list[dict] = []
        self.inverters: dict[str, dict] = {}
        self._load()

    def _load(self) -> None:
        try:
            with open(DEVICES_PATH) as f:
                data = json.load(f)
            self.packs = list(data.get("packs") or [])
            raw_inv = data.get("inverters") or {}
            self.inverters = {
                str(k): dict(v) for k, v in raw_inv.items() if isinstance(v, dict)
            }
        except FileNotFoundError:
            self.packs = []
            self.inverters = {}
        except Exception as e:
            log.warning("bms devices file %s unreadable: %s; starting empty",
                        DEVICES_PATH, e)
            self.packs = []
            self.inverters = {}

    def _save(self) -> None:
        try:
            os.makedirs(os.path.dirname(DEVICES_PATH) or ".", exist_ok=True)
            tmp = DEVICES_PATH + ".tmp"
            with open(tmp, "w") as f:
                json.dump({
                    "packs": self.packs,
                    "inverters": self.inverters,
                }, f, indent=2)
            os.replace(tmp, DEVICES_PATH)
        except Exception as e:
            log.error("failed to save bms devices: %s", e)

    def list_packs(self, siseli_device_sn: str | None = None) -> list[dict]:
        if siseli_device_sn is None:
            return list(self.packs)
        sn = str(siseli_device_sn)
        return [p for p in self.packs if p.get("siseli_device_sn") == sn]

    def get(self, mac: str) -> dict | None:
        try:
            mac_n = normalize_mac(mac)
        except ValueError:
            return None
        return next((p for p in self.packs if p.get("mac") == mac_n), None)

    def upsert(self, mac: str, *, alias: str = "",
               capacity_wh: int | float | None = None,
               siseli_device_sn: str | None = None) -> dict:
        mac_n = normalize_mac(mac)
        alias = (alias or "").strip() or mac_n
        now = time.time()
        sn = None if siseli_device_sn is None else (str(siseli_device_sn).strip() or None)
        existing = self.get(mac_n)
        if existing:
            existing["alias"] = alias
            if capacity_wh is not None:
                existing["capacity_wh"] = int(capacity_wh) if capacity_wh else 0
            if siseli_device_sn is not None:
                existing["siseli_device_sn"] = sn
                if sn:
                    self._ensure_inverter(sn)
        else:
            existing = {
                "mac": mac_n,
                "alias": alias,
                "capacity_wh": int(capacity_wh) if capacity_wh else 0,
                "siseli_device_sn": sn,
                "added": now,
            }
            self.packs.append(existing)
            if sn:
                self._ensure_inverter(sn)
        self._save()
        return existing

    def set_capacity_wh(self, mac: str, capacity_wh: int) -> dict | None:
        d = self.get(mac)
        if not d:
            return None
        d["capacity_wh"] = int(capacity_wh)
        self._save()
        return d

    def delete(self, mac: str) -> bool:
        try:
            mac_n = normalize_mac(mac)
        except ValueError:
            return False
        before = len(self.packs)
        self.packs = [p for p in self.packs if p.get("mac") != mac_n]
        changed = len(self.packs) != before
        if changed:
            self._save()
        return changed

    def _ensure_inverter(self, sn: str) -> dict:
        row = self.inverters.get(sn)
        if row is None:
            row = {"use_as_main": True}
            self.inverters[sn] = row
        return row

    def use_as_main(self, sn: str | None) -> bool:
        if not sn:
            return False
        row = self.inverters.get(str(sn))
        if row is None:
            return False
        return bool(row.get("use_as_main", True))

    def set_use_as_main(self, sn: str, enabled: bool) -> dict:
        sn = str(sn).strip()
        if not sn:
            raise ValueError("siseli device sn is required")
        row = self._ensure_inverter(sn)
        row["use_as_main"] = bool(enabled)
        self._save()
        return dict(row)


def _is_fresh(live: dict[str, Any] | None, now: float, stale_s: float) -> bool:
    if not live or live.get("error") and live.get("soc_pct") is None:
        return False
    ts = live.get("ts")
    if ts is None:
        return False
    try:
        age = now - float(ts)
    except (TypeError, ValueError):
        return False
    if age > stale_s:
        return False
    return live.get("soc_pct") is not None


def overlay_telemetry(
    tele: dict[str, Any] | None,
    *,
    packs: list[dict],
    live: dict[str, dict],
    use_as_main: bool,
    now: float | None = None,
    stale_s: float = STALE_AFTER_S,
    capacity_override_wh: int | None = None,
) -> dict[str, Any] | None:
    """Copy Siseli telemetry and, when enabled, replace headline SOC.

    Portal battery_percent is preserved as inverter_soc_pct / main_soc_pct.
    Does not mutate `tele`.
    """
    if tele is None:
        return None
    out = dict(tele)
    if not packs or not use_as_main:
        return out
    now = time.time() if now is None else now
    weights: list[tuple[float, float]] = []
    currents = 0.0
    powers = 0.0
    voltage = None
    hottest = None
    total_wh = 0
    for p in packs:
        mac = p.get("mac")
        reading = live.get(mac) if mac else None
        wh = int(p.get("capacity_wh") or 0)
        if wh <= 0:
            inferred = infer_capacity_wh(reading)
            wh = inferred or 0
        if wh > 0:
            total_wh += wh
        if not _is_fresh(reading, now, stale_s):
            continue
        soc = reading.get("soc_pct")
        if soc is None or wh <= 0:
            continue
        weights.append((float(soc), float(wh)))
        try:
            currents += float(reading.get("current_a") or 0)
        except (TypeError, ValueError):
            pass
        try:
            powers += float(reading.get("power_w") or 0)
        except (TypeError, ValueError):
            pass
        if voltage is None:
            try:
                voltage = float(reading.get("voltage_v"))
            except (TypeError, ValueError):
                voltage = None
        t = reading.get("temp_c")
        if t is not None:
            try:
                tf = float(t)
            except (TypeError, ValueError):
                tf = None
            if tf is not None and (hottest is None or tf > hottest):
                hottest = tf
    soc = weighted_soc(weights)
    if soc is None:
        return out
    portal = out.get("battery_percent")
    out["inverter_soc_pct"] = portal
    out["main_soc_pct"] = portal
    rounded = round(soc, 1)
    out["battery_percent"] = rounded
    out["system_soc_pct"] = rounded
    out["bms_source"] = True
    cap = int(capacity_override_wh) if capacity_override_wh else total_wh
    if cap:
        out["capacity_wh"] = cap
    if voltage is not None:
        out["battery_voltage_v"] = round(voltage, 3)
    out["battery_charge_a"] = max(0.0, currents)
    out["battery_discharge_a"] = max(0.0, -currents)
    out["battery_power_w"] = round(powers, 1)
    if hottest is not None:
        out["battery_temp_c"] = hottest
    if currents > 0.05:
        out["battery_status"] = 1
    elif currents < -0.05:
        out["battery_status"] = 2
    else:
        out["battery_status"] = 0
    return out


def ui_pack_rows(
    packs: list[dict],
    live: dict[str, dict],
    *,
    now: float | None = None,
    stale_s: float = STALE_AFTER_S,
) -> list[dict]:
    """Jackery-shaped pack rows so the existing Live packs card can render."""
    now = time.time() if now is None else now
    rows: list[dict] = []
    for i, p in enumerate(packs):
        mac = p.get("mac") or ""
        reading = live.get(mac) or {}
        fresh = _is_fresh(reading, now, stale_s)
        soc = reading.get("soc_pct") if reading else None
        watts = 0.0
        try:
            watts = float(reading.get("power_w") or 0)
        except (TypeError, ValueError):
            watts = 0.0
        ip = max(0, int(round(watts))) if watts > 0 else 0
        op = max(0, int(round(-watts))) if watts < 0 else 0
        err = reading.get("error")
        if not fresh:
            err = err or ("stale" if reading.get("ts") else "no reading")
        row = {
            "deviceSn": mac,
            "deviceOrder": i,
            "rb": soc,
            "ip": ip,
            "op": op,
            "it": reading.get("temp_c"),
            "source": "bms",
            "alias": p.get("alias") or mac,
            "capacity_wh": int(p.get("capacity_wh") or 0),
            "voltage_v": reading.get("voltage_v"),
            "current_a": reading.get("current_a"),
            "min_cell_mv": reading.get("min_cell_mv"),
            "max_cell_mv": reading.get("max_cell_mv"),
            "cell_count": reading.get("cell_count"),
            "cycles": reading.get("cycles"),
            "error": err,
            "ts": reading.get("ts"),
        }
        rows.append(row)
    return rows


def latest_live_ts(packs: list[dict], live: dict[str, dict]) -> float | None:
    latest = None
    for p in packs:
        reading = live.get(p.get("mac") or "")
        ts = (reading or {}).get("ts")
        if ts is None:
            continue
        try:
            tsf = float(ts)
        except (TypeError, ValueError):
            continue
        if latest is None or tsf > latest:
            latest = tsf
    return latest
