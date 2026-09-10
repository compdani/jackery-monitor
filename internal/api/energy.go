package api

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	catalog "jackery-monitor"

	"jackery-monitor/internal/energy"

	"jackery-monitor/internal/jackery"

	"jackery-monitor/internal/live"

	"jackery-monitor/internal/site"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
)

func localTime(app core.App) time.Time {
	l, err := site.Get(app)
	if err == nil {
		return time.Now().In(l.Zone())
	}
	return time.Now()
}

func recordLive(store *energy.Store, service *live.Service) error {
	snapshot := service.Snapshot("")
	cloud := snapshot["cloud"].(map[string]any)
	raw, _ := json.Marshal(cloud["devices"])
	var devices []jackery.Device
	if err := json.Unmarshal(raw, &devices); err != nil {
		return err
	}
	for _, device := range devices {
		snap := service.Snapshot(device.ID)
		tele, _ := snap["telemetry"].(map[string]any)
		if tele == nil {
			continue
		}
		if snap["battery_packs_ts"] == nil {
			packs, ts, err := store.Packs(device.SN)
			if err != nil {
				return err
			}
			if ts > 0 {
				service.HydratePacks(device.SN, packs, time.Unix(ts, 0))
				snap = service.Snapshot(device.ID)
				tele = snap["telemetry"].(map[string]any)
			}
		}
		ts := energy.Number(snap["last_update_ts"])
		if err := store.UpsertDevice(device, time.UnixMilli(int64(ts*1000))); err != nil {
			return err
		}
		reading := energy.Reading{TS: ts, Input: energy.Number(tele["input_power_w"]), Output: energy.Number(tele["output_power_w"]), Solar: energy.Number(tele["solar_input_w"]), Grid: energy.Number(tele["ac_input_w"]), SOC: energy.Nullable(tele["battery_percent"]), SystemSOC: energy.Nullable(tele["system_soc_pct"])}
		if err := store.Record(device.SN, reading); err != nil {
			return err
		}
		if stamp := int64(energy.Number(snap["battery_packs_ts"])); stamp > 0 {
			raw, err := json.Marshal(snap["battery_packs"])
			if err != nil {
				return err
			}
			var packs []map[string]any
			if err = json.Unmarshal(raw, &packs); err != nil {
				return err
			}
			if err = store.RecordPacks(device.SN, packs, stamp); err != nil {
				return err
			}
		}
	}
	return nil
}
func decorateEnergy(store *energy.Store, snapshot map[string]any) map[string]any {
	device, _ := snapshot["device"].(map[string]any)
	if device == nil {
		return snapshot
	}
	sn, _ := device["device_sn"].(string)
	if sn == "" {
		return snapshot
	}
	totals, err := store.Totals(sn, localTime(store.App))
	if err != nil {
		snapshot["energy_error"] = err.Error()
		return snapshot
	}
	snapshot["energy"] = totals
	history, err := store.RecentHistory(sn, time.Now())
	if err != nil {
		snapshot["energy_error"] = err.Error()
		return snapshot
	}
	if len(history) > 0 {
		points := []map[string]any{}
		for _, r := range history {
			soc := r["system_soc"]
			if soc == nil {
				soc = r["battery_pct"]
			}
			points = append(points, map[string]any{"ts": r["ts"], "battery_percent": soc, "input_power_w": r["input_w"], "output_power_w": r["output_w"], "solar_input_w": r["solar_w"]})
		}
		snapshot["history"] = points
	}
	overrides, err := energy.Query(store.App, `SELECT capacity_wh_override FROM energy_devices WHERE device_sn={:sn}`, dbx.Params{"sn": sn})
	if err != nil {
		snapshot["energy_error"] = err.Error()
		return snapshot
	}
	if len(overrides) > 0 && overrides[0]["capacity_wh_override"] != nil {
		if tele, ok := snapshot["telemetry"].(map[string]any); ok {
			tele["capacity_wh"] = overrides[0]["capacity_wh_override"]
		}
	}
	return snapshot
}
func registerEnergy(store *energy.Store, service *live.Service, route func(string, string, func(*core.RequestEvent) error)) {
	selectedSN := func(r *core.RequestEvent) string {
		if sn := r.Request.URL.Query().Get("device_sn"); sn != "" {
			return sn
		}
		snapshot := service.Snapshot(r.Request.URL.Query().Get("view_device_id"))
		if d, ok := snapshot["device"].(map[string]any); ok {
			return d["device_sn"].(string)
		}
		devices, err := store.Devices()
		if err == nil && len(devices) > 0 {
			return devices[0]["device_sn"].(string)
		}
		return ""
	}
	bounded := func(r *core.RequestEvent, key string, fallback, maximum int) (int, error) {
		raw := r.Request.URL.Query().Get(key)
		if raw == "" {
			return fallback, nil
		}
		value, err := strconv.Atoi(raw)
		if err != nil {
			return 0, r.BadRequestError(key+" must be an integer", nil)
		}
		return min(maximum, max(1, value)), nil
	}
	route("GET", "/energy/totals", func(r *core.RequestEvent) error {
		data, err := store.Totals(selectedSN(r), localTime(store.App))
		if err != nil {
			return err
		}
		return r.JSON(200, data)
	})
	route("GET", "/energy/history", func(r *core.RequestEvent) error {
		hours, err := bounded(r, "hours", 24, 8760)
		if err != nil {
			return err
		}
		bucket := max(60, hours*3600/120)
		sn := selectedSN(r)
		rows, err := store.History(sn, hours, bucket, time.Now())
		if err != nil {
			return err
		}
		return r.JSON(200, map[string]any{"device_sn": sn, "hours": hours, "bucket_s": bucket, "history": rows})
	})
	route("GET", "/energy/daily", func(r *core.RequestEvent) error {
		days, err := bounded(r, "days", 90, 365)
		if err != nil {
			return err
		}
		now := localTime(store.App)
		_, offset := now.Zone()
		sn := selectedSN(r)
		rows, err := store.Daily(sn, days, offset, now)
		if err != nil {
			return err
		}
		return r.JSON(200, map[string]any{"device_sn": sn, "days": days, "tz_offset_s": offset, "daily": rows})
	})
	route("GET", "/energy/devices", func(r *core.RequestEvent) error {
		devices, err := store.Devices()
		if err != nil {
			return err
		}
		result := []map[string]any{}
		for _, d := range devices {
			totals, err := store.Totals(d["device_sn"].(string), localTime(store.App))
			if err != nil {
				return err
			}
			totals["name"] = d["name"]
			result = append(result, totals)
		}
		return r.JSON(200, map[string]any{"devices": result})
	})
	route("GET", "/devices/capacity", func(r *core.RequestEvent) error {
		devices, err := store.Devices()
		if err != nil {
			return err
		}
		result := []map[string]any{}
		for _, d := range devices {
			main, pack, known := catalog.Capacity(int(energy.Number(d["model_code"])))
			packs, _, err := store.Packs(d["device_sn"].(string))
			if err != nil {
				return err
			}
			effective := main + len(packs)*pack
			var auto any
			if len(packs) > 0 {
				auto = effective
			}
			if v := d["capacity_wh_override"]; v != nil {
				effective = int(energy.Number(v))
			}
			result = append(result, map[string]any{"device_sn": d["device_sn"], "name": d["name"], "model_code": d["model_code"], "model_recognized": known, "default_capacity_wh": main, "capacity_wh_override": d["capacity_wh_override"], "auto_capacity_wh": auto, "pack_count": len(packs), "effective_capacity_wh": effective})
		}
		return r.JSON(200, map[string]any{"devices": result})
	})
	route("POST", "/devices/capacity", func(r *core.RequestEvent) error {
		var body struct {
			SN       string `json:"device_sn"`
			Capacity *int   `json:"capacity_wh"`
		}
		if err := r.BindBody(&body); err != nil {
			return r.BadRequestError("Invalid capacity", err)
		}
		if body.Capacity != nil && *body.Capacity == 0 {
			body.Capacity = nil
		}
		if err := store.SetCapacity(body.SN, body.Capacity); err != nil {
			return r.JSON(400, map[string]string{"detail": err.Error()})
		}
		return r.JSON(200, map[string]any{"ok": true, "device_sn": body.SN, "capacity_wh_override": body.Capacity})
	})
	route("GET", "/devices/battery_packs", func(r *core.RequestEvent) error {
		sn := selectedSN(r)
		packs, ts, err := store.Packs(sn)
		if err != nil {
			return err
		}
		return r.JSON(200, map[string]any{"device_sn": sn, "packs": packs, "fetched_at": ts, "cached": true})
	})
	route("GET", "/migration/status", func(r *core.RequestEvent) error {
		rows, err := energy.Query(store.App, `SELECT source,imported_at,report FROM legacy_imports ORDER BY imported_at`, nil)
		if err != nil {
			return err
		}
		for _, row := range rows {
			var report any
			if err = json.Unmarshal([]byte(fmt.Sprint(row["report"])), &report); err != nil {
				return err
			}
			row["report"] = report
		}
		return r.JSON(200, map[string]any{"imports": rows})
	})
}

// Keep cancellation separate from the cloud network loop so MQTT deltas are
// integrated while HTTP is paused or waiting on the provider.
func persistEnergy(ctx context.Context, store *energy.Store, service *live.Service, onError func(error)) {
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		onError(recordLive(store, service))
		timer.Reset(2 * time.Second)
	}
}
