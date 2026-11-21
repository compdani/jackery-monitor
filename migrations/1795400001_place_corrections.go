package migrations

import (
	"github.com/pocketbase/pocketbase/core"
	m "github.com/pocketbase/pocketbase/migrations"
)

// place_corrections remembers the address the driver picked for a Flex pin
// (Amazon's pin is often the parking spot, not the house). A later pin at
// the same spot uses the pick automatically; the app marks it as such.
func init() {
	m.Register(func(app core.App) error {
		c := core.NewBaseCollection("place_corrections")
		c.Fields.Add(
			&core.NumberField{Name: "pin_lat", Required: true},
			&core.NumberField{Name: "pin_lng", Required: true},
			&core.TextField{Name: "place_id", Required: true},
			&core.TextField{Name: "address"},
			&core.NumberField{Name: "lat"},
			&core.NumberField{Name: "lng"},
			&core.NumberField{Name: "uses"},
			&core.NumberField{Name: "last_used"},
			&core.AutodateField{Name: "created", OnCreate: true},
			&core.AutodateField{Name: "updated", OnCreate: true, OnUpdate: true},
		)
		c.AddIndex("idx_place_corrections_pin", false, "pin_lat, pin_lng", "")
		return app.Save(c)
	}, func(app core.App) error {
		c, err := app.FindCollectionByNameOrId("place_corrections")
		if err != nil {
			return nil
		}
		return app.Delete(c)
	})
}
