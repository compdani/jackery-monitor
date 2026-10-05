"""
Per-device local preferences: display alias and which Siseli controls
appear on the Live page.

Storage: /data/device_prefs.json. Format:
{
  "siseli:42": {
    "alias": "House inverter",
    "live_controls": ["LoadSwitchSetting", "batteryChargeLimit"]
  }
}

Aliases are display-only — they never write to Siseli or Jackery cloud.
`live_controls` omitted means "use the known CONTROL_DEFINITIONS this
inverter exposes". An explicit empty list means nothing on Live.
`ignore_inverter_soc` (Siseli only) drops voltage-based inverter SOC
from Live, history, and the packs card.
`calc_grid` (Siseli only) estimates grid watts on this dashboard from
solar, battery discharge, and load. It never writes to Siseli.
`load_sources` (Siseli + linked EcoFlow) is which load contributors
count toward Live `output_power_w`. Missing key → all available;
explicit list → only those ids (e.g. ``siseli``, ``ecoflow:R351…``).
"""

from __future__ import annotations

import json
import logging
import os
from typing import Any

import siseli_client

log = logging.getLogger("device_prefs")

PREFS_PATH = os.environ.get("JACKERY_DEVICE_PREFS_FILE", "/data/device_prefs.json")

_UNSET = object()


def _clean_load_sources(raw: Any) -> list[str] | None:
    """Normalize a load_sources payload. None means 'use all'."""
    if raw is None:
        return None
    if not isinstance(raw, (list, tuple)):
        return None
    out: list[str] = []
    seen: set[str] = set()
    for item in raw:
        s = str(item or "").strip()
        if s and s not in seen:
            seen.add(s)
            out.append(s)
    return out


def _clean_id(device_id: str | None) -> str:
    return str(device_id or "").strip()


def overlay_device(device: dict[str, Any] | None,
                   pref: dict[str, Any] | None) -> dict[str, Any]:
    """Copy `device` with portal_name preserved and alias applied to name."""
    out = dict(device or {})
    portal = out.get("portal_name") or out.get("name") or out.get("model_name")
    out["portal_name"] = portal
    alias = ((pref or {}).get("alias") or "").strip()
    if alias:
        out["name"] = alias
    return out


def resolved_live_controls(pref: dict[str, Any] | None,
                           controls: list[dict[str, Any]] | None) -> list[str]:
    """Live-page pin list. Missing key → known defaults; [] → none."""
    pref = pref or {}
    if "live_controls" in pref and pref["live_controls"] is not None:
        out: list[str] = []
        seen: set[str] = set()
        for key in pref["live_controls"]:
            s = str(key or "").strip()
            if s and s not in seen:
                seen.add(s)
                out.append(s)
        return out
    return siseli_client.default_live_control_keys(controls)


def ignore_inverter_soc(pref: dict[str, Any] | None) -> bool:
    """True when this Siseli device should never use inverter (voltage) SOC."""
    return bool((pref or {}).get("ignore_inverter_soc"))


def calc_grid(pref: dict[str, Any] | None) -> bool:
    """True when this dashboard should estimate Siseli grid watts."""
    return bool((pref or {}).get("calc_grid"))


def solar_flow_unified(pref: dict[str, Any] | None) -> bool:
    """True when Live power flow collapses all solar inputs into one node.

    Default False = show Siseli PV strings and linked EcoFlow PV inputs
    as separate solar nodes.
    """
    return bool((pref or {}).get("solar_flow_unified"))


def load_sources(pref: dict[str, Any] | None) -> list[str] | None:
    """Which load_inputs ids contribute to output_power_w.

    None → all available sources. Explicit list (incl. empty) → filter.
    """
    if not pref or "load_sources" not in pref:
        return None
    return _clean_load_sources(pref.get("load_sources"))


class DevicePrefs:
    def __init__(self) -> None:
        self.by_id: dict[str, dict[str, Any]] = {}
        self._load()

    def _load(self) -> None:
        try:
            with open(PREFS_PATH) as f:
                data = json.load(f)
            raw = data if isinstance(data, dict) else {}
            self.by_id = {
                str(k): dict(v) for k, v in raw.items() if isinstance(v, dict)
            }
        except FileNotFoundError:
            self.by_id = {}
        except Exception as e:
            log.warning("device prefs file %s unreadable: %s; starting empty",
                        PREFS_PATH, e)
            self.by_id = {}

    def _save(self) -> None:
        try:
            os.makedirs(os.path.dirname(PREFS_PATH) or ".", exist_ok=True)
            tmp = PREFS_PATH + ".tmp"
            with open(tmp, "w") as f:
                json.dump(self.by_id, f, indent=2)
            os.replace(tmp, PREFS_PATH)
        except Exception as e:
            log.error("failed to save device prefs: %s", e)

    def get(self, device_id: str | None) -> dict[str, Any]:
        did = _clean_id(device_id)
        if not did:
            return {}
        return dict(self.by_id.get(did) or {})

    def all(self) -> dict[str, dict[str, Any]]:
        return {k: dict(v) for k, v in self.by_id.items()}

    def update(self, device_id: str, *, alias: Any = _UNSET,
               live_controls: Any = _UNSET,
               ignore_inverter_soc: Any = _UNSET,
               calc_grid: Any = _UNSET,
               solar_flow_unified: Any = _UNSET,
               load_sources: Any = _UNSET) -> dict[str, Any]:
        did = _clean_id(device_id)
        if not did:
            raise ValueError("device_id is required")
        row = dict(self.by_id.get(did) or {})
        if alias is not _UNSET:
            text = ("" if alias is None else str(alias)).strip()
            if text:
                row["alias"] = text
            else:
                row.pop("alias", None)
        if live_controls is not _UNSET:
            if live_controls is None:
                row.pop("live_controls", None)
            else:
                keys: list[str] = []
                seen: set[str] = set()
                for key in live_controls:
                    s = str(key or "").strip()
                    if s and s not in seen:
                        seen.add(s)
                        keys.append(s)
                row["live_controls"] = keys
        if ignore_inverter_soc is not _UNSET:
            if ignore_inverter_soc:
                row["ignore_inverter_soc"] = True
            else:
                row.pop("ignore_inverter_soc", None)
        if calc_grid is not _UNSET:
            if calc_grid:
                row["calc_grid"] = True
            else:
                row.pop("calc_grid", None)
        if solar_flow_unified is not _UNSET:
            if solar_flow_unified:
                row["solar_flow_unified"] = True
            else:
                row.pop("solar_flow_unified", None)
        if load_sources is not _UNSET:
            cleaned = _clean_load_sources(load_sources)
            if cleaned is None:
                row.pop("load_sources", None)
            else:
                row["load_sources"] = cleaned
        if row:
            self.by_id[did] = row
        else:
            self.by_id.pop(did, None)
        self._save()
        return dict(row)
