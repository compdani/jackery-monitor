// Package tunables preserves the scalar runtime settings contract, backed by
// the app_settings collection instead of a shared JSON file.
package tunables

import (
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"math"
	"os"
	"strconv"

	"github.com/pocketbase/pocketbase/core"
)

//go:embed schema.json
var schemaJSON []byte

type Spec struct {
	Key     string `json:"key"`
	Env     string `json:"-"`
	Default int    `json:"default"`
	Type    string `json:"type"`
	Min     int    `json:"min"`
	Max     int    `json:"max"`
	Label   string `json:"label"`
	Hint    string `json:"hint"`
	Value   int    `json:"value"`
}

var order = []string{"poll_interval_s", "cloud_poll_interval_s", "session_contested_cooldown_s", "inverter_trip_recovery_min_w", "low_battery_threshold", "advisor_trigger_hour", "backup_schedule_hour", "backup_keep_count"}

func Schema() map[string]Spec {
	var specs map[string]Spec
	if err := json.Unmarshal(schemaJSON, &specs); err != nil {
		panic(err)
	}
	var envs map[string]struct {
		Env string `json:"env"`
	}
	if err := json.Unmarshal(schemaJSON, &envs); err != nil {
		panic(err)
	}
	for key, spec := range specs {
		spec.Key = key
		spec.Env = envs[key].Env
		specs[key] = spec
	}
	return specs
}
func clamp(value int, spec Spec) int { return min(spec.Max, max(spec.Min, value)) }
func values(app core.App) (map[string]int, *core.Record, error) {
	out := make(map[string]int)
	for key, spec := range Schema() {
		value := spec.Default
		if n, err := strconv.Atoi(os.Getenv(spec.Env)); err == nil {
			value = n
		}
		out[key] = clamp(value, spec)
	}
	record, err := app.FindFirstRecordByData("app_settings", "key", "tunables")
	if errors.Is(err, sql.ErrNoRows) {
		return out, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	var overrides map[string]int
	if err := record.UnmarshalJSONField("data", &overrides); err != nil {
		return nil, nil, err
	}
	for key, value := range overrides {
		if spec, ok := Schema()[key]; ok {
			out[key] = clamp(value, spec)
		}
	}
	return out, record, nil
}
func Get(e *core.RequestEvent) error {
	current, _, err := values(e.App)
	if err != nil {
		return err
	}
	specs := Schema()
	result := make([]Spec, 0, len(order))
	for _, key := range order {
		spec := specs[key]
		spec.Value = current[key]
		result = append(result, spec)
	}
	return e.JSON(200, map[string]any{"settings": result})
}
func Post(e *core.RequestEvent) error {
	var body map[string]any
	if err := e.BindBody(&body); err != nil || body == nil {
		return e.BadRequestError("body must be a JSON object", err)
	}
	var result map[string]int
	err := e.App.RunInTransaction(func(tx core.App) error {
		current, record, err := values(tx)
		if err != nil {
			return err
		}
		overrides := map[string]int{}
		if record != nil {
			if err := record.UnmarshalJSONField("data", &overrides); err != nil {
				return err
			}
		}
		for key, raw := range body {
			spec, ok := Schema()[key]
			if !ok {
				continue
			}
			var n float64
			switch value := raw.(type) {
			case float64:
				n = value
			case string:
				parsed, err := strconv.Atoi(value)
				if err != nil {
					continue
				}
				n = float64(parsed)
			default:
				continue
			}
			if math.IsNaN(n) || math.IsInf(n, 0) {
				continue
			}
			value := int(math.Min(float64(spec.Max), math.Max(float64(spec.Min), n)))
			overrides[key] = value
			current[key] = value
		}
		if record == nil {
			collection, err := tx.FindCollectionByNameOrId("app_settings")
			if err != nil {
				return err
			}
			record = core.NewRecord(collection)
			record.Set("key", "tunables")
		}
		record.Set("data", overrides)
		if err := tx.Save(record); err != nil {
			return err
		}
		result = current
		return nil
	})
	if err != nil {
		return err
	}
	return e.JSON(200, map[string]any{"ok": true, "settings": result})
}

// Value reads a runtime setting on each poll, so UI changes apply without restart.
func Value(app core.App, key string) int {
	current, _, err := values(app)
	if err != nil {
		return Schema()[key].Default
	}
	return current[key]
}
