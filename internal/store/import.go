package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	_ "modernc.org/sqlite"
	"net/url"
)

// ImportLegacy reads an existing database in read-only mode. Deterministic
// source/table/content keys make reruns idempotent. JSON payloads retain every
// original field; this is archival import, not a claim of query parity.
func (s *Store) ImportLegacy(ctx context.Context, path string) (map[string]int, error) {
	u := url.URL{Scheme: "file", Path: path}
	q := u.Query()
	q.Set("mode", "ro")
	u.RawQuery = q.Encode()
	db, e := sql.Open("sqlite", u.String())
	if e != nil {
		return nil, e
	}
	defer db.Close()
	tables := map[string]string{"devices": "devices", "samples": "samples", "forecast_predictions": "predictions", "smart_charge_decisions": "automation_decisions", "solar_charge_decisions": "automation_decisions", "algorithm_suggestions": "suggestions", "algorithm_changes": "changes", "automation_firings": "automation_decisions", "device_params": "forecast_models", "assistant_conversations": "conversations", "assistant_messages": "messages", "assistant_attachments": "attachments", "assistant_tool_runs": "tool_runs"}
	counts := map[string]int{}
	for table, collection := range tables {
		var exists int
		if e = db.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?", table).Scan(&exists); e != nil {
			return counts, e
		}
		if exists == 0 {
			continue
		}
		// table comes exclusively from the static allowlist above.
		rows, e := db.QueryContext(ctx, "SELECT * FROM "+table)
		if e != nil {
			return counts, e
		}
		columns, e := rows.Columns()
		if e != nil {
			rows.Close()
			return counts, e
		}
		for rows.Next() {
			values := make([]any, len(columns))
			ptrs := make([]any, len(columns))
			for i := range values {
				ptrs[i] = &values[i]
			}
			if e = rows.Scan(ptrs...); e != nil {
				rows.Close()
				return counts, e
			}
			p := map[string]any{}
			for i, k := range columns {
				v := values[i]
				if b, ok := v.([]byte); ok {
					v = string(b)
				}
				p[k] = v
			}
			data, e := json.Marshal(p)
			if e != nil {
				rows.Close()
				return counts, e
			}
			hash := sha256.Sum256(append([]byte(table+":"), data...))
			key := "legacy:" + table + ":" + hex.EncodeToString(hash[:])
			if e = json.Unmarshal(data, &p); e != nil {
				rows.Close()
				return counts, e
			}
			for _, k := range []string{"ts", "bucket", "made_at", "decided_at", "created_at"} {
				if v, ok := p[k].(float64); ok {
					p["ts"] = v
					break
				}
			}
			p["_legacy_table"] = table
			sn, _ := p["device_sn"].(string)
			if table == "devices" {
				p = map[string]any{"device": p}
			}
			if e = s.Put(collection, key, sn, p); e != nil {
				rows.Close()
				return counts, fmt.Errorf("%s: %w", table, e)
			}
			counts[table]++
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return counts, e
		}
	}
	return counts, nil
}
