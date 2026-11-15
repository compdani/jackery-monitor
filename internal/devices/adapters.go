// Package devices connects the hardware libraries to the core bridge contracts.
package devices

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/compdani/jackery-monitor/internal/ble"
	"github.com/compdani/jackery-monitor/internal/ecoflow"
	"github.com/compdani/jackery-monitor/internal/kasa"
	"github.com/compdani/jackery-monitor/internal/siseli"
)

type KasaSwitch struct{ Client *kasa.Client }

func (k KasaSwitch) SetPower(ctx context.Context, host string, on bool) error {
	if k.Client == nil {
		return errors.New("Kasa unavailable")
	}
	_, e := k.Client.SetState(ctx, host, on)
	return e
}

type Siseli struct {
	Client *siseli.Client
	ID     string
	// BMS is an injected snapshot; BLE sampling remains owned by the poller.
	BMS func() ble.OverlayOpts
}

func (s Siseli) Poll(ctx context.Context) (map[string]any, error) {
	if s.Client == nil {
		return nil, errors.New("Siseli unavailable")
	}
	raw, e := s.Client.FetchLatestData(ctx, s.ID)
	if e != nil {
		return nil, e
	}
	tele := map[string]any(siseli.ToTelemetry(raw))
	if s.BMS != nil {
		tele = ble.OverlayTelemetry(tele, s.BMS())
	}
	return tele, nil
}
func (s Siseli) SetOutput(context.Context, string, bool) error {
	return errors.New("Siseli output toggle unsupported; use device settings")
}

type EcoFlow struct {
	Client *ecoflow.Client
	SN     string
}

func (e EcoFlow) Poll(ctx context.Context) (map[string]any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if e.Client == nil {
		return nil, errors.New("EcoFlow unavailable")
	}
	s, ok := e.Client.Snapshot()[e.SN]
	if !ok {
		return nil, errors.New("EcoFlow telemetry not received")
	}
	b, err := json.Marshal(s.Telemetry)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	err = json.Unmarshal(b, &m)
	return m, err
}
func (e EcoFlow) SetOutput(ctx context.Context, port string, on bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if e.Client == nil {
		return errors.New("EcoFlow unavailable")
	}
	return e.Client.SetOutput(e.SN, port, on)
}
