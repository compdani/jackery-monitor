package store

import (
	"encoding/json"
	"github.com/pocketbase/dbx"
)

func decodeEnergyPayload(payload string) (map[string]any, error) {
	var row map[string]any
	err := json.Unmarshal([]byte(payload), &row)
	return row, err
}

// The settings contract caps Siseli polling at one hour; its integration
// tolerance is 1.5 * 3600 + 30. Share that ceiling with indexed neighbor lookups.
const MaxEnergyGapSeconds = 5430.0

// Boundary neighbors are raw observations, not authoritative minute/portal rows.
// Looking them up explicitly keeps reconstruction independent of polling cadence.
func (s *Store) rawEnergyNeighbor(sn string, ts float64, before bool) (map[string]any, error) {
	op, order := ">", "ASC"
	if before {
		op, order = "<", "DESC"
	}
	var rows []struct {
		Payload string `db:"payload"`
	}
	err := s.App.DB().NewQuery(`SELECT payload FROM samples WHERE device_sn={:sn} AND ts ` + op + ` {:ts}
 AND ts >= {:min} AND ts <= {:max}
 AND json_extract(payload,'$.bucket') IS NULL
 AND coalesce(json_extract(payload,'$._energy_kind'),'') NOT IN ('aggregate','portal_solar')
 ORDER BY ts ` + order + `,id ` + order + ` LIMIT 1`).Bind(dbx.Params{"sn": sn, "ts": ts, "min": ts - MaxEnergyGapSeconds, "max": ts + MaxEnergyGapSeconds}).All(&rows)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	return decodeEnergyPayload(rows[0].Payload)
}
