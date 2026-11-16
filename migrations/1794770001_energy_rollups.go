package migrations

import (
	"github.com/pocketbase/pocketbase/core"
	m "github.com/pocketbase/pocketbase/migrations"
)

func init() {
	m.Register(func(app core.App) error {
		c := core.NewBaseCollection("energy_minutes")
		c.Fields.Add(&core.TextField{Name: "device_sn", Required: true}, &core.NumberField{Name: "ts"})
		for _, name := range []string{"input_wh", "output_wh", "solar_wh", "ac_input_wh", "solar_charge_diverted_wh", "solar_w", "output_w", "soc"} {
			c.Fields.Add(&core.NumberField{Name: name})
		}
		c.Fields.Add(&core.BoolField{Name: "has_soc"})
		c.AddIndex("idx_energy_minutes_device_ts", true, "device_sn, ts", "")
		if err := app.Save(c); err != nil {
			return err
		}
		v := core.NewViewCollection("energy_daily_stats")
		v.ViewQuery = `SELECT min(id) AS id, device_sn, cast(ts/86400 AS INTEGER)*86400 AS ts,
  sum(input_wh) AS input_wh, sum(output_wh) AS output_wh, sum(solar_wh) AS solar_wh,
  sum(ac_input_wh) AS ac_input_wh, sum(solar_charge_diverted_wh) AS solar_charge_diverted_wh,
  max(solar_w) AS peak_solar_w, max(output_w) AS peak_output_w,
  min(CASE WHEN has_soc THEN soc ELSE NULL END) AS min_soc,
  max(CASE WHEN has_soc THEN soc ELSE NULL END) AS max_soc
  FROM energy_minutes GROUP BY device_sn, cast(ts/86400 AS INTEGER)`
		return app.Save(v)
	}, func(app core.App) error {
		for _, name := range []string{"energy_daily_stats", "energy_minutes"} {
			c, err := app.FindCollectionByNameOrId(name)
			if err != nil {
				return err
			}
			if err := app.Delete(c); err != nil {
				return err
			}
		}
		return nil
	})
}
