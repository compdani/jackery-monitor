package migrations

import (
	"github.com/pocketbase/pocketbase/core"
	m "github.com/pocketbase/pocketbase/migrations"
)

func init() {
	m.Register(func(app core.App) error {
		for _, name := range []string{"weather_observations", "weather_forecasts"} {
			c := core.NewBaseCollection(name)
			c.Fields.Add(&core.TextField{Name: "key", Required: true}, &core.TextField{Name: "device_sn"}, &core.NumberField{Name: "ts"}, &core.JSONField{Name: "payload", MaxSize: 10 << 20})
			c.AddIndex("idx_"+name+"_key", true, "key", "")
			c.AddIndex("idx_"+name+"_ts", false, "device_sn,ts", "")
			if err := app.Save(c); err != nil {
				return err
			}
		}
		return nil
	}, func(app core.App) error {
		for _, name := range []string{"weather_observations", "weather_forecasts"} {
			c, err := app.FindCollectionByNameOrId(name)
			if err != nil {
				return err
			}
			if err = app.Delete(c); err != nil {
				return err
			}
		}
		return nil
	})
}
