"""Offline, deterministic end-to-end forecast and drain parity fixtures.

Run from the repository root: PYTHONPATH=python python python/scripts/build-forecast-fixtures.py
"""
import os
import time
os.environ['TZ'] = 'UTC'
time.tzset()
import json
import gzip
import random
from pathlib import Path
import forecaster as f
r = random.Random(7852)
now = 1736892900
cases = []
for i in range(48):
    rows, weather = [], []
    cap = r.choice([3024, 5040, 30240])
    for h in range(24 * 10):
        ts = now - (240 - h) * 3600
        sol = max(0, 1000 - abs(h % 24 - 12) * 170)
        rows.append(dict(ts=ts, output_w=r.choice([0, 50, 100, 600]),
                         output_wh=r.choice([100, 500, 800]), solar_w=sol,
                         solar_wh=sol, ac_input_wh=0, input_wh=sol,
                         battery_pct=98 - h % 12 * 2,
                         **({'system_soc': 98 - h % 12 * .6} if i % 2 else {})))
    # Separate clean discharge windows force both narrow and wide fit branches.
    if i % 3:
        for h, row in enumerate(rows):
            load = 250 if i % 3 == 1 else [80, 100, 300, 700, 1000][h % 5]
            row.update(solar_wh=0, ac_input_wh=0, output_wh=load)
    for h in range(-240, 169):
        ts = now // 3600 * 3600 + h * 3600
        weather.append(dict(ts=ts, ghi_w_m2=max(0, 900 - abs(h % 24 - 12)*160), cloud_cover_pct=r.choice([0, 20, 90])))
    kw = dict(now_ts=now, pack_count=i % 4, utc_offset_seconds=r.choice([0, -21600, 19800]),
              ac_charge_floor_pct=r.choice([None, 25]), extra_load_w=r.choice([0, 600]),
              extra_load_floor_pct=r.choice([None, 30]), sleep_start='23:00', sleep_end='06:00')
    if i % 4 == 0:
        kw.update(load_mode='scheduled', load_windows=[dict(start='08:00', end='20:00', watts=200, days='all')])
    if i % 5 == 0:
        kw['fs_hourly'] = {int(w['ts'])-3600: 500 for w in weather[240:250]}
    if i == 0:
        rows = rows[:3]
    cases.append(dict(history=rows, weather=weather, capacity=cap, start=74.2, options=kw,
                      expected=f.build_forecast(rows, weather, 74.2, cap, **kw),
                      drain=f.fit_drain_model(rows, cap, pack_count=kw['pack_count']),
                      overhead=f.fit_inverter_overhead_pct(rows, cap)))
Path('internal/forecast/testdata/build.json.gz').write_bytes(gzip.compress(json.dumps(cases).encode(), mtime=0))
# Freeze the reference wall clock at the historical decision for replay.
# backtest.py itself does not pass now_ts into build_forecast.
import backtest
from unittest.mock import patch
replays=[]
for c in cases[1:17]:
    decisions=[dict(decided_at=now, sunrise_ts=now+10*3600, current_soc_pct=32,
                    target_sunrise_soc_pct=45, action='off', reason='old policy',
                    predicted_sunrise_soc_pct=20)]
    with patch('forecaster.time.time', return_value=now):
        expected=backtest.replay_decisions(decisions=decisions, full_energy_history=c['history'],
            weather_observations=c['weather'], capacity_wh=c['capacity'], max_charge_w=800,
            cost_plan={'type':'flat','rate_per_kwh':.12},tz_offset_seconds=0)
    replays.append(dict(history=c['history'],weather=c['weather'],capacity=c['capacity'],decisions=decisions,expected=expected,summary=backtest.summarize(expected)))
Path('internal/forecast/testdata/replay.json.gz').write_bytes(gzip.compress(json.dumps(replays).encode(), mtime=0))
