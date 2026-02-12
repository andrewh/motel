// Per-operation attribute value generators for wide span emission
// Supports static, weighted, sequence, boolean, range, and normal distribution values
package synth

import (
	"fmt"
	"math/rand/v2"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
)

// DistributionConfig defines parameters for a normal distribution generator.
type DistributionConfig struct {
	Mean   float64 `yaml:"mean"`
	StdDev float64 `yaml:"stddev"`
}

// AttributeValueConfig defines how an attribute value is generated from YAML.
type AttributeValueConfig struct {
	Value        any                 `yaml:"value,omitempty"`
	Values       map[string]int      `yaml:"values,omitempty"`
	Sequence     string              `yaml:"sequence,omitempty"`
	Probability  *float64            `yaml:"probability,omitempty"`
	Range        []int64             `yaml:"range,omitempty"`
	Distribution *DistributionConfig `yaml:"distribution,omitempty"`
}

// AttributeGenerator produces typed values for a span attribute.
type AttributeGenerator interface {
	Generate(rng *rand.Rand) any
}

// StaticValue always returns the same value.
type StaticValue struct {
	Value any
}

func (s *StaticValue) Generate(_ *rand.Rand) any {
	return s.Value
}

// WeightedChoice picks from a set of values according to relative weights.
type WeightedChoice struct {
	Choices      []any
	CumulWeights []int
	TotalWeight  int
}

func (w *WeightedChoice) Generate(rng *rand.Rand) any {
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

func (s *SequenceValue) Generate(_ *rand.Rand) any {
	n := s.counter.Add(1)
	return strings.ReplaceAll(s.Pattern, "{n}", strconv.FormatInt(n, 10))
}

// BoolValue generates a boolean based on a probability threshold.
type BoolValue struct {
	Probability float64
}

func (b *BoolValue) Generate(rng *rand.Rand) any {
	return rng.Float64() < b.Probability
}

// RangeValue generates a random int64 uniformly within [Min, Max].
type RangeValue struct {
	Min int64
	Max int64
}

func (r *RangeValue) Generate(rng *rand.Rand) any {
	span := uint64(r.Max) - uint64(r.Min) + 1       //nolint:gosec // deliberate uint64 cast for overflow-safe range arithmetic
	return int64(uint64(r.Min) + rng.Uint64N(span)) //nolint:gosec // result is within [Min, Max] by construction
}

// NormalValue generates a normally distributed float64.
type NormalValue struct {
	Mean   float64
	StdDev float64
}

func (n *NormalValue) Generate(rng *rand.Rand) any {
	return n.Mean + rng.NormFloat64()*n.StdDev
}

// NewAttributeGenerator creates an AttributeGenerator from a config entry.
// Exactly one of the config fields must be set.
func NewAttributeGenerator(cfg AttributeValueConfig) (AttributeGenerator, error) {
	set := 0
	if cfg.Value != nil {
		set++
	}
	if len(cfg.Values) > 0 {
		set++
	}
	if cfg.Sequence != "" {
		set++
	}
	if cfg.Probability != nil {
		set++
	}
	if len(cfg.Range) > 0 {
		set++
	}
	if cfg.Distribution != nil {
		set++
	}
	if set != 1 {
		return nil, fmt.Errorf("exactly one of value, values, sequence, probability, range, or distribution must be set")
	}

	if cfg.Value != nil {
		return &StaticValue{Value: cfg.Value}, nil
	}

	if cfg.Sequence != "" {
		return &SequenceValue{Pattern: cfg.Sequence}, nil
	}

	if cfg.Probability != nil {
		p := *cfg.Probability
		if p < 0.0 || p > 1.0 {
			return nil, fmt.Errorf("probability must be between 0.0 and 1.0, got %f", p)
		}
		return &BoolValue{Probability: p}, nil
	}

	if len(cfg.Range) > 0 {
		if len(cfg.Range) != 2 {
			return nil, fmt.Errorf("range must have exactly 2 elements [min, max], got %d", len(cfg.Range))
		}
		if cfg.Range[0] > cfg.Range[1] {
			return nil, fmt.Errorf("range min (%d) must not exceed max (%d)", cfg.Range[0], cfg.Range[1])
		}
		return &RangeValue{Min: cfg.Range[0], Max: cfg.Range[1]}, nil
	}

	if cfg.Distribution != nil {
		if cfg.Distribution.StdDev < 0 {
			return nil, fmt.Errorf("distribution stddev must not be negative, got %f", cfg.Distribution.StdDev)
		}
		return &NormalValue{Mean: cfg.Distribution.Mean, StdDev: cfg.Distribution.StdDev}, nil
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

	choices := make([]any, 0, len(keys))
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
