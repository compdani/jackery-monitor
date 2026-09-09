"""Inverter recovery watchdog.

The Jackery 5000+ inverter can trip in a way that does NOT flip `oac`:
the unit keeps reporting the AC port ON while output sits at 0W. That
hardware-trip signature is OPT-IN via `collapse_floor_w` (0 = disabled).
When it fires, the caller must cycle AC off → on — a plain AC-on is a
no-op while the port still claims on.

A port that actually reports OFF is left alone. The dashboard never
auto-enables AC just because `oac` is False (UI off, phone-app off, or
already off at startup). Operators who want collapse recovery set
`inverter_trip_recovery_min_w` below their known 24/7 base load.

This module is the pure-function state machine; the server's poll_loop
drives it via `evaluate()` after each successful telemetry read, and
issues an off→on cycle on "cycle".

HARDWARE trip (2026-07-21): after output was recently at/above
OUTPUT_BASELINE_W, if COLLAPSE_MIN_SAMPLES *distinct* fresh telemetry
samples (deduped by sample_ts) sit at/below the floor for at least
COLLAPSE_MIN_DURATION_S, and the drop from baseline to floor was
abrupt (within ABRUPT_DROP_MAX_S), a hardware-trip episode latches.
Recovery: "cycle", capped at MAX_CYCLES_PER_EPISODE per episode — a
false positive costs at most two brief cuts, never a burst. The episode
latch clears ONLY on genuine recovery (fresh output above the floor);
it does NOT time out while output is still collapsed, so the badge can
never be silently wiped mid-outage.
"""
from __future__ import annotations

import threading
import time
from dataclasses import dataclass
from typing import Literal

# Action returned by evaluate(); callers translate to MQTT toggles + UI.
# "cycle" = hardware-trip recovery: the port CLAIMS on (oac=1) but output
# collapsed, so a plain AC-on is a no-op — the caller must toggle
# off -> on to reset the inverter.
Action = Literal["idle", "cycle", "waiting", "user_grace", "error"]


@dataclass
class WatchdogState:
    """Per-device state. 0/None values mean "no recovery in progress."

    Persisted only in memory — restart resets, which is fine: a restart
    means the dashboard is back online and the user is watching."""
    consecutive_attempts: int = 0
    # Pacing clock. Deliberately PRESERVED across idle resets (only
    # dismiss_error zeroes it): a flapping load that repeatedly arms and
    # clears the collapse detector is still rate-limited to one fire per
    # retry interval instead of an unpaced storm of power cuts.
    last_attempt_ts: float = 0.0
    # Stamped by the AC-toggle endpoint when the user explicitly turns
    # AC off via our UI. Suppresses hardware-trip detection for
    # `user_grace_s` afterward so lagging telemetry (port still claims
    # ON, watts already 0) cannot look like a collapse and cycle AC
    # back on during the device's apply window.
    last_user_off_ts: float = 0.0
    # Set when we've exhausted the cycle cap (hardware-trip path).
    # UI shows this; clicking dismiss resets.
    error_message: str | None = None
    # ---- hardware-trip (output-collapse) detection ----
    last_high_output_ts: float = 0.0   # newest fresh sample >= baseline
    first_low_ts: float = 0.0          # start of the current low streak
    low_samples: int = 0               # DISTINCT fresh samples in streak
    last_sample_ts: float = 0.0        # dedup: bridge serves cached frames
    hw_trip_active: bool = False       # episode latch — see module doc
    episode_cycles: int = 0            # off->on cycles fired this episode


DEFAULT_RETRY_INTERVAL_S = 10.0
DEFAULT_USER_GRACE_S = 60.0

# Hardware-trip signature gates (see module docstring). The floor itself
# is per-rig config (settings: inverter_trip_recovery_min_w; 0 disables).
OUTPUT_BASELINE_W = 300.0        # "loads were genuinely running"
COLLAPSE_MIN_SAMPLES = 3         # distinct fresh samples at/below floor
COLLAPSE_MIN_DURATION_S = 6.0    # wall-clock span of the low streak
ABRUPT_DROP_MAX_S = 15.0         # baseline -> floor must happen this fast
MAX_CYCLES_PER_EPISODE = 2       # then latch the badge and stop cycling


