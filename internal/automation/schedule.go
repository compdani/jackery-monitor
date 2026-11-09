package automation

import (
	"math"
	"strconv"
	"strings"
)

func ParseHHMM(s string) (int, bool) {
	v := strings.Split(strings.TrimSpace(s), ":")
	if len(v) != 2 {
		return 0, false
	}
	h, e := strconv.Atoi(v[0])
	if e != nil {
		return 0, false
	}
	m, e := strconv.Atoi(v[1])
	return h*60 + m, e == nil && h >= 0 && h < 24 && m >= 0 && m < 60
}
func InSleepWindow(m int, start, end string) bool {
	a, ok := ParseHHMM(start)
	b, ok2 := ParseHHMM(end)
	return ok && ok2 && HourInSlot((m%1440+1440)%1440, a, b)
}

type Window struct {
	Start    string  `json:"start"`
	End      string  `json:"end"`
	Kind     string  `json:"kind"`
	Date     string  `json:"date"`
	Weekdays []int   `json:"weekdays"`
	Days     string  `json:"days"`
	Watts    float64 `json:"watts"`
}

func (w Window) Covers(hour, weekday int, date string) bool {
	a, ok := ParseHHMM(w.Start)
	b, ok2 := ParseHHMM(w.End)
	if !ok || !ok2 || !HourInSlot(hour*60, a, b) {
		return false
	}
	switch w.Kind {
	case "once":
		return w.Date != "" && w.Date == date
	case "weekly":
		for _, d := range w.Weekdays {
			if d == weekday {
				return true
			}
		}
		return false
	}
	switch w.Days {
	case "weekday":
		return weekday < 5
	case "weekend":
		return weekday >= 5
	}
	return true
}
func ScheduledLoad(windows []Window, h, d int, date string) float64 {
	v := 0.0
	for _, w := range windows {
		if w.Covers(h, d, date) {
			v += math.Max(0, w.Watts)
		}
	}
	return v
}
