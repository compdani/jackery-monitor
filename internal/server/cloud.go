package server

import (
	"database/sql"
	"errors"
	"github.com/compdani/jackery-monitor/internal/bridge"
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
func (s *Server) UseNativeCloud() { s.Bridge = &bridge.Native{Accounts: cloudAccounts{s}} }
