// Package migrate imports legacy data without modifying the source files.
package migrate

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"jackery-monitor/internal/energy"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
)

var tables = map[string]string{"devices": "energy_devices", "samples": "samples", "forecast_predictions": "forecast_predictions", "smart_charge_decisions": "smart_charge_decisions", "solar_charge_decisions": "solar_charge_decisions", "weather_observations": "weather_observations", "weather_forecast": "weather_forecast", "daily_solar_summary": "daily_solar_summary", "battery_packs": "battery_packs", "algorithm_suggestions": "legacy_algorithm_suggestions", "algorithm_changes": "legacy_algorithm_changes", "automation_firings": "automation_firings", "device_params": "device_params"}

func quote(name string) string { return `"` + strings.ReplaceAll(name, `"`, `""`) + `"` }
func Run(app core.App, root string) error {
	path := filepath.Join(root, "energy.db")
	if _, err := os.Stat(path); err == nil {
		if err = ImportEnergy(app, path); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return ImportConfigs(app, root)
}
func ImportEnergy(app core.App, path string) error {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	rows, err := energy.Query(app, `SELECT source FROM legacy_imports WHERE source='energy.db'`, nil)
	if err != nil {
		return err
	}
	if len(rows) > 0 {
		return nil
	}
	u := url.URL{Scheme: "file", Path: absolute}
	q := u.Query()
	q.Set("mode", "ro")
	u.RawQuery = q.Encode()
	source, err := sql.Open("sqlite", u.String())
	if err != nil {
		return err
	}
	defer source.Close()
	src, err := source.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return err
	}
	defer src.Rollback()
	names, err := src.Query(`SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'`)
	if err != nil {
		return err
	}
	tableNames := []string{}
	for names.Next() {
		var name string
		if err = names.Scan(&name); err != nil {
			names.Close()
			return err
		}
		if _, ok := tables[name]; !ok {
			names.Close()
			return fmt.Errorf("unsupported legacy table %q; original database is unchanged", name)
		}
		tableNames = append(tableNames, name)
	}
	err = names.Err()
	names.Close()
	if err != nil {
		return err
	}
	if len(tableNames) == 0 {
		return errors.New("legacy energy database contains no recognized tables")
	}
	return app.RunInTransaction(func(tx core.App) error {
		report := map[string]any{"path": absolute, "tables": map[string]int64{}, "verified": true}
		counts := report["tables"].(map[string]int64)
		for _, name := range tableNames {
			target := tables[name]
			existing, err := energy.Query(tx, "SELECT COUNT(*) n FROM "+quote(target), nil)
			if err != nil {
				return err
			}
			if energy.Number(existing[0]["n"]) != 0 {
				return fmt.Errorf("refusing to import %s into nonempty %s; use an empty PocketBase data directory", name, target)
			}
			columns, err := energy.Query(tx, "PRAGMA table_info("+quote(target)+")", nil)
			if err != nil {
				return err
			}
			known := map[string]bool{}
			for _, c := range columns {
				known[c["name"].(string)] = true
			}
			data, err := src.Query("SELECT * FROM " + quote(name))
			if err != nil {
				return err
			}
			cols, err := data.Columns()
			if err != nil {
				data.Close()
				return err
			}
			quoted := []string{}
			params := []string{}
			for i, col := range cols {
				if !known[col] {
					data.Close()
					return fmt.Errorf("unsupported legacy column %s.%s; import rolled back", name, col)
				}
				quoted = append(quoted, quote(col))
				params = append(params, fmt.Sprintf("{:p%d}", i))
			}
			query := "INSERT INTO " + quote(target) + " (" + strings.Join(quoted, ",") + ") VALUES (" + strings.Join(params, ",") + ")"
			var count int64
			for data.Next() {
				values := make([]any, len(cols))
				refs := make([]any, len(cols))
				for i := range values {
					refs[i] = &values[i]
				}
				if err = data.Scan(refs...); err != nil {
					data.Close()
					return err
				}
				bound := dbx.Params{}
				for i, v := range values {
					bound[fmt.Sprintf("p%d", i)] = v
				}
				if _, err = tx.DB().NewQuery(query).Bind(bound).Execute(); err != nil {
					data.Close()
					return fmt.Errorf("import %s: %w", name, err)
				}
				count++
			}
			err = data.Err()
			data.Close()
			if err != nil {
				return err
			}
			counts[name] = count
			got, err := energy.Query(tx, "SELECT COUNT(*) n FROM "+quote(target), nil)
			if err != nil {
				return err
			}
			if int64(energy.Number(got[0]["n"])) != count {
				return errors.New("legacy row count verification failed")
			}
			if name == "samples" {
				for _, field := range []string{"input_wh", "output_wh", "solar_wh", "ac_input_wh", "solar_charge_diverted_wh"} {
					present := false
					for _, col := range cols {
						if col == field {
							present = true
						}
					}
					if !present {
						continue
					}
					var want float64
					if err = src.QueryRow("SELECT COALESCE(SUM(" + quote(field) + "),0) FROM samples").Scan(&want); err != nil {
						return err
					}
					sum, err := energy.Query(tx, "SELECT COALESCE(SUM("+quote(field)+"),0) total FROM samples", nil)
					if err != nil {
						return err
					}
					if math.Abs(energy.Number(sum[0]["total"])-want) > max(1e-6, math.Abs(want)*1e-10) {
						return fmt.Errorf("legacy %s total verification failed", field)
					}
				}
			}
		}
		raw, err := json.Marshal(report)
		if err != nil {
			return err
		}
		_, err = tx.DB().NewQuery(`INSERT INTO legacy_imports(source,imported_at,report) VALUES ('energy.db',{:now},{:report})`).Bind(dbx.Params{"now": time.Now().Unix(), "report": string(raw)}).Execute()
		return err
	})
}
func ImportConfigs(app core.App, root string) error {
	mapping := map[string][2]string{"settings.json": {"app_settings", "tunables"}, "automation.json": {"automation_rules", "legacy"}, "smart_charge.json": {"smart_charge_config", "legacy"}, "solar_charge.json": {"solar_charge_config", "legacy"}, "kasa_devices.json": {"kasa_devices", "legacy"}, "location.json": {"location", "default"}, "cost.json": {"cost_plan", "default"}}
	return app.RunInTransaction(func(tx core.App) error {
		for file, destination := range mapping {
			raw, err := os.ReadFile(filepath.Join(root, file))
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil {
				return err
			}
			var data any
			if err = json.Unmarshal(raw, &data); err != nil {
				return fmt.Errorf("invalid legacy %s: %w", file, err)
			}
			count, err := tx.CountRecords(destination[0])
			if err != nil {
				return err
			}
			if count > 0 {
				continue
			}
			collection, err := tx.FindCollectionByNameOrId(destination[0])
			if err != nil {
				return err
			}
			record := core.NewRecord(collection)
			record.Set("key", destination[1])
			record.Set("data", data)
			if err = tx.Save(record); err != nil {
				return err
			}
		}
		return nil
	})
}
