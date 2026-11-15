package server

import (
	"context"
	"fmt"
	"github.com/compdani/jackery-monitor/internal/automation"
	"strings"
	"sync"
	"time"
)

type actionArbiter struct {
	plugs sync.Mutex
	mu    sync.Mutex
	ac    map[string]context.CancelFunc
}
type governedSwitch struct {
	s     *Server
	owner string
	next  automation.Switch
}

func (g governedSwitch) SetPower(ctx context.Context, host string, on bool) error {
	g.s.actions.plugs.Lock()
	defer g.s.actions.plugs.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if g.owner != "rescue" {
		raw, err := g.s.Store.Get("settings", "rescue_state")
		if err != nil && !isMissing(err) {
			return err
		}
		by, _ := raw["by_device"].(map[string]any)
		for _, v := range by {
			st, _ := v.(map[string]any)
			if st["fired"] == true && sameHost(str(st, "kasa_host"), host) {
				return fmt.Errorf("plug reserved by rescue")
			}
		}
		if g.owner == "solar" && g.s.smartOwnsHost(host) {
			return fmt.Errorf("plug reserved by smart-charge")
		}
		if g.owner == "rule" && (g.s.smartOwnsHost(host) || g.s.solarOwnsHost(host)) {
			return fmt.Errorf("plug reserved by charge controller")
		}
	}
	return g.next.SetPower(ctx, host, on)
}
func sameHost(a, b string) bool { return strings.EqualFold(strings.TrimSpace(a), strings.TrimSpace(b)) }

// A recovery holds its AC lease across the entire OFF/wait/ON or ON/wait/OFF
// sequence. User OFF cancels the lease immediately, including queued commands.
func (s *Server) acquireAC(ctx context.Context, sn string) (context.Context, func(), bool) {
	s.actions.mu.Lock()
	defer s.actions.mu.Unlock()
	if s.actions.ac[sn] != nil {
		return nil, nil, false
	}
	st, err := s.watchdogState(sn)
	if err != nil || float64(time.Now().Unix())-st.LastUserOffTS < 60 {
		return nil, nil, false
	}
	c, cancel := context.WithCancel(ctx)
	if s.actions.ac == nil {
		s.actions.ac = map[string]context.CancelFunc{}
	}
	s.actions.ac[sn] = cancel
	return c, func() { s.actions.mu.Lock(); defer s.actions.mu.Unlock(); cancel(); delete(s.actions.ac, sn) }, true
}
func (s *Server) cancelAC(sn string) {
	s.actions.mu.Lock()
	defer s.actions.mu.Unlock()
	if cancel := s.actions.ac[sn]; cancel != nil {
		cancel()
	}
}
