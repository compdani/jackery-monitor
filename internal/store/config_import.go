package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/pocketbase/pocketbase/core"
)

// ConfigSource is explicit: only allowlisted filenames are read. A manifest can
// relocate individual files without scanning a data directory or opening a DB.
type ConfigSource struct {
	Name, Env, File, Collection, Key, Shape string
	Secret                                  bool
}

var ConfigSources = []ConfigSource{
	{"ecoflow_reconnect", "JACKERY_ECOFLOW_RECONNECT_FILE", "ecoflow_reconnect.json", "settings", "ecoflow_reconnect", "object", false},
	{"solar_charge_overload", "JACKERY_SOLAR_CHARGE_OVERLOAD_FILE", "solar_charge_overload.json", "settings", "solar_charge_overload", "object", false},
	{"forecast_solar", "JACKERY_FORECAST_SOLAR_CREDS_FILE", "forecast-solar-creds.json", "credentials", "forecast_solar", "object", true},
	{"settings", "JACKERY_SETTINGS_FILE", "settings.json", "settings", "settings", "object", false},
	{"device_prefs", "JACKERY_DEVICE_PREFS_FILE", "device_prefs.json", "device_prefs", "", "prefs", false},
	{"automation", "JACKERY_RULES_FILE", "automation.json", "automation_rules", "", "rules", false},
	{"kasa_devices", "JACKERY_KASA_DEVICES_FILE", "kasa_devices.json", "settings", "kasa_devices", "object", false},
	{"ecoflow_devices", "JACKERY_ECOFLOW_DEVICES_FILE", "ecoflow_devices.json", "settings", "ecoflow_devices", "object", false},
	{"bms_devices", "JACKERY_BMS_DEVICES_FILE", "bms_devices.json", "settings", "bms_devices", "object", false},
	{"siseli_devices", "JACKERY_SISELI_DEVICES_FILE", "siseli_devices.json", "settings", "siseli_devices", "object", false},
	{"smart_charge", "JACKERY_SMART_CHARGE_FILE", "smart_charge.json", "settings", "smart_charge", "object", false},
	{"solar_charge", "JACKERY_SOLAR_CHARGE_FILE", "solar_charge.json", "settings", "solar_charge", "object", false},
	{"f7_ac_reset", "JACKERY_F7_AC_RESET_FILE", "f7_ac_reset.json", "settings", "f7_ac_reset", "object", false},
	{"solar_array", "JACKERY_SOLAR_ARRAY_FILE", "solar_array.json", "settings", "solar_array", "object", false},
	{"load_schedule", "JACKERY_LOAD_SCHEDULE_FILE", "load_schedule.json", "settings", "load_schedule", "schedule", false},
	{"anthropic_prefs", "JACKERY_ANTHROPIC_PREFS_FILE", "anthropic-prefs.json", "settings", "anthropic_prefs", "ai_prefs", false},
	{"cost", "JACKERY_COST_FILE", "cost.json", "settings", "cost_plan", "object", false},
	{"location", "JACKERY_LOCATION_FILE", "location.json", "credentials", "location", "object", true},
	{"jackery", "JACKERY_CREDS_FILE", "jackery-creds.json", "credentials", "jackery", "object", true},
	{"anthropic", "JACKERY_ANTHROPIC_CREDS_FILE", "anthropic-creds.json", "credentials", "anthropic", "object", true},
	{"openai", "JACKERY_OPENAI_CREDS_FILE", "openai-creds.json", "credentials", "openai", "object", true},
	{"kasa", "JACKERY_KASA_CREDS_FILE", "kasa-creds.json", "credentials", "kasa", "object", true},
	{"siseli", "JACKERY_SISELI_CREDS_FILE", "siseli-creds.json", "credentials", "siseli", "object", true},
	{"ecoflow", "JACKERY_ECOFLOW_CREDS_FILE", "ecoflow-creds.json", "credentials", "ecoflow", "object", true},
	{"auth", "JACKERY_AUTH_FILE", "auth.json", "users", "", "user", true},
}