def _track_collapse(state: WatchdogState, ac_on: bool,
                    output_w: float | None, sample_ts: float | None,
                    collapse_floor_w: float, now: float) -> None:
    """Update the collapse detector from one telemetry frame. Only
    DISTINCT frames count (sample_ts dedup — the bridge re-serves cached
    frames every poll tick, and a single glitched 0W frame must never
    satisfy the whole debounce)."""
    if collapse_floor_w <= 0 or output_w is None:
        return
    if sample_ts is not None:
        if sample_ts == state.last_sample_ts:
            return  # same cached frame — not a new observation
        state.last_sample_ts = sample_ts
    if not ac_on:
        # Port is actually off — a streak accumulated while AC was off
        # must not fire a gratuitous cycle right after the port returns.
        state.first_low_ts = 0.0
        state.low_samples = 0
        return
    if output_w >= OUTPUT_BASELINE_W:
        state.last_high_output_ts = now
        state.first_low_ts = 0.0
        state.low_samples = 0
        if state.hw_trip_active:
            state.hw_trip_active = False   # genuine recovery
            state.episode_cycles = 0
    elif output_w <= collapse_floor_w:
        if state.first_low_ts == 0.0:
            state.first_low_ts = now
        state.low_samples += 1
    else:
        # Mid-range: the house demonstrably has power.
        state.first_low_ts = 0.0
        state.low_samples = 0
        if state.hw_trip_active:
            state.hw_trip_active = False
            state.episode_cycles = 0
    # Arm a new episode only on the full signature.
    if (not state.hw_trip_active and ac_on
            and state.low_samples >= COLLAPSE_MIN_SAMPLES
            and state.first_low_ts > 0
            and (now - state.first_low_ts) >= COLLAPSE_MIN_DURATION_S
            and state.last_high_output_ts > 0
            and (state.first_low_ts - state.last_high_output_ts)
            <= ABRUPT_DROP_MAX_S):
        state.hw_trip_active = True
        state.episode_cycles = 0


