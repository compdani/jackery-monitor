package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	"io"
	_ "modernc.org/sqlite"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type HistoryImportOptions struct {
	DryRun        bool
	AttachmentDir string
}
type Reconciliation struct {
	SourceCount      int                `json:"source_count"`
	DestinationCount int                `json:"destination_count"`
	SourceWh         map[string]float64 `json:"source_wh,omitempty"`
	DestinationWh    map[string]float64 `json:"destination_wh,omitempty"`
	AttachmentBytes  int64              `json:"attachment_bytes,omitempty"`
}
type historyTable struct {
	name, collection string
	keys             []string
}

var historyTables = []historyTable{
	{"devices", "devices", []string{"device_sn"}},
	{"samples", "samples", []string{"device_sn", "bucket"}},
	{"weather_observations", "weather_observations", []string{"ts"}},
	{"weather_forecast", "weather_forecasts", []string{"ts"}},
	{"daily_solar_summary", "forecast_models", []string{"device_sn", "date"}},
	{"battery_packs", "battery_packs", []string{"parent_sn", "ts", "pack_sn"}},
	{"forecast_predictions", "predictions", []string{"device_sn", "made_at", "target"}},
	{"device_params", "forecast_models", []string{"device_sn", "key"}},
	{"smart_charge_decisions", "automation_decisions", []string{"id"}},
	{"solar_charge_decisions", "automation_decisions", []string{"id"}},
	{"automation_firings", "automation_decisions", []string{"id"}},
	{"algorithm_suggestions", "suggestions", []string{"id"}},
	{"algorithm_changes", "changes", []string{"id"}},
	{"assistant_conversations", "conversations", []string{"id"}},
	{"assistant_messages", "messages", []string{"id"}},
	{"assistant_attachments", "attachments", []string{"id"}},
	{"assistant_tool_runs", "tool_runs", []string{"id"}},
}
var energyFields = []string{"input_wh", "output_wh", "solar_wh", "ac_input_wh", "solar_charge_diverted_wh"}

func num(v any) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case int64:
		return float64(x)
	case int:
		return float64(x)
	}
	return 0
}
func text(v any) string {
	if v == nil {
		return ""
	}
	if n, ok := v.(float64); ok {
		return strconv.FormatFloat(n, 'f', -1, 64)
	}
	return fmt.Sprint(v)
}
func SampleKey(sn string, bucket float64) string { return fmt.Sprintf("sample:%s:%.0f", sn, bucket) }
func historyIdentity(t historyTable, p map[string]any) (string, error) {
	parts := []any{}
	for _, k := range t.keys {
		if p[k] == nil {
			return "", fmt.Errorf("%s missing identity column %s", t.name, k)
		}
		parts = append(parts, p[k])
	}
	if t.name == "samples" {
		return SampleKey(text(p["device_sn"]), num(p["bucket"])), nil
	}
	if t.name == "devices" {
		return text(p["device_sn"]), nil
	}
	if strings.HasPrefix(t.name, "assistant_") {
		return text(p["id"]), nil
	}
	if t.name == "forecast_predictions" {
		return fmt.Sprintf("forecast:%s:%.0f:%.0f", text(p["device_sn"]), num(p["made_at"]), num(p["target"])), nil
	}
	if t.name == "daily_solar_summary" {
		return "daily:" + text(p["device_sn"]) + ":" + text(p["date"]), nil
	}
	data, _ := json.Marshal(parts)
	hash := sha256.Sum256(data)
	return "history:" + t.name + ":" + hex.EncodeToString(hash[:]), nil
}
func normalizeHistory(t historyTable, p map[string]any) (map[string]any, string) {
	sn := text(p["device_sn"])
	if t.name == "battery_packs" {
		sn = text(p["parent_sn"])
	}
	if t.name == "automation_firings" {
		sn = text(p["jackery_sn"])
	}
	if strings.HasPrefix(t.name, "assistant_") && t.name != "assistant_conversations" {
		sn = text(p["conversation_id"])
	}
	for _, k := range []string{"ts", "bucket", "made_at", "decided_at", "created_at", "applied_at", "fired_at", "updated_at", "last_seen"} {
		if p[k] != nil {
			p["ts"] = num(p[k])
			break
		}
	}
	for _, k := range []string{"parts", "args", "result", "current_value", "proposed_value", "old_value", "new_value"} {
		raw, ok := p[k+"_json"].(string)
		if !ok {
			raw, ok = p[k].(string)
			if !ok {
				continue
			}
		}
		var v any
		if json.Unmarshal([]byte(raw), &v) == nil {
			p[k] = v
		}
	}
	p["_legacy_table"] = t.name
	if t.name == "samples" {
		p["_energy_kind"] = "aggregate"
		p["battery_pct"] = p["last_battery_pct"]
	}
	if t.name == "battery_packs" {
		p["packs"] = []any{map[string]any{"pack_sn": p["pack_sn"], "rb": p["soc_pct"], "input_w": p["input_w"], "output_w": p["output_w"], "device_order": p["device_order"], "internal_temp_c": p["internal_temp_c"], "error_code": p["error_code"]}}
	}
	if t.name == "devices" {
		p = map[string]any{"device": p, "ts": num(p["last_seen"]), "capacity_wh_override": p["capacity_wh_override"], "_legacy_table": t.name}
	}
	return p, sn
}

