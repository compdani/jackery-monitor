"""Regenerate reference vectors with the original Python implementation, no network."""
import json
import math
import os
from pathlib import Path
import sys
import time
ROOT = Path(__file__).resolve().parents[3]
sys.path.insert(0, str(ROOT))
os.environ['TZ'] = 'UTC'
time.tzset()
import forecaster
now = 1789041600  # deterministic epoch
now = now // 3600 * 3600
cases = []
for name, packs in [('solar', 0), ('no_panels', 0), ('packs', 2)]:
    history, weather = [], []
    for i in range(-336, 120):
        ts = now + i*3600
        hour = time.gmtime(ts).tm_hour
        ghi = max(0, math.sin((hour-6)*math.pi/12))*850 if 6 <= hour <= 18 else 0
        weather.append(dict(ts=ts, ghi_w_m2=ghi, cloud_cover_pct=10))
        if i < 0:
            solar = ghi*2 if name != 'no_panels' else 0
            load = 90 if hour < 6 else 300 + (i%5)*80
            history.append(dict(ts=ts,solar_w=solar,output_w=load,input_w=solar,ac_input_w=0,
                                solar_wh=solar,output_wh=load,input_wh=solar,ac_input_wh=0,
                                battery_pct=70+min(25,ghi/30),system_soc=65+min(25,ghi/30) if packs else None))
    expected = forecaster.build_forecast(history, weather, 70, 5040*(1+packs), now_ts=now, pack_count=packs)
    cases.append(dict(name=name, packs=packs, capacity=5040*(1+packs), start=70, now=now, history=history,weather=weather,expected=expected))
Path(__file__).with_name('python_vectors.json').write_text(json.dumps(cases,separators=(',',':'))+'\n')
