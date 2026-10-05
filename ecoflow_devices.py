"""Persisted registry of EcoFlow power stations.

Storage: /data/ecoflow_devices.json. Format:
{
  "devices": [
    {
      "sn": "ecoflow:R351…",
      "raw_sn": "R351…",
      "device_type": "DELTA_3_MAX_PLUS",
      "alias": "Garage Delta",
      "capacity_wh": 2048,
      "siseli_device_sn": "siseli:42",
      "roles": ["solar", "battery", "output"],
      "added": <unix-ts>
    }
  ]
}

Live telemetry lives in server.state.ecoflow, not here.
Overlay helpers are pure so they can be unit-tested without FastAPI.
"""
from __future__ import annotations

import json
import logging
import os
import time
from typing import Any

import bms_devices

log = logging.getLogger("ecoflow_devices")

DEVICES_PATH = os.environ.get(
    "JACKERY_ECOFLOW_DEVICES_FILE", "/data/ecoflow_devices.json"
)

SN_PREFIX = "ecoflow:"
SUPPORTED_TYPES = ("DELTA_3_MAX_PLUS", "DELTA_3", "DELTA_3_1500")
DEFAULT_TYPE = "DELTA_3_MAX_PLUS"
ALL_ROLES = ("solar", "battery", "output")
STALE_AFTER_S = 90.0

# Approximate Wh for known models when the user has not set capacity_wh.
DEFAULT_CAPACITY_WH = {
    "DELTA_3_MAX_PLUS": 2048,
    "DELTA_3": 1024,
    "DELTA_3_1500": 1536,
}


def is_ecoflow_sn(value: Any) -> bool:
    return str(value or "").startswith(SN_PREFIX)


def make_sn(raw_sn: str) -> str:
    raw = str(raw_sn or "").strip()
    if not raw:
        raise ValueError("device serial is required")
    if raw.startswith(SN_PREFIX):
        return raw
    return f"{SN_PREFIX}{raw}"


def raw_sn_from_sn(sn: str) -> str:
    s = str(sn or "").strip()
    if s.startswith(SN_PREFIX):
        return s[len(SN_PREFIX):]
    return s


def _clean_roles(roles: Any) -> list[str]:
    if roles is None:
        return list(ALL_ROLES)
    out: list[str] = []
    seen: set[str] = set()
    for r in roles:
        key = str(r or "").strip().lower()
        if key in ALL_ROLES and key not in seen:
            seen.add(key)
            out.append(key)
    return out or list(ALL_ROLES)


def _normalize_row(d: dict) -> dict | None:
    try:
        sn = make_sn(d.get("sn") or d.get("raw_sn") or "")
    except ValueError:
        return None
    dtype = str(d.get("device_type") or DEFAULT_TYPE).strip().upper()
    if dtype not in SUPPORTED_TYPES:
        dtype = DEFAULT_TYPE
    raw = str(d.get("raw_sn") or raw_sn_from_sn(sn)).strip()
    alias = (str(d.get("alias") or "").strip()
             or DEFAULT_TYPE.replace("_", " ").title())
    try:
        cap = int(d.get("capacity_wh") or 0)
    except (TypeError, ValueError):
        cap = 0
    if cap <= 0:
        cap = int(DEFAULT_CAPACITY_WH.get(dtype) or 0)
    siseli = str(d.get("siseli_device_sn") or "").strip() or None
    try:
        added = float(d.get("added") or time.time())
    except (TypeError, ValueError):
        added = time.time()
    return {
        "sn": sn,
        "raw_sn": raw,
        "device_type": dtype,
        "alias": alias,
        "capacity_wh": cap,
        "siseli_device_sn": siseli,
        "roles": _clean_roles(d.get("roles")),
        "added": added,
    }