def evaluate(
    state: WatchdogState,
    ac_on: bool,
    output_w: float | None = None,
    *,
    sample_ts: float | None = None,
    collapse_floor_w: float = 0.0,
    now_ts: float | None = None,
    retry_interval_s: float = DEFAULT_RETRY_INTERVAL_S,
    user_grace_s: float = DEFAULT_USER_GRACE_S,
) -> Action:
    """Drive the recovery state machine one tick. Mutates `state`.

    Args:
        state: per-device runtime, modified in place.
        ac_on: current AC-port state from the latest telemetry.
        output_w: live output power (hardware-trip detection input).
        sample_ts: the telemetry frame's own timestamp — used to dedup
            re-served cached frames so the collapse debounce counts
            distinct observations, not poll ticks.
        collapse_floor_w: hardware-trip detection floor. 0 (default)
            DISABLES detection; operators set it below their known 24/7
            base load (settings: inverter_trip_recovery_min_w).
        now_ts: clock injection for tests; defaults to time.time().
        retry_interval_s: seconds between cycle attempts.
        user_grace_s: skip collapse detection this long after a
            user-initiated OFF, so lagging ON+0W telemetry cannot
            look like a hardware trip.

    Returns one of the Action strings. Callers should:
        - "cycle"       → hardware trip: send AC-off, pause, AC-on
        - "idle"        → nothing to do (healthy, or port genuinely OFF)
        - "waiting"     → mid-recovery, holding for retry interval
        - "user_grace"  → user turned AC off recently; telemetry still
                          claims ON; collapse detection suppressed
        - "error"       → badge latched; hardware-trip path stops after
                          the cycle cap until output genuinely recovers
    """
    now = float(now_ts if now_ts is not None else time.time())
    grace_active = bool(state.last_user_off_ts
                        and (now - state.last_user_off_ts) < user_grace_s)

    if grace_active:
        # Don't accumulate collapse evidence off the user's own OFF.
        state.first_low_ts = 0.0
        state.low_samples = 0
    else:
        _track_collapse(state, ac_on, output_w, sample_ts,
                        collapse_floor_w, now)

    if not ac_on:
        # Port reports OFF — never auto-enable. Clear recovery counters
        # and any latched hardware-trip episode so a later AC-on with
        # brief 0W lag does not inherit a stale cycle.
        state.consecutive_attempts = 0
        state.error_message = None
        state.hw_trip_active = False
        state.episode_cycles = 0
        state.first_low_ts = 0.0
        state.low_samples = 0
        return "idle"

    effective_on = ac_on and not state.hw_trip_active
    if effective_on:
        # AC healthy (either our recovery worked or an external action
        # fixed it). Clear the recovery machinery — but keep
        # last_attempt_ts as a pacing floor (see WatchdogState) and the
        # collapse bookkeeping (it self-maintains in _track_collapse).
        state.consecutive_attempts = 0
        state.error_message = None
        return "idle"
    if grace_active:
        return "user_grace"
    # Hardware-trip episode: hard cap on off->on cycles. A genuine trip
    # either resets on the first cycle or two; anything beyond that is
    # more likely a false positive or a unit that needs eyes on it —
    # repeating unconfirmed 2s outages multiplies harm without adding
    # recovery power. The episode (and badge) clears only on genuine
    # output recovery.
    if state.hw_trip_active \
            and state.episode_cycles >= MAX_CYCLES_PER_EPISODE:
        if not state.error_message:
            state.error_message = (
                f"Output collapsed while the AC port still reports ON — "
                f"hardware trip suspected. Cycled AC off/on "
                f"{MAX_CYCLES_PER_EPISODE}x without output returning; "
                f"stopped to avoid repeated power cuts. Check the unit. "
                f"Recovery resumes automatically when output returns "
                f"(or the port reports OFF)."
            )
        return "error"
    # Fire when the pacing interval allows. Note attempts==0 no longer
    # bypasses pacing: last_attempt_ts survives resets, so an arm/clear/
    # re-arm flap can't fire faster than retry_interval_s.
    if (now - state.last_attempt_ts) >= retry_interval_s:
        state.consecutive_attempts += 1
        state.last_attempt_ts = now
        state.episode_cycles += 1
        return "cycle"
    return "waiting"


def record_user_off(state: WatchdogState, now_ts: float | None = None) -> None:
    """Stamp the user-initiated AC-off timestamp. Called from the AC
    toggle endpoint when the user clicks AC OFF in our UI. Suppresses
    hardware-trip collapse detection during the device's apply window."""
    state.last_user_off_ts = float(now_ts if now_ts is not None else time.time())


def dismiss_error(state: WatchdogState) -> None:
    """Reset the latched error. Next trip observation starts a fresh
    recovery sequence. Called from the UI's "dismiss" click."""
    state.consecutive_attempts = 0
    state.last_attempt_ts = 0.0
    state.error_message = None
    state.hw_trip_active = False
    state.episode_cycles = 0
    state.first_low_ts = 0.0
    state.low_samples = 0


# ---------- Per-device runtime registry ----------
_runtime: dict[str, WatchdogState] = {}
_runtime_lock = threading.Lock()


def get_state(device_sn: str) -> WatchdogState:
    """Return (and lazily create) the per-device watchdog state."""
    with _runtime_lock:
        if device_sn not in _runtime:
            _runtime[device_sn] = WatchdogState()
        return _runtime[device_sn]


def reset_state(device_sn: str | None = None) -> None:
    """Clear cached state. Test hook; also called by callers that
    explicitly want a fresh slate (e.g. dismiss-error endpoint)."""
    with _runtime_lock:
        if device_sn:
            _runtime.pop(device_sn, None)
        else:
            _runtime.clear()


def state_to_dict(state: WatchdogState) -> dict:
    """JSON-friendly snapshot for the UI."""
    return {
        "consecutive_attempts": state.consecutive_attempts,
        "last_attempt_ts": state.last_attempt_ts,
        "last_user_off_ts": state.last_user_off_ts,
        "error_message": state.error_message,
        "hw_trip_active": state.hw_trip_active,
        "episode_cycles": state.episode_cycles,
        "low_samples": state.low_samples,
        "last_high_output_ts": state.last_high_output_ts,
    }
