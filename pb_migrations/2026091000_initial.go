package pb_migrations

import (
	"github.com/pocketbase/pocketbase/core"
	m "github.com/pocketbase/pocketbase/migrations"
	"github.com/pocketbase/pocketbase/tools/types"
)

func init() {
	m.Register(func(app core.App) error {
		users, err := app.FindCollectionByNameOrId("users")
		if err != nil {
			return err
		}
		users.Fields.Add(&core.TextField{Name: "username", Required: true, Min: 1, Max: 64, Pattern: `^[a-zA-Z0-9_.-]+$`})
		users.AddIndex("idx_users_username", true, "username", "")
		users.ListRule = nil
		users.DeleteRule = nil
		users.ManageRule = nil
		users.CreateRule = types.Pointer("") // first-run gate is enforced by the Go request hook
		users.ViewRule = types.Pointer(`id = @request.auth.id`)
		users.UpdateRule = types.Pointer(`id = @request.auth.id`)
		users.PasswordAuth = core.PasswordAuthConfig{Enabled: true, IdentityFields: []string{"username"}}
		users.Fields.GetByName("password").(*core.PasswordField).Min = 12
		if err := app.Save(users); err != nil {
			return err
		}
		for _, name := range []string{"devices", "app_settings", "automation_rules", "kasa_devices", "smart_charge_config", "solar_charge_config", "location", "cost_plan", "events", "algorithm_suggestions", "algorithm_changes"} {
			c := core.NewBaseCollection(name)
			c.Fields.Add(&core.TextField{Name: "key", Required: true}, &core.JSONField{Name: "data", MaxSize: 2 << 20})
			c.AddIndex("idx_"+name+"_key", true, "key", "")
			// Collection REST remains superuser-only; feature routes own validation.
			if err := app.Save(c); err != nil {
				return err
			}
		}
		return nil
	}, nil)
}
