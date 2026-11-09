package automation

// WatchdogState belongs to one serialized device controller. PendingOff tracks
// requests until acknowledgement; callers own tokens so cancellations cannot
// erase another request's intent.
type WatchdogState struct {
	ConsecutiveAttempts int             `json:"consecutive_attempts"`
	LastAttemptTS       float64         `json:"last_attempt_ts"`
	LastUserOffTS       float64         `json:"last_user_off_ts"`
	ErrorMessage        *string         `json:"error_message"`
	LastHighOutputTS    float64         `json:"last_high_output_ts"`
	FirstLowTS          float64         `json:"first_low_ts"`
	LowSamples          int             `json:"low_samples"`
	LastSampleTS        float64         `json:"last_sample_ts"`
	HWTripActive        bool            `json:"hw_trip_active"`
	EpisodeCycles       int             `json:"episode_cycles"`
	PendingOff          map[string]bool `json:"-"`
}

func (s *WatchdogState) Dismiss() {
	s.ConsecutiveAttempts = 0
	s.LastAttemptTS = 0
	s.ErrorMessage = nil
	s.HWTripActive = false
	s.EpisodeCycles = 0
	s.FirstLowTS = 0
	s.LowSamples = 0
}
func (s *WatchdogState) Evaluate(ac bool, output, sample *float64, floor, now, retry, grace float64) string {
	userGrace := len(s.PendingOff) > 0 || (s.LastUserOffTS != 0 && now-s.LastUserOffTS < grace)
	if userGrace {
		s.FirstLowTS = 0
		s.LowSamples = 0
	} else if floor > 0 && output != nil && (sample == nil || *sample != s.LastSampleTS) {
		if sample != nil {
			s.LastSampleTS = *sample
		}
		if !ac {
			s.FirstLowTS = 0
			s.LowSamples = 0
		} else {
			if *output >= 300 {
				s.LastHighOutputTS = now
				s.FirstLowTS = 0
				s.LowSamples = 0
				s.HWTripActive = false
				s.EpisodeCycles = 0
			} else if *output <= floor {
				if s.FirstLowTS == 0 {
					s.FirstLowTS = now
				}
				s.LowSamples++
			} else {
				s.FirstLowTS = 0
				s.LowSamples = 0
				s.HWTripActive = false
				s.EpisodeCycles = 0
			}
			if !s.HWTripActive && s.LowSamples >= 3 && s.FirstLowTS > 0 && now-s.FirstLowTS >= 6 && s.LastHighOutputTS > 0 && s.FirstLowTS-s.LastHighOutputTS <= 15 {
				s.HWTripActive = true
				s.EpisodeCycles = 0
			}
		}
	}
	if !ac {
		s.ConsecutiveAttempts = 0
		s.ErrorMessage = nil
		s.HWTripActive = false
		s.EpisodeCycles = 0
		s.FirstLowTS = 0
		s.LowSamples = 0
		return "idle"
	}
	if !s.HWTripActive {
		s.ConsecutiveAttempts = 0
		s.ErrorMessage = nil
		return "idle"
	}
	if userGrace {
		return "user_grace"
	}
	if s.EpisodeCycles >= 2 {
		if s.ErrorMessage == nil {
			msg := "Output collapsed while the AC port still reports ON — hardware trip suspected. Cycled AC off/on 2x without output returning; stopped to avoid repeated power cuts. Check the unit. Recovery resumes automatically when output returns (or the port reports OFF)."
			s.ErrorMessage = &msg
		}
		return "error"
	}
	if now-s.LastAttemptTS >= retry {
		s.ConsecutiveAttempts++
		s.LastAttemptTS = now
		s.EpisodeCycles++
		return "cycle"
	}
	return "waiting"
}
