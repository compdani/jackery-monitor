package weatherstore

import (
	"github.com/compdani/jackery-monitor/internal/forecast"
	"github.com/compdani/jackery-monitor/internal/store"
	_ "github.com/compdani/jackery-monitor/migrations"
	"github.com/pocketbase/pocketbase"
	"testing"
)

func TestPersistenceAndObservationSeparation(t *testing.T) {
	app := pocketbase.NewWithConfig(pocketbase.Config{DefaultDataDir: t.TempDir()})
	if err := app.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	defer app.ResetBootstrapState()
	if err := app.RunAllMigrations(); err != nil {
		t.Fatal(err)
	}
	p := PocketWeatherStore{Store: &store.Store{App: app}}
	w := forecast.Weather{FetchedAt: 100, Hourly: []forecast.Row{{"ts": 50.0, "ghi_w_m2": 100}, {"ts": 150.0, "ghi_w_m2": 500}}}
	if err := p.SaveWeather("site", w); err != nil {
		t.Fatal(err)
	}
	p = PocketWeatherStore{Store: &store.Store{App: app}}
	got, err := p.LoadWeather("site")
	if err != nil || len(got.Hourly) != 2 || got.FetchedAt != 100 {
		t.Fatal(got, err)
	}
	obs, err := p.Observations("site", 0)
	if err != nil || len(obs) != 1 || forecast.Number(obs[0], "ts") != 50 {
		t.Fatal(obs, err)
	}
	obs, err = p.Observations("other", 0)
	if err != nil || len(obs) != 0 {
		t.Fatal(obs, err)
	}
}

func TestSolarKeyEncryption(t *testing.T) {
	app := pocketbase.NewWithConfig(pocketbase.Config{DefaultDataDir: t.TempDir()})
	if err := app.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	defer app.ResetBootstrapState()
	if err := app.RunAllMigrations(); err != nil {
		t.Fatal(err)
	}
	p := PocketWeatherStore{Store: &store.Store{App: app}}
	key := make([]byte, 32)
	if err := p.SaveSolarKey(key, " secret "); err != nil {
		t.Fatal(err)
	}
	got, err := p.SolarKey(key)
	if err != nil || got != "secret" {
		t.Fatal(got, err)
	}
	raw, err := p.Store.Get("credentials", "forecast_solar")
	if err != nil || raw["api_key"] != nil || raw["ct"] == nil {
		t.Fatal(raw, err)
	}
	wrong := make([]byte, 32)
	wrong[0] = 1
	if _, err = p.SolarKey(wrong); err == nil {
		t.Fatal("accepted wrong key")
	}
	if err = p.ClearSolarKey(); err != nil {
		t.Fatal(err)
	}
	if err = p.ClearSolarKey(); err != nil {
		t.Fatal(err)
	}
	got, err = p.SolarKey(key)
	if err != nil || got != "" {
		t.Fatal(got, err)
	}
}
