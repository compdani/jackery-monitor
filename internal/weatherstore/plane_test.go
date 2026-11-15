package weatherstore

import (
	"github.com/compdani/jackery-monitor/internal/store"
	"github.com/pocketbase/pocketbase"
	"testing"
)

func TestPlaneDeviceAndLegacyInheritance(t *testing.T) {
	a := pocketbase.NewWithConfig(pocketbase.Config{DefaultDataDir: t.TempDir()})
	if e := a.Bootstrap(); e != nil {
		t.Fatal(e)
	}
	defer a.ResetBootstrapState()
	if e := a.RunAllMigrations(); e != nil {
		t.Fatal(e)
	}
	p := PocketWeatherStore{Store: &store.Store{App: a}}
	if _, ok, e := p.Plane("A"); e != nil || ok {
		t.Fatal(ok, e)
	}
	if _, e := p.SavePlane("A", 20, 0, 2.4); e != nil {
		t.Fatal(e)
	}
	if _, e := p.SavePlane("B", 30, -90, 1.2); e != nil {
		t.Fatal(e)
	}
	v, ok, e := p.Plane("A")
	if e != nil || !ok || v.KWP != 2.4 || v.Declination != 20 {
		t.Fatal(v, ok, e)
	}
	v, ok, e = p.Plane("B")
	if e != nil || !ok || v.Azimuth != -90 {
		t.Fatal(v, ok, e)
	}
	if _, ok, e = p.Plane("C"); e != nil || ok {
		t.Fatal(ok, e)
	}
	if e = p.Store.Put("settings", "solar_array", "", map[string]any{"declination": 20, "azimuth": 0, "kwp": 2.4}); e != nil {
		t.Fatal(e)
	}
	if _, e = p.SavePlane("SN1", 10, 90, 5); e != nil {
		t.Fatal(e)
	}
	v, ok, e = p.Plane("SN1")
	if e != nil || !ok || v.KWP != 5 {
		t.Fatal(v, ok, e)
	}
	v, ok, e = p.Plane("SN2")
	if e != nil || !ok || v.KWP != 2.4 || v.Declination != 20 {
		t.Fatal(v, ok, e)
	}
}
