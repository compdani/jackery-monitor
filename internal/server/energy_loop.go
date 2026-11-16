package server

import (
	"context"
	"net/http"
	"net/url"
	"runtime"
	"time"
)

// EnergyLoop owns rollup/stat warming independently of live polling and HTTP
// clients. It starts after serving and follows the application's cancellation.
func (s *Server) EnergyLoop(ctx context.Context) {
	for {
		rows, err := s.Store.List("devices", "", 0, 0)
		if err != nil {
			s.Store.App.Logger().Error("energy devices", "error", err)
		}
		for _, row := range rows {
			if ctx.Err() != nil {
				return
			}
			d, _ := row["device"].(map[string]any)
			sn := str(d, "device_sn")
			if sn == "" {
				continue
			}
			path := "/api/energy/totals?" + url.Values{"device_sn": {sn}}.Encode()
			req, _ := http.NewRequestWithContext(ctx, "GET", path, nil)
			response := &energyResponse{header: make(http.Header)}
			for {
				s.energyRead(response, req)
				if response.status != 202 || ctx.Err() != nil {
					break
				}
				runtime.Gosched()
				response = &energyResponse{header: make(http.Header)}
			}
			if response.status != 200 {
				s.Store.App.Logger().Error("energy totals", "device", sn, "status", response.status)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(5 * time.Second):
		}
	}
}
