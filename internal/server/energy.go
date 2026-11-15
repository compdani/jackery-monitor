package server

import (
	"net/http"
)

func (s *Server) energy(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != "GET" {
		return false
	}
	switch r.URL.Path {
	case "/api/devices", "/api/energy/devices":
		rows, e := s.Store.List("devices", "", 0, 1000)
		if e != nil {
			storageError(w, e)
			return true
		}
		devices := []any{}
		for _, row := range rows {
			if d, ok := row["device"].(map[string]any); ok {
				devices = append(devices, s.deviceAlias(d))
			}
		}
		var selected any
		if cloud, ok := s.status(r)["cloud"].(map[string]any); ok {
			selected = cloud["selected_device_id"]
		}
		jsonOut(w, 200, map[string]any{"devices": devices, "selected_device_id": selected})
		return true
	case "/api/energy/history", "/api/energy/totals", "/api/energy/daily":
		return s.energyRead(w, r)
	}
	return false
}
