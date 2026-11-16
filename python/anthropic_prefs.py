"""User preferences for the Anthropic-powered Claude integrations.

Two roles, picked per call site:
  - advisor:  daily heavy review by claude_advisor.py (multi-turn,
              extended thinking, optional 1M context). Quality matters
              more than cost; default Opus 4.7 + 1M + high effort.
  - narrator: per-decision 1-2 sentence rationale by claude_narrator.py.
              Cheap + fast matters; default Haiku 4.5.

Per-role knobs:
  - advisor:  model, 1m_context (bool), thinking_effort
              (low|medium|high — adaptive thinking on Opus 4.7+)
  - narrator: model only

Storage: /data/anthropic-prefs.json (plain JSON; non-sensitive). The
encrypted-at-rest API key lives separately in anthropic_creds.py.

Reading: claude_advisor and claude_narrator call the getters at request
time, NOT at module load — so a UI change applies on the next tick
without a container restart.
"""
from __future__ import annotations

import json
import logging
import os
import threading
from typing import Any

log = logging.getLogger("anthropic_prefs")

PREFS_PATH = os.environ.get(
    "JACKERY_ANTHROPIC_PREFS_FILE", "/data/anthropic-prefs.json"
)

# Sensible defaults preserved from the pre-prefs era so that an existing
# deploy without the file behaves identically until the user opts in.
# advisor_1m_context=True matches the unconditional behavior of the
# old code, which always sent the context-1m beta header.
DEFAULTS: dict[str, Any] = {
    # Which AI backend is active. Exactly one at a time; the advisor +
    # narrator route to whichever is selected (and whichever has a key).
    "provider": "anthropic",
    # --- Anthropic ---
    "advisor_model": "claude-opus-4-7",
    "advisor_1m_context": True,
    "advisor_thinking_effort": "high",
    "narrator_model": "claude-haiku-4-5",
    # --- OpenAI-compatible (mirror of the Anthropic knobs) ---
    "openai_advisor_model": "o4-mini",
    "openai_narrator_model": "gpt-4o-mini",
    # Reasoning effort for o-series advisor models; ignored by non-
    # reasoning models (e.g. gpt-4o). Mirrors advisor_thinking_effort.
    "openai_advisor_effort": "high",
    # Endpoint: openai | openrouter | groq | ollama | custom.
    # Empty openai_base_url → use the preset's default URL.
    "openai_compat_preset": "openai",
    "openai_base_url": "",
}

# Roles the UI surfaces. Keep small; new roles need a corresponding
# getter caller and a default above.
ROLES: tuple[str, ...] = ("advisor", "narrator")

# The AI backends the UI can switch between. Exactly one active.
# "openai" means any OpenAI-compatible HTTP API (see openai_compat presets).
VALID_PROVIDERS: tuple[str, ...] = ("anthropic", "openai")

VALID_OPENAI_COMPAT_PRESETS: tuple[str, ...] = (
    "openai", "openrouter", "groq", "ollama", "custom",
)

# Adaptive-thinking effort levels per the Anthropic API spec. Higher
# = the model self-allocates a larger thinking budget. Reject other
# values to avoid silent typos misconfiguring the daily review.
VALID_THINKING_EFFORTS: tuple[str, ...] = ("low", "medium", "high")

_lock = threading.Lock()


def _load() -> dict:
    try:
        with open(PREFS_PATH) as f:
            data = json.load(f)
        return data if isinstance(data, dict) else {}
    except FileNotFoundError:
        return {}
    except Exception as e:
        log.warning("anthropic prefs file unreadable (%s); using defaults", e)
        return {}


def _save(data: dict) -> None:
    try:
        os.makedirs(os.path.dirname(PREFS_PATH) or ".", exist_ok=True)
        tmp = PREFS_PATH + ".tmp"
        with open(tmp, "w") as f:
            json.dump(data, f, indent=2)
        os.replace(tmp, PREFS_PATH)
    except Exception as e:
        log.error("failed to save anthropic prefs: %s", e)


def get_provider() -> str:
    """Active AI backend: 'anthropic' | 'openai' (OpenAI-compatible).
    Falls back to the default (anthropic) on missing/invalid values so
    existing deploys are unaffected until the user opts in."""
    with _lock:
        data = _load()
    val = data.get("provider")
    if isinstance(val, str) and val.strip().lower() in VALID_PROVIDERS:
        return val.strip().lower()
    return DEFAULTS["provider"]


def get_openai_compat_preset() -> str:
    """Which OpenAI-compatible gateway preset is selected."""
    with _lock:
        data = _load()
    val = data.get("openai_compat_preset")
    if isinstance(val, str) and val.strip().lower() in VALID_OPENAI_COMPAT_PRESETS:
        return val.strip().lower()
    return DEFAULTS["openai_compat_preset"]


def get_openai_base_url() -> str:
    """Custom / override base URL for the OpenAI-compatible client.
    Empty string means 'use the preset default' (or SDK default for
    official OpenAI)."""
    with _lock:
        data = _load()
    val = data.get("openai_base_url")
    if isinstance(val, str):
        return val.strip().rstrip("/")
    return ""


