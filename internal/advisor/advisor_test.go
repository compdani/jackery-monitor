package advisor

import (
	"math"
	"testing"
)

func TestSuggestionBounds(t *testing.T) {
	bounds := map[string]Bound{"max_charge_w": {100, 2000}}
	for _, c := range []struct {
		s     Suggestion
		valid bool
	}{{Suggestion{Field: "max_charge_w", Value: 1500, Status: "pending"}, true}, {Suggestion{Field: "max_charge_w", Value: 2500, Status: "pending"}, false}, {Suggestion{Field: "password", Value: 1, Status: "pending"}, false}, {Suggestion{Field: "max_charge_w", Value: 1500, Status: "applied"}, false}, {Suggestion{Field: "max_charge_w", Value: math.NaN(), Status: "pending"}, false}} {
		if e := Validate(c.s, bounds); (e == nil) != c.valid {
			t.Fatal(c, e)
		}
	}
}
