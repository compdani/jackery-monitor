// Package jackerymonitor embeds the shared Python/Go model capacity catalog.
package jackerymonitor

import (
	_ "embed"
	"encoding/json"
	"strconv"
)

//go:embed models.json
var modelJSON []byte
var catalog struct {
	Default int `json:"default_capacity_wh"`
	Models  map[string]struct {
		Capacity int `json:"capacity_wh"`
		Pack     int `json:"pack_capacity_wh"`
	} `json:"models"`
}

func init() {
	if err := json.Unmarshal(modelJSON, &catalog); err != nil {
		panic(err)
	}
}
func Capacity(code int) (main, pack int, recognized bool) {
	m, ok := catalog.Models[strconv.Itoa(code)]
	if !ok {
		return catalog.Default, catalog.Default, false
	}
	p := m.Pack
	if p == 0 {
		p = m.Capacity
	}
	return m.Capacity, p, true
}
