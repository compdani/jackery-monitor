"""OpenAI-compatible HTTP client factory.

The "openai" AI provider talks the OpenAI Chat Completions / Models API
shape, but the endpoint can be OpenAI itself, OpenRouter, Groq, Ollama,
or any custom base URL. Presets live in anthropic_prefs; this module
resolves them into an AsyncOpenAI client.
"""
from __future__ import annotations

from typing import Any

import anthropic_prefs

# Known OpenAI-compatible gateways. `base_url` None = SDK default
# (https://api.openai.com/v1). Keys match anthropic_prefs presets.
PRESETS: dict[str, dict[str, Any]] = {
    "openai": {
        "label": "OpenAI",
        "base_url": None,
        "hint": "api.openai.com — official OpenAI keys (sk-…)",
    },
    "openrouter": {
        "label": "OpenRouter",
        "base_url": "https://openrouter.ai/api/v1",
        "hint": "openrouter.ai — one key for many models (sk-or-…)",
    },
    "groq": {
        "label": "Groq",
        "base_url": "https://api.groq.com/openai/v1",
        "hint": "api.groq.com — fast inference (gsk_…)",
    },
    "ollama": {
        "label": "Ollama",
        "base_url": "http://host.docker.internal:11434/v1",
        "hint": "Local Ollama — base URL usually ends in /v1",
    },
    "custom": {
        "label": "Custom",
        "base_url": None,
        "hint": "Any OpenAI-compatible endpoint — set Base URL below",
    },
}

VALID_PRESETS: tuple[str, ...] = tuple(PRESETS.keys())


def list_presets() -> list[dict[str, str]]:
    """UI-facing preset catalog (id, label, default base_url, hint)."""
    out = []
    for pid, meta in PRESETS.items():
        out.append({
            "id": pid,
            "label": meta["label"],
            "base_url": meta["base_url"] or "",
            "hint": meta["hint"],
        })
    return out


def resolve_base_url(
    preset: str | None = None,
    base_url: str | None = None,
) -> str | None:
    """Return the effective base_url for the SDK, or None for OpenAI default.

    Explicit non-empty `base_url` wins. Otherwise use the preset default.
    Custom with no URL → None (caller should treat as misconfigured).
    """
    url = (base_url if base_url is not None
           else anthropic_prefs.get_openai_base_url())
    url = (url or "").strip().rstrip("/")
    if url:
        return url
    pid = (preset or anthropic_prefs.get_openai_compat_preset()).strip().lower()
    meta = PRESETS.get(pid) or PRESETS["openai"]
    default = meta.get("base_url")
    return default.rstrip("/") if isinstance(default, str) and default else None


def is_official_openai(base_url: str | None = None) -> bool:
    """True when traffic goes to api.openai.com (strict key + model filters)."""
    url = resolve_base_url(base_url=base_url)
    if not url:
        return True
    return "api.openai.com" in url.lower()


def default_headers(preset: str | None = None) -> dict[str, str] | None:
    """Optional headers some gateways expect (OpenRouter attribution)."""
    pid = (preset or anthropic_prefs.get_openai_compat_preset()).strip().lower()
    if pid == "openrouter":
        return {
            "HTTP-Referer": "https://github.com/dani/jackery-monitor",
            "X-Title": "Solar Pow Monitor",
        }
    return None


def make_async_client(api_key: str, *, timeout: float | None = 60.0):
    """Build an AsyncOpenAI client pointed at the configured compat endpoint."""
    from openai import AsyncOpenAI

    kwargs: dict[str, Any] = {"api_key": api_key}
    base = resolve_base_url()
    if base:
        kwargs["base_url"] = base
    headers = default_headers()
    if headers:
        kwargs["default_headers"] = headers
    if timeout is not None:
        kwargs["timeout"] = timeout
    return AsyncOpenAI(**kwargs)
