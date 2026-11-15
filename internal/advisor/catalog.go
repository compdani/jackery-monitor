package advisor

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"math"

	"github.com/compdani/jackery-monitor/internal/assistant"
)

//go:embed contracts.json
var contractsJSON []byte

//go:embed tunables.json
var tunablesJSON []byte

// Contracts are the prompt and tool schemas exported from claude_advisor.py.
type Contracts struct {
	System         string           `json:"system"`
	ReviewTool     assistant.Tool   `json:"review_tool"`
	QueryTools     []assistant.Tool `json:"query_tools"`
	NarratorSystem string           `json:"narrator_system"`
	MaxTurns       int              `json:"max_turns"`
	MaxTokens      int              `json:"max_tokens"`
	DefaultModel   string           `json:"default_model"`
}

// Target is one whitelisted tunable (tunables.json).
type Target struct {
	Scope   string  `json:"scope"`
	Min     float64 `json:"min"`
	Max     float64 `json:"max"`
	Comment string  `json:"comment"`
}

var contracts = func() Contracts {
	var c Contracts
	if err := json.Unmarshal(contractsJSON, &c); err != nil {
		panic(err)
	}
	return c
}()

var targets = func() map[string]Target {
	var doc struct {
		Targets map[string]Target `json:"targets"`
	}
	if err := json.Unmarshal(tunablesJSON, &doc); err != nil {
		panic(err)
	}
	return doc.Targets
}()

func Prompts() Contracts { return contracts }

// AllowedTarget returns the whitelist rule for target.
func AllowedTarget(target string) (Target, bool) { t, ok := targets[target]; return t, ok }

// ValidateProposal ports _validate_suggestion.
func ValidateProposal(target string, value any) error {
	t, ok := targets[target]
	if !ok {
		return fmt.Errorf("target %q not whitelisted", target)
	}
	v, ok := value.(float64)
	if !ok || math.IsNaN(v) || math.IsInf(v, 0) {
		return fmt.Errorf("proposed_value not numeric")
	}
	if v < t.Min || v > t.Max {
		return fmt.Errorf("proposed %g out of range [%g, %g]", v, t.Min, t.Max)
	}
	return nil
}
