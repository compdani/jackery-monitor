package migrations

import (
	"github.com/pocketbase/pocketbase/core"
	m "github.com/pocketbase/pocketbase/migrations"
)

func init() {
	numbers := []string{"input_w", "ac_input_w", "system_soc", "solar_coverage_s"}
	flags := []string{"has_input_w", "has_ac_input_w", "has_solar_w", "has_output_w", "has_system_soc"}
	m.Register(func(app core.App) error {
		c, err := app.FindCollectionByNameOrId("energy_minutes")
		if err != nil {
			return err
		}
		for _, name := range numbers {
			c.Fields.Add(&core.NumberField{Name: name})
		}
		for _, name := range flags {
			c.Fields.Add(&core.BoolField{Name: name})
		}
		return app.Save(c)
	}, func(app core.App) error {
		c, err := app.FindCollectionByNameOrId("energy_minutes")
		if err != nil {
			return err
		}
		for _, name := range append(numbers, flags...) {
			c.Fields.RemoveByName(name)
		}
		return app.Save(c)
	})
}