class EcoflowRegistry:
    def __init__(self) -> None:
        self.devices: list[dict] = []
        self._load()

    def _load(self) -> None:
        try:
            with open(DEVICES_PATH) as f:
                data = json.load(f)
            rows = []
            for raw in (data.get("devices") or []):
                if not isinstance(raw, dict):
                    continue
                row = _normalize_row(raw)
                if row:
                    rows.append(row)
            self.devices = rows
        except FileNotFoundError:
            self.devices = []
        except Exception as e:
            log.warning("ecoflow devices file %s unreadable: %s; starting empty",
                        DEVICES_PATH, e)
            self.devices = []

    def _save(self) -> None:
        try:
            os.makedirs(os.path.dirname(DEVICES_PATH) or ".", exist_ok=True)
            tmp = DEVICES_PATH + ".tmp"
            with open(tmp, "w") as f:
                json.dump({"devices": self.devices}, f, indent=2)
            os.replace(tmp, DEVICES_PATH)
        except Exception as e:
            log.error("failed to save ecoflow devices: %s", e)

    def list_devices(self, siseli_device_sn: str | None = None) -> list[dict]:
        if siseli_device_sn is None:
            return [dict(d) for d in self.devices]
        sn = str(siseli_device_sn)
        return [dict(d) for d in self.devices if d.get("siseli_device_sn") == sn]

    def get(self, sn: str) -> dict | None:
        try:
            key = make_sn(sn)
        except ValueError:
            return None
        return next((dict(d) for d in self.devices if d.get("sn") == key), None)

    def upsert(
        self,
        raw_sn: str,
        *,
        device_type: str = DEFAULT_TYPE,
        alias: str = "",
        capacity_wh: int | float | None = None,
        siseli_device_sn: Any = None,
        roles: Any = None,
    ) -> dict:
        sn = make_sn(raw_sn)
        dtype = str(device_type or DEFAULT_TYPE).strip().upper()
        if dtype not in SUPPORTED_TYPES:
            raise ValueError(f"unsupported device_type: {device_type}")
        existing = next((d for d in self.devices if d.get("sn") == sn), None)
        if existing is None:
            existing = {
                "sn": sn,
                "raw_sn": raw_sn_from_sn(sn),
                "device_type": dtype,
                "alias": (alias or "").strip() or dtype.replace("_", " ").title(),
                "capacity_wh": int(
                    capacity_wh
                    if capacity_wh is not None
                    else DEFAULT_CAPACITY_WH.get(dtype) or 0
                ),
                "siseli_device_sn": None,
                "roles": list(ALL_ROLES),
                "added": time.time(),
            }
            self.devices.append(existing)
        else:
            existing["device_type"] = dtype
            if alias is not None and str(alias).strip():
                existing["alias"] = str(alias).strip()
            if capacity_wh is not None:
                existing["capacity_wh"] = int(capacity_wh) if capacity_wh else 0
        if siseli_device_sn is not None:
            link = str(siseli_device_sn or "").strip() or None
            existing["siseli_device_sn"] = link
        if roles is not None:
            existing["roles"] = _clean_roles(roles)
        self._save()
        return dict(existing)

    def link(
        self,
        sn: str,
        *,
        siseli_device_sn: str | None,
        roles: Any = None,
    ) -> dict:
        row = next((d for d in self.devices if d.get("sn") == make_sn(sn)), None)
        if not row:
            raise KeyError(sn)
        link = str(siseli_device_sn or "").strip() or None
        row["siseli_device_sn"] = link
        if roles is not None:
            row["roles"] = _clean_roles(roles)
        elif link and not row.get("roles"):
            row["roles"] = list(ALL_ROLES)
        self._save()
        return dict(row)

    def delete(self, sn: str) -> bool:
        try:
            key = make_sn(sn)
        except ValueError:
            return False
        before = len(self.devices)
        self.devices = [d for d in self.devices if d.get("sn") != key]
        changed = len(self.devices) != before
        if changed:
            self._save()
        return changed

    def fleet_meta(self) -> list[dict]:
        """Unattached EcoFlow devices for the Live fleet strip / picker.

        Rows linked to a Siseli (`siseli_device_sn`) are omitted; their
        watts overlay onto the Siseli view instead.
        """
        out = []
        for d in self.devices:
            if d.get("siseli_device_sn"):
                continue
            out.append({
                "device_id": d["sn"],
                "device_sn": d["sn"],
                "name": d.get("alias") or d["sn"],
                "model_name": d.get("device_type", DEFAULT_TYPE).replace("_", " ").title(),
                "model_code": None,
                "source": "ecoflow",
                "device_type": d.get("device_type"),
                "capacity_wh": d.get("capacity_wh"),
            })
        return out


