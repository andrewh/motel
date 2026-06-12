// Per-operation attribute value generators for wide span emission
// Supports static, weighted, sequence, boolean, range, and normal distribution values
package synth

import (
	"fmt"
	"maps"
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
	Values       map[any]int         `yaml:"values,omitempty"`
	Sequence     string              `yaml:"sequence,omitempty"`
	Probability  *float64            `yaml:"probability,omitempty"`
	Range        []int64             `yaml:"range,omitempty"`
	Distribution *DistributionConfig `yaml:"distribution,omitempty"`
}

// Attribute pairs a key with its value generator.
type Attribute struct {
	Key string
	Gen AttributeGenerator
}

// Attributes is an ordered attribute collection, sorted by key.
// Iteration order is deterministic by construction, which keeps RNG
// consumption order — and therefore seeded runs — reproducible without
// per-call-site sorting. Construct with NewAttributes.
type Attributes []Attribute

// NewAttributes converts a generator map into a key-sorted Attributes.
func NewAttributes(m map[string]AttributeGenerator) Attributes {
	if len(m) == 0 {
		return nil
	}
	attrs := make(Attributes, 0, len(m))
	for _, k := range slices.Sorted(maps.Keys(m)) {
		attrs = append(attrs, Attribute{Key: k, Gen: m[k]})
	}
	return attrs
}

// Merge returns the union of a and over, with over taking precedence on
// key collisions. Both inputs must be key-sorted; the result is too.
func (a Attributes) Merge(over Attributes) Attributes {
	if len(over) == 0 {
		return a
	}
	if len(a) == 0 {
		return over
	}
	merged := make(Attributes, 0, len(a)+len(over))
	i, j := 0, 0
	for i < len(a) && j < len(over) {
		switch {
		case a[i].Key < over[j].Key:
			merged = append(merged, a[i])
			i++
		case a[i].Key > over[j].Key:
			merged = append(merged, over[j])
			j++
		default:
			merged = append(merged, over[j])
			i++
			j++
		}
	}
	merged = append(merged, a[i:]...)
	merged = append(merged, over[j:]...)
	return merged
}

// Get returns the generator for key, or nil if the key is absent.
func (a Attributes) Get(key string) AttributeGenerator {
	for _, attr := range a {
		if attr.Key == key {
			return attr.Gen
		}
	}
	return nil
}

// AttributeGenerator produces typed values for a span attribute.
type AttributeGenerator interface {
	Generate(rng *rand.Rand) any
}

// StaticValue always returns the same value.
type StaticValue struct {
	Value any
}

// Generate returns the static value unchanged.
func (s *StaticValue) Generate(_ *rand.Rand) any {
	return s.Value
}

// WeightedChoice picks from a set of values according to relative weights.
type WeightedChoice struct {
	Choices      []any
	CumulWeights []int
	TotalWeight  int
}

// Generate picks a value at random according to the cumulative weights.
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

// Generate returns the next value in the sequence, replacing {n} with an incrementing counter.
func (s *SequenceValue) Generate(_ *rand.Rand) any {
	n := s.counter.Add(1)
	return strings.ReplaceAll(s.Pattern, "{n}", strconv.FormatInt(n, 10))
}

// BoolValue generates a boolean based on a probability threshold.
type BoolValue struct {
	Probability float64
}

// Generate returns true with the configured probability.
func (b *BoolValue) Generate(rng *rand.Rand) any {
	return rng.Float64() < b.Probability
}

// RangeValue generates a random int64 uniformly within [Min, Max].
type RangeValue struct {
	Min int64
	Max int64
}

// Generate returns a uniformly random int64 within [Min, Max].
func (r *RangeValue) Generate(rng *rand.Rand) any {
	span := uint64(r.Max) - uint64(r.Min) + 1       //nolint:gosec // deliberate uint64 cast for overflow-safe range arithmetic
	return int64(uint64(r.Min) + rng.Uint64N(span)) //nolint:gosec // result is within [Min, Max] by construction
}

// NormalValue generates a normally distributed float64.
type NormalValue struct {
	Mean   float64
	StdDev float64
}

// Generate returns a normally distributed float64 with the configured mean and standard deviation.
func (n *NormalValue) Generate(rng *rand.Rand) any {
	return n.Mean + rng.NormFloat64()*n.StdDev
}

// IsStaticAttributeConfig reports whether cfg produces a deterministic value
// that is the same on every Generate call (i.e. only the value: field is set).
// Used to validate that span-derived updowncounter attributes are consistent
// across start and end observations.
func IsStaticAttributeConfig(cfg AttributeValueConfig) bool {
	return cfg.Value != nil &&
		len(cfg.Values) == 0 &&
		cfg.Sequence == "" &&
		cfg.Probability == nil &&
		len(cfg.Range) == 0 &&
		cfg.Distribution == nil
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

func newWeightedChoice(values map[any]int) (*WeightedChoice, error) {
	if len(values) == 0 {
		return nil, fmt.Errorf("values must have at least one entry")
	}

	// Sort keys for deterministic ordering
	type entry struct {
		key    any
		weight int
	}
	entries := make([]entry, 0, len(values))
	for k, w := range values {
		entries = append(entries, entry{k, w})
	}
	slices.SortFunc(entries, func(a, b entry) int {
		return strings.Compare(fmt.Sprint(a.key), fmt.Sprint(b.key))
	})

	choices := make([]any, 0, len(entries))
	cumul := make([]int, 0, len(entries))
	total := 0

	for _, e := range entries {
		if e.weight <= 0 {
			return nil, fmt.Errorf("weight for %v must be positive, got %d", e.key, e.weight)
		}
		total += e.weight
		choices = append(choices, e.key)
		cumul = append(cumul, total)
	}

	return &WeightedChoice{
		Choices:      choices,
		CumulWeights: cumul,
		TotalWeight:  total,
	}, nil
}
