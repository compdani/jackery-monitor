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
				device := s.deviceAlias(d)
				if r.URL.Path == "/api/energy/devices" {
					if totals := s.statusEnergy(str(d, "device_sn")); totals != nil {
						for k, v := range totals {
							if k != "device_sn" {
								device[k] = v
							}
						}
					}
				}
				devices = append(devices, device)
			}
		}
		var selected any
		selected = s.selectedSerial(r)
		jsonOut(w, 200, map[string]any{"devices": devices, "selected_device_id": selected})
		return true
	case "/api/energy/history", "/api/energy/totals", "/api/energy/daily":
		return s.energyRead(w, r)
	}
	return false
}