def _is_fresh(live: dict[str, Any] | None, now: float, stale_s: float) -> bool:
    if not live:
        return False
    ts = live.get("ts")
    if ts is None:
        return False
    try:
        if (now - float(ts)) > stale_s:
            return False
    except (TypeError, ValueError):
        return False
    tele = live.get("telemetry") if isinstance(live.get("telemetry"), dict) else live
    return tele.get("battery_percent") is not None or tele.get("solar_input_w") is not None


def linked_rows_for_siseli(
    registry: EcoflowRegistry,
    siseli_sn: str,
    live_by_sn: dict[str, dict],
    *,
    now: float | None = None,
    stale_s: float = STALE_AFTER_S,
) -> list[dict]:
    """Linked EcoFlow devices with fresh telemetry for the expandable Live card."""
    now = time.time() if now is None else now
    out = []
    for d in registry.list_devices(siseli_sn):
        live = live_by_sn.get(d["sn"]) or {}
        tele = live.get("telemetry") if isinstance(live.get("telemetry"), dict) else {}
        detail = live.get("detail") if isinstance(live.get("detail"), dict) else {}
        fresh = _is_fresh(live, now, stale_s)
        out.append({
            "sn": d["sn"],
            "alias": d.get("alias"),
            "device_type": d.get("device_type"),
            "roles": list(d.get("roles") or ALL_ROLES),
            "capacity_wh": d.get("capacity_wh"),
            "fresh": fresh,
            "telemetry": dict(tele) if tele else {},
            "detail": dict(detail) if detail else {},
            "ts": live.get("ts"),
        })
    return out


