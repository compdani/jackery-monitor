// Package migrations defines the core monitor collections. All API rules are
// locked: the compatibility HTTP layer performs authorization.
package migrations

import (
	"github.com/pocketbase/pocketbase/core"
	m "github.com/pocketbase/pocketbase/migrations"
)

var Collections = []string{"settings", "device_prefs", "credentials", "devices", "samples", "predictions", "automation_rules", "automation_decisions", "forecast_models", "conversations", "messages", "attachments", "tool_runs", "suggestions", "changes", "events"}

func init() {
	m.Register(func(app core.App) error {
		users, err := app.FindCollectionByNameOrId("users")
		if err != nil {
			users = core.NewAuthCollection("users")
		}
		users.ListRule = nil
		users.ViewRule = nil
		users.CreateRule = nil
		users.UpdateRule = nil
		users.DeleteRule = nil
		users.Fields.GetByName("email").(*core.EmailField).Required = false
		users.Fields.GetByName("password").(*core.PasswordField).Min = 6
		users.Fields.Add(&core.TextField{Name: "username", Required: true})
		users.AddIndex("idx_users_username", true, "username", "")
		users.PasswordAuth.Enabled = true
		users.PasswordAuth.IdentityFields = []string{"username"}
		users.AuthToken.Duration = 30 * 24 * 3600
		if err := app.Save(users); err != nil {
			return err
		}
		for _, name := range Collections {
			c := core.NewBaseCollection(name)
			c.Fields.Add(&core.TextField{Name: "key", Required: true}, &core.TextField{Name: "device_sn"}, &core.NumberField{Name: "ts"}, &core.JSONField{Name: "payload", MaxSize: 10 << 20})
			c.AddIndex("idx_"+name+"_key", true, "key", "")
			c.AddIndex("idx_"+name+"_device_ts", false, "device_sn, ts", "")
			if err := app.Save(c); err != nil {
				return err
			}
		}
		return nil
	}, func(app core.App) error {
		for i := len(Collections) - 1; i >= 0; i-- {
			c, e := app.FindCollectionByNameOrId(Collections[i])
			if e != nil {
				return e
			}
			if e = app.Delete(c); e != nil {
				return e
			}
		}
		// Preserve the pre-existing PocketBase users collection on rollback.
		return nil
	})
}
