package pb_migrations

import (
	"github.com/pocketbase/pocketbase/core"
	m "github.com/pocketbase/pocketbase/migrations"
)

func init() {
	m.Register(func(app core.App) error {
		_, err := app.DB().NewQuery(`CREATE TABLE IF NOT EXISTS weather_cache_meta(id INTEGER PRIMARY KEY CHECK(id=1),fetched_at INTEGER NOT NULL); CREATE TABLE IF NOT EXISTS forecast_model_runs(device_sn TEXT NOT NULL,made_at INTEGER NOT NULL,version TEXT NOT NULL,PRIMARY KEY(device_sn,made_at));`).Execute()
		return err
	}, nil)
}
