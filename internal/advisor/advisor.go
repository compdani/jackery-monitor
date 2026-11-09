// Package advisor enforces bounded, human-approved algorithm suggestions.
package advisor

import (
	"fmt"
	"math"
)

type Bound struct{ Min, Max float64 }
type Suggestion struct {
	ID, DeviceSN, Field string
	Value               float64
	Status              string
}

func Validate(s Suggestion, allow map[string]Bound) error {
	b, ok := allow[s.Field]
	if !ok {
		return fmt.Errorf("field %q is not tunable", s.Field)
	}
	if math.IsNaN(s.Value) || math.IsInf(s.Value, 0) || s.Value < b.Min || s.Value > b.Max {
		return fmt.Errorf("value outside allowed bounds")
	}
	if s.Status != "pending" {
		return fmt.Errorf("suggestion is not pending")
	}
	return nil
}
