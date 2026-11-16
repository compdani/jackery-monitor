"""Regenerate deterministic Go parity fixtures from the retained Python reference.
Run from the repository root: PYTHONPATH=python python python/scripts/forecast-fixtures.py
No hardware or network required.
"""
import os
import time
os.environ["TZ"] = "UTC"
time.tzset()
import json
import random
from pathlib import Path
import forecaster as f

rng = random.Random(932)
cases = []
for i in range(100):
    hours = [
        {"ts": 1700000000 + h * 3600, "solar_w": rng.randrange(0, 4000),
         "load_w": rng.randrange(0, 3000), "duration_h": rng.choice([0, .25, .5, 1, 2])}
        for h in range(48)
    ]
    kw = dict(ac_charge_floor_pct=rng.choice([None, 25, 40]),
              charge_efficiency=rng.choice([None, .8, .95]),
              extra_load_w=rng.choice([0, 600, 1300]),
              extra_load_floor_pct=rng.choice([None, 25, 40]),
              soc_ceiling_pct=rng.choice([None, 78, 95]))
    start, capacity = rng.randrange(-10, 110), rng.choice([2042, 5040, 30240])
    cases.append(dict(start=start, capacity=capacity, hours=hours, options=kw,
                      expected=f.simulate_soc(start, capacity, hours, **kw)))
solar = []
for i in range(30):
    energy, weather = [], []
    for h in range(50):
        ts = 1700000000 + h * 3600
        ghi, soc = rng.randrange(0, 1000), rng.randrange(0, 100)
        energy.append(dict(ts=ts, solar_w=ghi*rng.choice([.1, .3, 4]),
                           system_soc=soc, battery_pct=100, ac_input_w=rng.choice([0, 100])))
        weather.append(dict(ts=ts+3600, ghi_w_m2=ghi, cloud_cover_pct=rng.randrange(0, 100)))
    k, n = f.fit_solar_coefficient(energy, weather)
    solar.append(dict(energy=energy, weather=weather, k=k, n=n,
                      shape=f.fit_diurnal_shape(energy, weather, k, -25200)))
load = []
for i in range(20):
    rows = []
    for h in range(24*14):
        rows.append(dict(ts=1704067200+h*3600, output_w=rng.choice([0, 30, 100, 500, 2000]),
                         solar_charge_plug_on_frac=rng.choice([None, .5, 1]),
                         solar_charge_diverted_wh=rng.choice([0, 300]),
                         input_wh=rng.choice([0, 100, 500, 1000]),
                         output_wh=100, system_soc=(h%40)*.8+30,
                         solar_wh=rng.choice([0, 400, 1000])))
    profile = f.fit_load_profile(rows, now_ts=1704067200+24*14*3600, car_load_w=600)
    eff, n = f.fit_charge_efficiency(rows, 5040)
    ceiling = f.fit_charge_ceiling(rows, 5040)
    load.append(dict(rows=rows, profile=[dict(hour=k[0], weekend=k[1], value=v) for k,v in profile.items()],
                     efficiency=eff, windows=n, ceiling=ceiling))
Path("internal/forecast/testdata/python.json").write_text(json.dumps(dict(simulation=cases, solar=solar, load=load)))
