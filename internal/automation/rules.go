package automation

import (
	"context"
	"errors"
	"math"
	"sort"
	"sync"
)

type Switch interface {
	SetPower(context.Context, string, bool) error
}
type Rule struct {
	ID       string  `json:"id"`
	Name     string  `json:"name"`
	Enabled  bool    `json:"enabled"`
	Operator string  `json:"operator"`
	Value    float64 `json:"value"`
	Action   string  `json:"action"`
	Host     string  `json:"kasa_host"`
	DeviceSN string  `json:"jackery_device_sn"`
}

func (r Rule) Validate() error {
	if r.Host == "" || (r.Action != "on" && r.Action != "off") {
		return errors.New("kasa_host and on/off action required")
	}
	switch r.Operator {
	case "<", "<=", "=", ">=", ">":
	default:
		return errors.New("invalid operator")
	}
	if math.IsNaN(r.Value) || math.IsInf(r.Value, 0) {
		return errors.New("invalid threshold")
	}
	return nil
}
func (r Rule) Matches(soc float64) bool {
	switch r.Operator {
	case "<":
		return soc < r.Value
	case "<=":
		return soc <= r.Value
	case "=":
		return math.Abs(soc-r.Value) <= .5
	case ">=":
		return soc >= r.Value
	case ">":
		return soc > r.Value
	}
	return false
}

type Engine struct {
	mu     sync.Mutex
	states map[string]bool
	Switch Switch
}

func (e *Engine) Evaluate(ctx context.Context, r Rule, device string, soc float64) (bool, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !r.Enabled || (r.DeviceSN != "" && r.DeviceSN != device) {
		return false, nil
	}
	if err := r.Validate(); err != nil {
		return false, err
	}
	if e.states == nil {
		e.states = map[string]bool{}
	}
	match := r.Matches(soc)
	previous := e.states[r.ID]
	if !match {
		e.states[r.ID] = false
		return false, nil
	}
	if previous {
		return false, nil
	}
	if e.Switch == nil {
		return false, errors.New("switch adapter unavailable")
	}
	if err := e.Switch.SetPower(ctx, r.Host, r.Action == "on"); err != nil {
		return false, err
	}
	e.states[r.ID] = true
	return true, nil
}

type Candidate struct {
	TS   int64
	Rate float64
}

func PickPlannedHours(c []Candidate, needed int, mandatory *int64) []int64 {
	if needed <= 0 {
		return []int64{}
	}
	c = append([]Candidate(nil), c...)
	sort.SliceStable(c, func(i, j int) bool {
		if c[i].Rate == c[j].Rate {
			return c[i].TS > c[j].TS
		}
		return c[i].Rate < c[j].Rate
	})
	out := []int64{}
	if mandatory != nil {
		for _, v := range c {
			if v.TS == *mandatory {
				out = append(out, v.TS)
				break
			}
		}
	}
	for _, v := range c {
		if len(out) >= needed {
			break
		}
		if len(out) > 0 && mandatory != nil && v.TS == *mandatory {
			continue
		}
		out = append(out, v.TS)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
