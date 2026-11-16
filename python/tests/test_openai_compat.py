"""openai_compat: OpenAI-compatible base URL presets."""
from __future__ import annotations

import importlib

import pytest


@pytest.fixture()
def compat(tmp_path, monkeypatch):
    monkeypatch.setenv("JACKERY_ANTHROPIC_PREFS_FILE",
                       str(tmp_path / "anthropic-prefs.json"))
    import anthropic_prefs
    import openai_compat
    importlib.reload(anthropic_prefs)
    importlib.reload(openai_compat)
    return openai_compat, anthropic_prefs


def test_presets_catalog(compat):
    openai_compat, _ = compat
    ids = {p["id"] for p in openai_compat.list_presets()}
    assert {"openai", "openrouter", "groq", "ollama", "custom"} <= ids


def test_openrouter_default_and_headers(compat):
    openai_compat, prefs = compat
    prefs.set_models(openai_compat_preset="openrouter")
    assert openai_compat.resolve_base_url() == "https://openrouter.ai/api/v1"
    headers = openai_compat.default_headers()
    assert headers and "HTTP-Referer" in headers
    assert openai_compat.is_official_openai() is False


def test_explicit_base_url_wins(compat):
    openai_compat, prefs = compat
    prefs.set_models(openai_compat_preset="openrouter",
                     openai_base_url="https://alt.example/v1")
    assert openai_compat.resolve_base_url() == "https://alt.example/v1"


def test_groq_and_ollama_defaults(compat):
    openai_compat, prefs = compat
    prefs.set_models(openai_compat_preset="groq")
    assert "groq.com" in (openai_compat.resolve_base_url() or "")
    prefs.set_models(openai_compat_preset="ollama")
    assert "11434" in (openai_compat.resolve_base_url() or "")
