// Per-operation attribute value generators for wide span emission
// Supports static, weighted random, and incrementing sequence values
package synth

import (
	"fmt"
	"math/rand/v2"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
)

// AttributeValueConfig defines how an attribute value is generated from YAML.
type AttributeValueConfig struct {
	Value    string         `yaml:"value,omitempty"`
	Values   map[string]int `yaml:"values,omitempty"`
	Sequence string         `yaml:"sequence,omitempty"`
}

// AttributeGenerator produces string values for a span attribute.
type AttributeGenerator interface {
	Generate(rng *rand.Rand) string
}

// StaticValue always returns the same string.
type StaticValue struct {
	Value string
}

func (s *StaticValue) Generate(_ *rand.Rand) string {
	return s.Value
}

// WeightedChoice picks from a set of values according to relative weights.
type WeightedChoice struct {
	Choices      []string
	CumulWeights []int
	TotalWeight  int
}

func (w *WeightedChoice) Generate(rng *rand.Rand) string {
	r := rng.IntN(w.TotalWeight)
	for i, cw := range w.CumulWeights {
		if r < cw {
			return w.Choices[i]
		}
	}
	return w.Choices[len(w.Choices)-1]
}

// SequenceValue produces incrementing values by replacing {n} in a pattern.
type SequenceValue struct {
	Pattern string
	counter atomic.Int64
}

func (s *SequenceValue) Generate(_ *rand.Rand) string {
	n := s.counter.Add(1)
	return strings.ReplaceAll(s.Pattern, "{n}", strconv.FormatInt(n, 10))
}

// NewAttributeGenerator creates an AttributeGenerator from a config entry.
// Exactly one of Value, Values, or Sequence must be set.
func NewAttributeGenerator(cfg AttributeValueConfig) (AttributeGenerator, error) {
	set := 0
	if cfg.Value != "" {
		set++
	}
	if len(cfg.Values) > 0 {
		set++
	}
	if cfg.Sequence != "" {
		set++
	}
	if set != 1 {
		return nil, fmt.Errorf("exactly one of value, values, or sequence must be set")
	}

	if cfg.Value != "" {
		return &StaticValue{Value: cfg.Value}, nil
	}

	if cfg.Sequence != "" {
		return &SequenceValue{Pattern: cfg.Sequence}, nil
	}

	return newWeightedChoice(cfg.Values)
}

func newWeightedChoice(values map[string]int) (*WeightedChoice, error) {
	if len(values) == 0 {
		return nil, fmt.Errorf("values must have at least one entry")
	}

	// Sort keys for deterministic ordering
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	slices.Sort(keys)

	choices := make([]string, 0, len(keys))
	cumul := make([]int, 0, len(keys))
	total := 0

	for _, k := range keys {
		w := values[k]
		if w <= 0 {
			return nil, fmt.Errorf("weight for %q must be positive, got %d", k, w)
		}
		total += w
		choices = append(choices, k)
		cumul = append(cumul, total)
	}

	return &WeightedChoice{
		Choices:      choices,
		CumulWeights: cumul,
		TotalWeight:  total,
	}, nil
}
