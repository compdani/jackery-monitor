package server

import (
	"context"
	"database/sql"
	"errors"
	"github.com/compdani/jackery-monitor/internal/bridge"
	"os"
	"time"
)

type cloudAccounts struct{ s *Server }

func (a cloudAccounts) Load() (map[string]any, error) {
	m, e := a.s.Store.Secret(a.s.Key, "jackery")
	if errors.Is(e, sql.ErrNoRows) {
		return nil, nil
	}
	return m, e
}
func (a cloudAccounts) Save(m map[string]any) error {
	return a.s.Store.SaveSecret(a.s.Key, "jackery", m)
}
func (a cloudAccounts) Clear() error {
	e := a.s.Store.Delete("credentials", "jackery")
	if errors.Is(e, sql.ErrNoRows) {
		return nil
	}
	return e
}
func (s *Server) UseNativeCloud() {
	s.Bridge = &bridge.Native{Accounts: cloudAccounts{s}, Configure: func(c *bridge.Cloud) error {
		config, err := bridge.TLSFromCA(os.Getenv("JACKERY_MQTT_CA_FILE"))
		if err != nil {
			return err
		}
		c.TLSConfig = config
		c.Broker = os.Getenv("JACKERY_MQTT_BROKER")
		return nil
	}, PollInterval: func() time.Duration {
		v, _, err := s.settingValues()
		if err != nil {
			return time.Minute
		}
		return time.Duration(v["cloud_poll_interval_s"].(int)) * time.Second
	}, Cooldown: func() time.Duration {
		v, _, err := s.settingValues()
		if err != nil {
			return time.Minute
		}
		return time.Duration(v["session_contested_cooldown_s"].(int)) * time.Second
	}}
}

func (s *Server) RunNativeRealtime(ctx context.Context) {
	if native, ok := s.Bridge.(*bridge.Native); ok {
		native.Realtime(ctx, func(p map[string]any) {
			s.acceptSnapshot(ctx, p)
			s.rescueTick(ctx, float64(time.Now().UnixNano())/1e9, p)
		})
	}
}
