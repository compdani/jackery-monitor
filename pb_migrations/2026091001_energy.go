package pb_migrations

import (
	"jackery-monitor/internal/energy"

	"github.com/pocketbase/pocketbase/core"
	m "github.com/pocketbase/pocketbase/migrations"
)

func init() {
	m.Register(func(app core.App) error { _, err := app.DB().NewQuery(energy.Schema).Execute(); return err }, nil)
}