// ImportHistory reads a checkpointed/offline SQLite copy using immutable+ro,
// preserving all columns. Source identity uses table primary keys, never row
// contents or a source path. One source table is committed atomically.
func (s *Store) ImportHistory(ctx context.Context, path string, opt HistoryImportOptions) (map[string]Reconciliation, error) {
	if info, err := os.Stat(path + "-wal"); err == nil && info.Size() > 0 {
		return nil, errors.New("use an offline checkpointed SQLite copy; source WAL is not empty")
	}
	path, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	u := url.URL{Scheme: "file", Path: path}
	q := u.Query()
	q.Set("mode", "ro")
	q.Set("immutable", "1")
	u.RawQuery = q.Encode()
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, err
	}
	defer db.Close()
	report := map[string]Reconciliation{}
	for _, spec := range historyTables {
		var exists int
		if err = db.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?", spec.name).Scan(&exists); err != nil {
			return report, err
		}
		if exists == 0 {
			continue
		}
		// Older decision logs use decided_at/device_sn as their primary identity.
		if spec.name == "smart_charge_decisions" || spec.name == "solar_charge_decisions" {
			spec.keys = []string{"device_sn", "decided_at"}
		}
		rows, err := db.QueryContext(ctx, "SELECT * FROM "+spec.name)
		if err != nil {
			return report, err
		}
		columns, err := rows.Columns()
		if err != nil {
			rows.Close()
			return report, err
		}
		r := Reconciliation{SourceWh: map[string]float64{}, DestinationWh: map[string]float64{}}
		apply := func(app core.App) error {
			st := &Store{App: app}
			seen := map[string]bool{}
			oldByIdentity := map[string][]*core.Record{}
			if !opt.DryRun {
				old, err := app.FindRecordsByFilter(spec.collection, "key ~ {:prefix}", "", 0, 0, dbx.Params{"prefix": "legacy:" + spec.name + ":"})
				if err != nil {
					return err
				}
				for _, record := range old {
					if !strings.HasPrefix(record.GetString("key"), "legacy:"+spec.name+":") {
						continue
					}
					prior, err := payload(record)
					if err != nil {
						return err
					}
					if spec.name == "devices" {
						prior, _ = prior["device"].(map[string]any)
					}
					identity, err := historyIdentity(spec, prior)
					if err == nil {
						oldByIdentity[identity] = append(oldByIdentity[identity], record)
					}
				}
			}
			for rows.Next() {
				if err := ctx.Err(); err != nil {
					return err
				}
				values := make([]any, len(columns))
				ptrs := make([]any, len(values))
				for i := range values {
					ptrs[i] = &values[i]
				}
				if err := rows.Scan(ptrs...); err != nil {
					return err
				}
				p := map[string]any{}
				for i, k := range columns {
					v := values[i]
					if b, ok := v.([]byte); ok {
						v = string(b)
					}
					p[k] = v
				}
				key, err := historyIdentity(spec, p)
				if err != nil {
					return err
				}
				if seen[key] {
					return errors.New("duplicate logical source identity")
				}
				seen[key] = true
				for _, field := range energyFields {
					if spec.name == "samples" {
						r.SourceWh[field] += num(p[field])
					}
				}
				r.SourceCount++
				p, sn := normalizeHistory(spec, p)
				if spec.name == "assistant_attachments" {
					attachment, err := s.prepareAttachment(p, opt)
					if err != nil {
						return err
					}
					r.AttachmentBytes += attachment
				}
				if opt.DryRun {
					continue
				}
				// A correction made in Go wins over future reruns of the legacy copy.
				if spec.name == "devices" {
					if existing, err := st.Get("devices", key); err == nil {
						for _, field := range []string{"telemetry", "last_update_ts", "battery_packs"} {
							if value, ok := existing[field]; ok {
								p[field] = value
							}
						}
						if existing["_capacity_corrected"] == true {
							p["capacity_wh_override"] = existing["capacity_wh_override"]
							p["_capacity_corrected"] = true
							device, _ := p["device"].(map[string]any)
							device["capacity_wh_override"] = existing["capacity_wh_override"]
						}
					}
				}
				if existing, e := st.Get(spec.collection, key); e == nil && existing["_corrected_output"] == true {
					for _, field := range []string{"output_wh", "last_output_w", "_corrected_output"} {
						p[field] = existing[field]
					}
				} else if e != nil && !errors.Is(e, sql.ErrNoRows) {
					return e
				}
				if err := st.Put(spec.collection, key, sn, p); err != nil {
					return err
				}
				// Remove only old archival duplicates of the same logical source row.
				for _, record := range oldByIdentity[key] {
					if err := app.Delete(record); err != nil {
						return err
					}
				}

				dest, err := st.Get(spec.collection, key)
				if err != nil {
					return err
				}
				r.DestinationCount++
				if spec.name == "samples" {
					for _, field := range energyFields {
						r.DestinationWh[field] += num(dest[field])
					}
				}
			}
			return rows.Err()
		}
		if opt.DryRun {
			err = apply(nil)
		} else {
			err = s.App.RunInTransaction(apply)
		}
		rows.Close()
		if err != nil {
			return report, fmt.Errorf("%s: %w", spec.name, err)
		}
		report[spec.name] = r
	}
	return report, nil
}