type SourceReport struct {
	Source     string   `json:"source"`
	Candidates int      `json:"candidates"`
	Imported   int      `json:"imported"`
	Skipped    int      `json:"skipped"`
	Errors     []string `json:"errors"`
	Note       string   `json:"note,omitempty"`
}
type ConfigImportOptions struct {
	Directory string
	Paths     map[string]string // explicit source-name overrides, e.g. from a manifest
	KeyPath   string
	DryRun    bool
}

// ReadExistingKey deliberately does not call LoadKey: import must never create
// directories, generate a key, or overwrite even a malformed existing key.
func ReadExistingKey(path string) ([]byte, error) {
	if path == "" {
		return nil, errors.New("JACKERY_AT_REST_KEY_FILE must identify the existing 32-byte key")
	}
	key, err := os.ReadFile(path)
	if err != nil {
		return nil, errors.New("existing at-rest key could not be read")
	}
	if len(key) != 32 {
		return nil, errors.New("existing at-rest key must contain exactly 32 bytes")
	}
	return key, nil
}

type configRecord struct {
	collection, key, device string
	payload                 map[string]any
	secret, user            bool
}

func decodeConfig(data, key []byte, secret bool) (map[string]any, error) {
	var object map[string]any
	if json.Unmarshal(data, &object) != nil || object == nil {
		return nil, errors.New("expected JSON object")
	}
	if _, encrypted := object["ct"]; encrypted {
		var env Envelope
		if json.Unmarshal(data, &env) != nil {
			return nil, errors.New("invalid encryption envelope")
		}
		plain, err := Decrypt(key, env)
		if err != nil {
			return nil, errors.New("could not decrypt with supplied existing key")
		}
		if json.Unmarshal(plain, &object) != nil || object == nil {
			return nil, errors.New("decrypted content is not a JSON object")
		}
	} else if secret {
		return nil, errors.New("encrypted source requires an AES-GCM envelope")
	}
	return object, nil
}
func configRecords(src ConfigSource, v map[string]any) ([]configRecord, error) {
	rec := func(key, device string, p map[string]any) configRecord {
		return configRecord{src.Collection, key, device, p, src.Secret, src.Shape == "user"}
	}
	switch src.Shape {
	case "schedule":
		out := []configRecord{}
		for id, raw := range v {
			p, ok := raw.(map[string]any)
			if !ok || id == "" {
				return nil, errors.New("invalid load schedule")
			}
			out = append(out, rec("load_schedule:"+id, id, p))
		}
		return out, nil
	case "ai_prefs":
		out := []configRecord{rec(src.Key, "", v)}
		if provider, ok := v["provider"].(string); ok {
			out = append(out, rec("ai_provider", "", map[string]any{"provider": provider}))
		}
		return out, nil
	case "prefs":
		out := []configRecord{}
		for id, raw := range v {
			p, ok := raw.(map[string]any)
			if !ok || strings.TrimSpace(id) == "" {
				return nil, errors.New("invalid device preference record")
			}
			out = append(out, rec(id, id, p))
		}
		return out, nil
	case "rules":
		list, ok := v["rules"].([]any)
		if !ok {
			return nil, errors.New("expected rules array")
		}
		out := []configRecord{}
		seen := map[string]bool{}
		for _, raw := range list {
			p, ok := raw.(map[string]any)
			if !ok {
				return nil, errors.New("invalid rule")
			}
			id, _ := p["id"].(string)
			if id == "" || seen[id] {
				return nil, errors.New("rules require unique stable ids")
			}
			seen[id] = true
			device, _ := p["device_sn"].(string)
			out = append(out, rec(id, device, p))
		}
		return out, nil
	case "user":
		name, _ := v["username"].(string)
		hash, _ := v["password_hash"].(string)
		if strings.TrimSpace(name) == "" || !ValidLegacyHash(hash) {
			return nil, errors.New("invalid legacy username or PBKDF2 hash; source retained, password reset required")
		}
		return []configRecord{rec(name, "", v)}, nil
	default:
		return []configRecord{rec(src.Key, "", v)}, nil
	}
}

