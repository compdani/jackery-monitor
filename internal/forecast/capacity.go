package forecast

import (
	"encoding/json"
	jackery "github.com/compdani/jackery-monitor"
	"strconv"
)

type catalogEntry struct {
	Capacity     float64 `json:"capacity_wh"`
	PackCapacity float64 `json:"pack_capacity_wh"`
}
type capacityCatalog struct {
	Default float64                 `json:"default_capacity_wh"`
	Models  map[string]catalogEntry `json:"models"`
}

var modelCatalog = func() capacityCatalog {
	var c capacityCatalog
	if e := json.Unmarshal(jackery.ModelCatalog, &c); e != nil {
		panic(e)
	}
	return c
}()

func BatteryCapacity(model int) float64 {
	if m, ok := modelCatalog.Models[strconv.Itoa(model)]; ok {
		return m.Capacity
	}
	return modelCatalog.Default
}
func ExpansionCapacity(model int) float64 {
	if m, ok := modelCatalog.Models[strconv.Itoa(model)]; ok && m.PackCapacity > 0 {
		return m.PackCapacity
	}
	return BatteryCapacity(model)
}

// InCatalog reports whether model_code has a bundled models.json entry.
func InCatalog(model int) bool { _, ok := modelCatalog.Models[strconv.Itoa(model)]; return ok }