def overlay_telemetry(
    tele: dict[str, Any] | None,
    *,
    linked: list[dict],
    live_by_sn: dict[str, dict],
    now: float | None = None,
    stale_s: float = STALE_AFTER_S,
    bms_already: bool = False,
    load_sources: list[str] | None = None,
) -> dict[str, Any] | None:
    """Merge linked EcoFlow solar/battery/output into Siseli telemetry.

    Does not mutate `tele`. Battery role uses capacity-weighted SOC with
    any existing BMS headline SOC when `bms_already` is set.

    `load_sources` filters which load_inputs contribute to
    ``output_power_w``. None = all; explicit list = only those ids.
    """
    if tele is None:
        return None
    out = dict(tele)
    now = time.time() if now is None else now

    solar_inputs = list(out.get("solar_inputs") or [])
    extra_solar = 0.0
    load_inputs: list[dict] = []
    have_output_role = False
    batt_weights: list[tuple[float, float]] = []
    linked_payload: list[dict] = []

    # Capture Siseli/BMS load before EcoFlow extras are added.
    base_load = float(out.get("output_power_w") or 0)

    for d in linked:
        roles = set(d.get("roles") or ALL_ROLES)
        sn = d.get("sn")
        live = live_by_sn.get(sn) or {}
        if not _is_fresh(live, now, stale_s):
            linked_payload.append({
                "sn": sn,
                "alias": d.get("alias"),
                "device_type": d.get("device_type"),
                "roles": list(roles),
                "fresh": False,
                "telemetry": {},
                "detail": live.get("detail") or {},
                "ts": live.get("ts"),
            })
            if "output" in roles:
                have_output_role = True
            continue
        et = live.get("telemetry") if isinstance(live.get("telemetry"), dict) else {}
        detail = live.get("detail") if isinstance(live.get("detail"), dict) else {}
        linked_payload.append({
            "sn": sn,
            "alias": d.get("alias"),
            "device_type": d.get("device_type"),
            "roles": list(roles),
            "fresh": True,
            "telemetry": dict(et),
            "detail": dict(detail),
            "ts": live.get("ts"),
        })
        if "solar" in roles:
            for inp in (et.get("solar_inputs") or []):
                if not isinstance(inp, dict):
                    continue
                watts = float(inp.get("watts") or 0)
                solar_inputs.append({
                    "id": inp.get("id") or f"{sn}:{inp.get('label')}",
                    "label": inp.get("label") or "EcoFlow PV",
                    "source": "ecoflow",
                    "device_sn": sn,
                    "watts": watts,
                })
                extra_solar += max(0.0, watts)
            if not et.get("solar_inputs"):
                w = float(et.get("solar_input_w") or 0)
                if w > 0:
                    solar_inputs.append({
                        "id": f"{sn}:pv",
                        "label": f"{d.get('alias') or 'EcoFlow'} solar",
                        "source": "ecoflow",
                        "device_sn": sn,
                        "watts": w,
                    })
                    extra_solar += w
        if "output" in roles:
            have_output_role = True
            load_inputs.append({
                "id": sn,
                "label": f"{d.get('alias') or 'EcoFlow'} load",
                "source": "ecoflow",
                "device_sn": sn,
                "watts": round(max(0.0, float(et.get("output_power_w") or 0))),
            })
        if "battery" in roles:
            soc = et.get("battery_percent")
            wh = float(d.get("capacity_wh") or 0)
            if soc is not None and wh > 0:
                batt_weights.append((float(soc), wh))

    if solar_inputs:
        out["solar_inputs"] = solar_inputs
    if extra_solar:
        base = float(out.get("solar_input_w") or 0)
        out["siseli_solar_w"] = base
        out["additional_solar_w"] = round(extra_solar)
        out["solar_input_w"] = round(base + extra_solar)
        out["input_power_w"] = round(
            float(out.get("solar_input_w") or 0) + float(out.get("ac_input_w") or 0)
        )

    if have_output_role:
        # Siseli/BMS headline load first, then each EcoFlow output unit.
        all_loads = [{
            "id": "siseli",
            "label": "Siseli load",
            "source": "siseli",
            "watts": round(max(0.0, base_load)),
        }] + load_inputs
        out["load_inputs"] = all_loads
        out["siseli_output_w"] = round(max(0.0, base_load))
        selected = (
            set(load_sources)
            if load_sources is not None
            else {row["id"] for row in all_loads}
        )
        total = 0.0
        for row in all_loads:
            if row["id"] in selected:
                total += float(row["watts"] or 0)
        out["additional_output_w"] = round(sum(
            float(row["watts"] or 0) for row in load_inputs
            if row["id"] in selected
        ))
        out["output_power_w"] = round(total)

    if batt_weights:
        # Combine with existing headline SOC when BMS already set it.
        if bms_already and out.get("battery_percent") is not None:
            try:
                existing_wh = float(out.get("capacity_wh") or 0)
            except (TypeError, ValueError):
                existing_wh = 0.0
            if existing_wh > 0:
                batt_weights.insert(0, (float(out["battery_percent"]), existing_wh))
        soc = bms_devices.weighted_soc(batt_weights)
        if soc is not None:
            if out.get("inverter_soc_pct") is None and not out.get("bms_source"):
                out["inverter_soc_pct"] = out.get("battery_percent")
                out["main_soc_pct"] = out.get("battery_percent")
            rounded = round(soc, 1)
            out["battery_percent"] = rounded
            out["system_soc_pct"] = rounded
            out["ecoflow_source"] = True
            total_wh = int(sum(w for _, w in batt_weights))
            if total_wh:
                out["capacity_wh"] = total_wh

    if linked_payload:
        out["linked_ecoflow"] = linked_payload
        out["ecoflow_linked"] = True
    return out