// ImportConfig is insert-only, per-source atomic and safe to repeat. Existing
// destination records win; conflicts never replace native settings or users.
// With nil Store/App, dry-run validates sources only, without opening the
// destination. Reports never contain plaintext credentials or password hashes.
func (s *Store) ImportConfig(ctx context.Context, opt ConfigImportOptions) ([]SourceReport, error) {
	info, err := os.Stat(opt.Directory)
	if err != nil || !info.IsDir() {
		return nil, errors.New("legacy source directory must exist")
	}
	key, err := ReadExistingKey(opt.KeyPath)
	if err != nil {
		return nil, err
	}
	if !opt.DryRun && (s == nil || s.App == nil) {
		return nil, errors.New("destination required")
	}
	reports := []SourceReport{}
	failed := false
	for _, src := range ConfigSources {
		if err := ctx.Err(); err != nil {
			return reports, err
		}
		report := SourceReport{Source: src.Name, Errors: []string{}}
		path := opt.Paths[src.Name]
		if path == "" {
			path = filepath.Join(opt.Directory, src.File)
		}
		data, err := os.ReadFile(path)
		if os.IsNotExist(err) {
			report.Skipped = 1
			report.Note = "source missing"
			reports = append(reports, report)
			continue
		}
		var records []configRecord
		if err == nil {
			var v map[string]any
			v, err = decodeConfig(data, key, src.Secret)
			if err == nil {
				records, err = configRecords(src, v)
			}
		} else {
			err = errors.New("source could not be read")
		}
		if err != nil {
			report.Errors = append(report.Errors, err.Error())
			failed = true
			reports = append(reports, report)
			continue
		}
		report.Candidates = len(records)
		apply := func(app core.App) error {
			for _, rec := range records {
				if err := ctx.Err(); err != nil {
					return err
				}
				exists := false
				if app != nil {
					field := "key"
					if rec.user {
						field = "username"
					}
					_, e := app.FindFirstRecordByData(rec.collection, field, rec.key)
					if e != nil && !errors.Is(e, sql.ErrNoRows) {
						return errors.New("destination lookup failed")
					}
					exists = e == nil
				}
				if exists {
					report.Skipped++
					continue
				}
				if opt.DryRun {
					continue
				}
				st := &Store{App: app}
				if rec.user {
					c, e := app.FindCollectionByNameOrId("users")
					if e != nil {
						return e
					}
					u := core.NewRecord(c)
					u.Set("username", rec.key)
					transition := ID() + ID()
					u.SetPassword(transition)
					rec.payload["_transition_password"] = transition
					u.Set("verified", true)
					if e = app.Save(u); e != nil {
						return errors.New("legacy account could not be created")
					}
					if e = st.SaveSecret(key, "legacy_user:"+u.Id, rec.payload); e != nil {
						return e
					}
				} else if rec.secret {
					if e := st.SaveSecret(key, rec.key, rec.payload); e != nil {
						return e
					}
				} else {
					if e := st.Put(rec.collection, rec.key, rec.device, rec.payload); e != nil {
						return e
					}
				}
				report.Imported++
			}
			return nil
		}
		if opt.DryRun {
			var app core.App
			if s != nil {
				app = s.App
			}
			err = apply(app)
			report.Note = "dry-run; candidates are planned inserts; unopened destinations are not checked"
		} else {
			err = s.App.RunInTransaction(apply)
		}
		if err != nil {
			report.Imported = 0
			report.Errors = append(report.Errors, "source transaction failed; no records from this source committed")
			failed = true
		}
		if src.Shape == "user" {
			report.Note += " legacy PBKDF2 retained encrypted; first successful login upgrades to PocketBase password"
		}
		reports = append(reports, report)
	}
	if failed {
		return reports, fmt.Errorf("one or more sources failed; inspect per-source report")
	}
	return reports, nil
}