// Compatibility wrapper; new callers should print the full reconciliation.
func (s *Store) ImportLegacy(ctx context.Context, path string) (map[string]int, error) {
	report, err := s.ImportHistory(ctx, path, HistoryImportOptions{})
	counts := map[string]int{}
	for table, r := range report {
		counts[table] = r.SourceCount
	}
	return counts, err
}
func (s *Store) prepareAttachment(p map[string]any, opt HistoryImportOptions) (int64, error) {
	source := text(p["path"])
	if opt.AttachmentDir != "" {
		source = filepath.Join(opt.AttachmentDir, text(p["conversation_id"]), filepath.Base(source))
		if _, err := os.Stat(source); os.IsNotExist(err) {
			source = filepath.Join(opt.AttachmentDir, filepath.Base(text(p["path"])))
		}
	}
	info, err := os.Stat(source)
	if err != nil || !info.Mode().IsRegular() {
		return 0, errors.New("attachment source is missing or not a regular file")
	}
	f, err := os.Open(source)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	hash := sha256.New()
	size, err := io.Copy(hash, f)
	if err != nil {
		return 0, err
	}
	digest := hex.EncodeToString(hash.Sum(nil))
	if p["size"] != nil && int64(num(p["size"])) != size {
		return 0, errors.New("attachment size mismatch")
	}
	if opt.DryRun {
		return size, nil
	}
	dir := filepath.Join(s.App.DataDir(), "assistant_attachments")
	if err = os.MkdirAll(dir, 0700); err != nil {
		return 0, err
	}
	dest := filepath.Join(dir, digest)
	if data, err := os.ReadFile(dest); err == nil {
		sum := sha256.Sum256(data)
		if hex.EncodeToString(sum[:]) != digest {
			return 0, errors.New("destination attachment checksum mismatch")
		}
	} else if !os.IsNotExist(err) {
		return 0, err
	} else {
		if _, err = f.Seek(0, 0); err != nil {
			return 0, err
		}
		out, err := os.CreateTemp(dir, ".import-*")
		if err != nil {
			return 0, err
		}
		temp := out.Name()
		defer os.Remove(temp)
		copyHash := sha256.New()
		_, copyErr := io.Copy(io.MultiWriter(out, copyHash), f)
		syncErr := out.Sync()
		closeErr := out.Close()
		if copyErr != nil {
			return 0, copyErr
		}
		if syncErr != nil {
			return 0, syncErr
		}
		if closeErr != nil {
			return 0, closeErr
		}
		if hex.EncodeToString(copyHash.Sum(nil)) != digest {
			return 0, errors.New("attachment source changed during copy")
		}
		// Publish without replacing any pre-existing file.
		if err := os.Link(temp, dest); err != nil && !os.IsExist(err) {
			return 0, err
		}

	}
	p["legacy_path"] = p["path"]
	p["path"] = dest
	p["sha256"] = digest
	return size, nil
}
