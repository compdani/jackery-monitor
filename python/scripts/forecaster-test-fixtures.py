"""Capture actual pure forecast calls exercised by the unmodified pytest suite."""
import copy
import json
import gzip
import os
from pathlib import Path
import sys
import time
import pytest
root = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(root))
os.environ['TZ'] = 'UTC'
time.tzset()
names = {'fit_drain_model', 'fit_inverter_overhead_pct', 'fit_max_charge_w', 'summarize_forecast_days', 'summarize_ev_divert', 'solar_calibration_stats', 'forecast_readiness'}
cases = []
def capture(frame, event, result):
    if event != 'return' or Path(frame.f_code.co_filename).name != 'forecaster.py' or frame.f_code.co_name not in names:
        return
    args = {k: copy.deepcopy(frame.f_locals[k]) for k in frame.f_code.co_varnames[:frame.f_code.co_argcount + frame.f_code.co_kwonlyargcount]}
    cases.append(dict(name=frame.f_code.co_name, input=args, expected=copy.deepcopy(result)))
sys.setprofile(capture)
status = pytest.main([str(root/'tests/test_forecaster.py'), '-q', '-p', 'no:cacheprovider'])
sys.setprofile(None)
(root.parent/'internal/forecast/testdata/forecaster-tests.json.gz').write_bytes(gzip.compress(json.dumps(cases).encode(), mtime=0))
raise SystemExit(status)
