"""Capture actual compute_plan/evaluate invocations while running reference tests.
Run with the project's pytest environment. Python sources are never modified.
"""
import dataclasses
import copy
import json
import pathlib
import sys
import pytest

root = pathlib.Path(__file__).resolve().parent.parent
sys.path.insert(0, str(root))
rows = {"smart_charge": [], "solar_charge": [], "inverter_watchdog": []}
watchdog_calls = {}
def capture(frame, event, result):
    module = pathlib.Path(frame.f_code.co_filename).stem
    if module not in rows:
        return
    if module == "inverter_watchdog" and frame.f_code.co_name == "evaluate":
        if event == "call":
            args = {k: copy.deepcopy(frame.f_locals[k]) for k in frame.f_code.co_varnames[:frame.f_code.co_argcount + frame.f_code.co_kwonlyargcount]}
            args["state"] = dataclasses.asdict(args["state"])
            args["state"]["pending_count"] = len(args["state"].pop("pending_user_off_intents"))
            watchdog_calls[id(frame)] = args
        elif event == "return" and isinstance(result, str):
            state = dataclasses.asdict(frame.f_locals["state"])
            state.pop("pending_user_off_intents")
            rows[module].append({"input": watchdog_calls.pop(id(frame)), "action": result, "expected": state})
        return
    if event != "return":
        return
    name = frame.f_code.co_name
    if name == "compute_plan" and result is not None:
        args = {k: frame.f_locals[k] for k in frame.f_code.co_varnames[:frame.f_code.co_argcount + frame.f_code.co_kwonlyargcount]}
        if args.get("now_ts") is None:
            args["now_ts"] = result.decided_at
        rows[module].append({"input": args, "expected": dataclasses.asdict(result)})
    if name == "evaluate" and module == "inverter_watchdog" and result is not None:
        # The state before each call is captured separately by the call event.
        pass
sys.setprofile(capture)
status = pytest.main([str(root / "tests/test_smart_charge.py"), str(root / "tests/test_solar_charge.py"), str(root / "tests/test_inverter_watchdog.py"), "-q", "-p", "no:cacheprovider"])
sys.setprofile(None)
for module, cases in rows.items():
    if cases:
        (root / "internal/automation/testdata" / (module + ".json")).write_text(json.dumps(cases, indent=2) + "\n")
raise SystemExit(status)
