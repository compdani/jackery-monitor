package migrations

import (
	"github.com/pocketbase/pocketbase/core"
	m "github.com/pocketbase/pocketbase/migrations"
)

func init() {
	m.Register(func(app core.App) error {
		c := core.NewBaseCollection("battery_packs")
		c.Fields.Add(&core.TextField{Name: "key", Required: true}, &core.TextField{Name: "device_sn"}, &core.NumberField{Name: "ts"}, &core.JSONField{Name: "payload", MaxSize: 10 << 20})
		c.AddIndex("idx_battery_packs_key", true, "key", "")
		c.AddIndex("idx_battery_packs_device_ts", false, "device_sn, ts", "")
		return app.Save(c)
	}, func(app core.App) error {
		c, e := app.FindCollectionByNameOrId("battery_packs")
		if e != nil {
			return e
		}
		return app.Delete(c)
	})
}
