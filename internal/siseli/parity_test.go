package siseli

import (
	"encoding/json"
	"os"
	"testing"
)

func equalJSON(t *testing.T, got, want any) {
	t.Helper()
	a, e := json.Marshal(got)
	if e != nil {
		t.Fatal(e)
	}
	b, e := json.Marshal(want)
	if e != nil {
		t.Fatal(e)
	}
	var x, y any
	_ = json.Unmarshal(a, &x)
	_ = json.Unmarshal(b, &y)
	a, _ = json.Marshal(x)
	b, _ = json.Marshal(y)
	if string(a) != string(b) {
		t.Fatalf("got %s\nwant %s", a, b)
	}
}

// Expected values captured by executing the original Python tests, never Go code.
func TestPythonParity(t *testing.T) {
	data, e := os.ReadFile("testdata/python_parity.json")
	if e != nil {
		t.Fatal(e)
	}
	var cases []struct {
		Function string `json:"function"`
		Args     []any  `json:"args"`
		Kwargs   Values `json:"kwargs"`
		Expected any    `json:"expected"`
	}
	if e = json.Unmarshal(data, &cases); e != nil {
		t.Fatal(e)
	}
	for _, c := range cases {
		t.Run(c.Function, func(t *testing.T) {
			var got any
			var v Values
			if len(c.Args) > 0 {
				v = object(c.Args[0])
			}
			switch c.Function {
			case "map_energy_flow_fields":
				got = MapEnergyFlowFields(v)
			case "map_latest_state_fields":
				got = MapLatestStateFields(v)
			case "to_telemetry":
				got = ToTelemetry(v)
			case "normalize_device":
				got = NormalizeDevice(v)
			case "normalize_controls":
				got = NormalizeControls(v)
			case "default_live_control_keys":
				controls := []Values{}
				for _, x := range c.Args[0].([]any) {
					controls = append(controls, object(x))
				}
				got = DefaultLiveControlKeys(controls)
			case "apply_derived_values":
				ApplyDerivedValues(v)
				got = v
			case "decoded_to_canonical":
				got = DecodedToCanonical(v)
			case "probe_outcome":
				k := c.Kwargs
				got = ProbeOutcome(text(k["error"]), k["running"] == true, k["packets"] == true, k["decoded"] == true, object(k["readings"]))
			case "should_skip_portal_latest":
				k := c.Kwargs
				stale := 1800.
				if n, ok := Number(k["stale_s"]); ok {
					stale = n
				}
				got = ShouldSkipPortalLatest(k["running"] == true, object(k["entry"]), num(k["now"]), stale)
			case "describe_mqtt_streams":
				rows := []Values{}
				for _, x := range c.Args[0].([]any) {
					rows = append(rows, object(x))
				}
				var since *float64
				if n, ok := Number(c.Kwargs["since"]); ok {
					since = &n
				}
				got = DescribeMQTTStreams(rows, since)
			default:
				t.Fatalf("unhandled %s", c.Function)
			}
			equalJSON(t, got, c.Expected)
		})
	}
}
