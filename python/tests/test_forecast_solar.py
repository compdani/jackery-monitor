"""Forecast.Solar estimate URL, hourly resample, 429 backoff."""
from __future__ import annotations

from datetime import datetime, timedelta, timezone

import httpx
import pytest

import forecast_solar


@pytest.fixture(autouse=True)
def _clear():
    forecast_solar.clear_cache()
    yield
    forecast_solar.clear_cache()


def test_estimate_url_public():
    url = forecast_solar.estimate_url(37.3382, -121.8863, 20, 0, 2.4)
    assert url == "https://api.forecast.solar/estimate/37.3382/-121.8863/20/0/2.4"
    assert "/abc/" not in url


def test_estimate_url_with_api_key():
    url = forecast_solar.estimate_url(52.0, 12.0, 37, 0, 5.67, api_key="secret-key")
    assert url.startswith("https://api.forecast.solar/secret-key/estimate/")
    assert url.endswith("/37/0/5.67")


def test_resample_watts_hourly_means_subhour_samples():
    watts = {
        "2024-07-01 08:15:00": 100,
        "2024-07-01 08:45:00": 300,
        "2024-07-01 09:00:00": 50,
    }
    # Treat stamps as UTC (offset 0).
    hourly = forecast_solar.resample_watts_hourly(watts, utc_offset_seconds=0)
    eight = int(datetime(2024, 7, 1, 8, 0, tzinfo=timezone.utc).timestamp())
    nine = int(datetime(2024, 7, 1, 9, 0, tzinfo=timezone.utc).timestamp())
    assert hourly[eight] == 200  # mean of 100 and 300
    assert hourly[nine] == 50


def test_parse_fs_timestamp_applies_offset():
    # PDT = UTC-7. Local 08:00 → unix of 15:00 UTC.
    ts = forecast_solar.parse_fs_timestamp("2024-07-01 08:00:00", -7 * 3600)
    expect = int(datetime(2024, 7, 1, 15, 0, tzinfo=timezone.utc).timestamp())
    assert ts == expect


class _FakeResp:
    def __init__(self, status, body, headers=None):
        self.status_code = status
        self._body = body
        self.headers = headers or {}
        self.request = httpx.Request("GET", "https://api.forecast.solar/estimate/1/2/3/4/5")

    def json(self):
        return self._body

    def raise_for_status(self):
        if self.status_code >= 400:
            raise httpx.HTTPStatusError(
                "err", request=self.request, response=httpx.Response(self.status_code),
            )


class _FakeClient:
    def __init__(self, resp):
        self._resp = resp
        self.calls = 0

    async def __aenter__(self):
        return self

    async def __aexit__(self, *a):
        return False

    async def get(self, *a, **k):
        self.calls += 1
        if callable(self._resp):
            return self._resp()
        return self._resp


@pytest.mark.asyncio
async def test_fetch_estimate_resamples_watts(monkeypatch):
    body = {
        "result": {
            "watts": {
                "2024-07-01 12:00:00": 1400,
                "2024-07-01 13:00:00": 1500,
            }
        },
        "message": {"code": 0, "type": "success"},
    }
    client = _FakeClient(_FakeResp(200, body))
    monkeypatch.setattr(forecast_solar.httpx, "AsyncClient",
                        lambda *a, **k: client)
    out = await forecast_solar.fetch_estimate(52, 12, 37, 0, 5.67)
    assert not out.get("error")
    assert len(out["hourly"]) == 2
    assert {r["watts"] for r in out["hourly"]} == {1400, 1500}


@pytest.mark.asyncio
async def test_429_honors_retry_at_and_does_not_recall(monkeypatch):
    retry = (datetime.now(timezone.utc) + timedelta(hours=1)).isoformat()
    body = {"message": {"ratelimit": {"retry-at": retry, "limit": 12}}}
    client = _FakeClient(_FakeResp(429, body, {"X-Ratelimit-Retry-At": retry}))
    monkeypatch.setattr(forecast_solar.httpx, "AsyncClient",
                        lambda *a, **k: client)
    first = await forecast_solar.fetch_estimate(37, -122, 20, 0, 2.0)
    assert first.get("error")
    assert client.calls == 1
    second = await forecast_solar.fetch_estimate(37, -122, 20, 0, 2.0)
    assert "rate limited" in (second.get("error") or "")
    assert client.calls == 1  # retry-at honored; no second HTTP call