def get_model(role: str, provider: str | None = None) -> str:
    """Return the configured model for `role` ('advisor' | 'narrator')
    under `provider` (defaults to the active provider). Anthropic reads
    `<role>_model`; OpenAI reads `openai_<role>_model`. Falls back to
    DEFAULTS so callers never see None and the system runs out-of-the-box."""
    if role not in ROLES:
        raise ValueError(f"unknown role: {role!r}")
    prov = (provider or get_provider())
    key = f"openai_{role}_model" if prov == "openai" else f"{role}_model"
    with _lock:
        data = _load()
    val = data.get(key)
    if isinstance(val, str) and val.strip():
        return val.strip()
    return DEFAULTS[key]


def get_openai_effort() -> str:
    """Reasoning effort for the OpenAI advisor (o-series). Returns one of
    VALID_THINKING_EFFORTS, defaulting on schema drift."""
    with _lock:
        data = _load()
    val = data.get("openai_advisor_effort")
    if isinstance(val, str) and val.strip().lower() in VALID_THINKING_EFFORTS:
        return val.strip().lower()
    return DEFAULTS["openai_advisor_effort"]


def get_1m_context() -> bool:
    """Whether the advisor should opt into the 1M-context beta header
    on its next call. Only applies to the advisor — narrator outputs
    are tiny so 1M is never relevant there."""
    with _lock:
        data = _load()
    val = data.get("advisor_1m_context")
    if isinstance(val, bool):
        return val
    return bool(DEFAULTS["advisor_1m_context"])


def get_thinking_effort() -> str:
    """Adaptive-thinking effort for the advisor. Returns one of
    VALID_THINKING_EFFORTS, falling back to DEFAULT when an unknown
    value is persisted (defensive against schema drift)."""
    with _lock:
        data = _load()
    val = data.get("advisor_thinking_effort")
    if isinstance(val, str) and val.strip().lower() in VALID_THINKING_EFFORTS:
        return val.strip().lower()
    return DEFAULTS["advisor_thinking_effort"]


def get_all() -> dict[str, Any]:
    """Snapshot of all preferences with defaults filled in.

    Flat shape (matches the keys the UI/API contract expects); the
    UI reads `advisor_model`, `advisor_1m_context`,
    `advisor_thinking_effort`, `narrator_model` directly."""
    return {
        "provider": get_provider(),
        "advisor_model": get_model("advisor", provider="anthropic"),
        "advisor_1m_context": get_1m_context(),
        "advisor_thinking_effort": get_thinking_effort(),
        "narrator_model": get_model("narrator", provider="anthropic"),
        "openai_advisor_model": get_model("advisor", provider="openai"),
        "openai_narrator_model": get_model("narrator", provider="openai"),
        "openai_advisor_effort": get_openai_effort(),
        "openai_compat_preset": get_openai_compat_preset(),
        "openai_base_url": get_openai_base_url(),
    }


def set_models(*,
               provider: str | None = None,
               advisor_model: str | None = None,
               advisor_1m_context: bool | None = None,
               advisor_thinking_effort: str | None = None,
               narrator_model: str | None = None,
               openai_advisor_model: str | None = None,
               openai_narrator_model: str | None = None,
               openai_advisor_effort: str | None = None,
               openai_compat_preset: str | None = None,
               openai_base_url: str | None = None) -> dict[str, Any]:
    """Persist any non-None field; leave the rest untouched. Returns
    the resulting full snapshot. Invalid provider / effort values are
    silently dropped (no half-saves) so the file never holds a value a
    caller would reject at API call time.

    `openai_base_url` accepts "" to clear a custom override (unlike
    model fields, where empty means 'leave unchanged').
    """
    with _lock:
        data = _load()
        if provider:
            p = str(provider).strip().lower()
            if p in VALID_PROVIDERS:
                data["provider"] = p
        if advisor_model and advisor_model.strip():
            data["advisor_model"] = advisor_model.strip()
        if advisor_1m_context is not None:
            data["advisor_1m_context"] = bool(advisor_1m_context)
        if advisor_thinking_effort:
            v = str(advisor_thinking_effort).strip().lower()
            if v in VALID_THINKING_EFFORTS:
                data["advisor_thinking_effort"] = v
        if narrator_model and narrator_model.strip():
            data["narrator_model"] = narrator_model.strip()
        if openai_advisor_model and openai_advisor_model.strip():
            data["openai_advisor_model"] = openai_advisor_model.strip()
        if openai_narrator_model and openai_narrator_model.strip():
            data["openai_narrator_model"] = openai_narrator_model.strip()
        if openai_advisor_effort:
            v = str(openai_advisor_effort).strip().lower()
            if v in VALID_THINKING_EFFORTS:
                data["openai_advisor_effort"] = v
        if openai_compat_preset is not None:
            p = str(openai_compat_preset).strip().lower()
            if p in VALID_OPENAI_COMPAT_PRESETS:
                data["openai_compat_preset"] = p
                # Cloud presets: drop stale overrides when switching,
                # unless the caller also sent openai_base_url.
                # ollama/custom keep (or accept) a typed URL.
                if p not in ("custom", "ollama") and openai_base_url is None:
                    data["openai_base_url"] = ""
        if openai_base_url is not None:
            # Allow clearing with "".
            data["openai_base_url"] = str(openai_base_url).strip().rstrip("/")
        _save(data)
    return get_all()
