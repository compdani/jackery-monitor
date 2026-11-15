package automation

const (
	RescueArmSOC       = 5.0
	RescueStaleSeconds = 60.0
	RescueClearSOC     = 20.0
)

// RescueState tracks MAIN-unit SOC, never capacity-weighted system SOC.
// Decide does not latch: only a successful plug operation may call MarkFired.
type RescueState struct {
	LastFreshTS    float64  `json:"last_fresh_ts"`
	LastFreshMain  *float64 `json:"last_fresh_main"`
	Fired          bool     `json:"fired"`
	LastFireReason *string  `json:"last_fire_reason"`
	LastFireTS     float64  `json:"last_fire_ts"`
}

func (s *RescueState) Decide(now float64, telemetryTS, mainSOC *float64) string {
	if telemetryTS == nil {
		return ""
	}
	if *telemetryTS > s.LastFreshTS && mainSOC != nil {
		s.LastFreshTS = *telemetryTS
		v := *mainSOC
		s.LastFreshMain = &v
	}
	silent := now - s.LastFreshTS
	if silent < RescueStaleSeconds {
		if s.Fired && s.LastFreshMain != nil && *s.LastFreshMain >= RescueClearSOC {
			s.Fired = false
		}
		if !s.Fired && s.LastFreshMain != nil && *s.LastFreshMain <= RescueArmSOC {
			return "live"
		}
		return ""
	}
	if !s.Fired && s.LastFreshMain != nil && *s.LastFreshMain <= RescueArmSOC && silent >= RescueStaleSeconds {
		return "deadman"
	}
	return ""
}
func (s *RescueState) MarkFired(reason string, now float64) {
	s.Fired = true
	s.LastFireReason = &reason
	s.LastFireTS = now
}
