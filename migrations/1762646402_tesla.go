package migrations

import (
	"github.com/pocketbase/pocketbase/core"
	m "github.com/pocketbase/pocketbase/migrations"
)

// TeslaCollections hold charge-state samples and charging sessions. Same
// locked {key, device_sn, ts, payload} shape as the core collections;
// device_sn is the VIN.
var TeslaCollections = []string{"tesla_samples", "tesla_sessions"}

func init() {
	m.Register(func(app core.App) error {
		for _, name := range TeslaCollections {
			c := core.NewBaseCollection(name)
			c.Fields.Add(&core.TextField{Name: "key", Required: true}, &core.TextField{Name: "device_sn"}, &core.NumberField{Name: "ts"}, &core.JSONField{Name: "payload", MaxSize: 1 << 20})
			c.AddIndex("idx_"+name+"_key", true, "key", "")
			c.AddIndex("idx_"+name+"_device_ts", false, "device_sn, ts", "")
			if err := app.Save(c); err != nil {
				return err
			}
		}
		return nil
	}, func(app core.App) error {
		for i := len(TeslaCollections) - 1; i >= 0; i-- {
			c, err := app.FindCollectionByNameOrId(TeslaCollections[i])
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
