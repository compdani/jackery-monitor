package server

import (
	"context"
	_ "embed"
	"github.com/compdani/jackery-monitor/internal/automation"
)

//go:embed contracts/device_params.json
var deviceParamMetadata []byte

func (s *Server) smartNarration(ctx context.Context, cfg automation.SmartSettings, p *automation.ChargePlan) string {
	if !cfg.Narrate || p == nil {
		return ""
	}
	return s.narrate(ctx, p)
}

func (s *Server) smartResolvedParams(ctx context.Context, sn string) []map[string]any {
	rows, err := s.resolveDeviceParams(ctx, sn, 14*24)
	if err != nil {
		return []map[string]any{}
	}
	return rows
}
