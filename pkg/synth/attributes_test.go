// Tests for per-operation attribute value generators
// Covers static, weighted, and sequence generator types
package synth

import (
	"math/rand/v2"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStaticValue(t *testing.T) {
	t.Parallel()

	gen := &StaticValue{Value: "/api/v1/users"}
	rng := rand.New(rand.NewPCG(42, 0)) //nolint:gosec // deterministic seed for testing

	// Always returns the same value
	for range 10 {
		assert.Equal(t, "/api/v1/users", gen.Generate(rng))
	}
}

func TestStaticValueTyped(t *testing.T) {
	t.Parallel()

	t.Run("int value", func(t *testing.T) {
		t.Parallel()
		gen := &StaticValue{Value: 5432}
		assert.Equal(t, 5432, gen.Generate(nil))
	})

	t.Run("bool value", func(t *testing.T) {
		t.Parallel()
		gen := &StaticValue{Value: true}
		assert.Equal(t, true, gen.Generate(nil))
	})
}

func TestWeightedChoice(t *testing.T) {
	t.Parallel()

	t.Run("respects weights", func(t *testing.T) {
		t.Parallel()
		gen := &WeightedChoice{
			Choices:      []any{"200", "404", "500"},
			CumulWeights: []int{90, 95, 100},
			TotalWeight:  100,
		}
		rng := rand.New(rand.NewPCG(42, 0)) //nolint:gosec // deterministic seed for testing

		counts := map[any]int{}
		for range 1000 {
			counts[gen.Generate(rng)]++
		}

		// 200 should dominate (90% weight)
		assert.Greater(t, counts["200"], counts["404"])
		assert.Greater(t, counts["200"], counts["500"])
		// All values should appear
		assert.Greater(t, counts["200"], 0)
		assert.Greater(t, counts["404"], 0)
		assert.Greater(t, counts["500"], 0)
	})

	t.Run("single choice always returns it", func(t *testing.T) {
		t.Parallel()
		gen := &WeightedChoice{
			Choices:      []any{"only"},
			CumulWeights: []int{1},
			TotalWeight:  1,
		}
		rng := rand.New(rand.NewPCG(42, 0)) //nolint:gosec // deterministic seed for testing

		for range 10 {
			assert.Equal(t, "only", gen.Generate(rng))
		}
	})
}

func TestSequenceValue(t *testing.T) {
	t.Parallel()

	gen := &SequenceValue{Pattern: "user-{n}"}
	rng := rand.New(rand.NewPCG(42, 0)) //nolint:gosec // deterministic seed for testing

	assert.Equal(t, "user-1", gen.Generate(rng))
	assert.Equal(t, "user-2", gen.Generate(rng))
	assert.Equal(t, "user-3", gen.Generate(rng))
}

func TestSequenceValueMultiplePlaceholders(t *testing.T) {
	t.Parallel()

	gen := &SequenceValue{Pattern: "req-{n}-trace-{n}"}
	rng := rand.New(rand.NewPCG(42, 0)) //nolint:gosec // deterministic seed for testing

	assert.Equal(t, "req-1-trace-1", gen.Generate(rng))
	assert.Equal(t, "req-2-trace-2", gen.Generate(rng))
}

func TestNewAttributeGenerator(t *testing.T) {
	t.Parallel()

	t.Run("static value", func(t *testing.T) {
		t.Parallel()
		gen, err := NewAttributeGenerator(AttributeValueConfig{
			Value: "/api/v1/users",
		})
		require.NoError(t, err)
		assert.IsType(t, &StaticValue{}, gen)
	})

	t.Run("weighted values", func(t *testing.T) {
		t.Parallel()
		gen, err := NewAttributeGenerator(AttributeValueConfig{
			Values: map[string]int{"200": 95, "404": 3, "500": 2},
		})
		require.NoError(t, err)
		assert.IsType(t, &WeightedChoice{}, gen)
	})

	t.Run("sequence", func(t *testing.T) {
		t.Parallel()
		gen, err := NewAttributeGenerator(AttributeValueConfig{
			Sequence: "user-{n}",
		})
		require.NoError(t, err)
		assert.IsType(t, &SequenceValue{}, gen)
	})

	t.Run("no fields set is error", func(t *testing.T) {
		t.Parallel()
		_, err := NewAttributeGenerator(AttributeValueConfig{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "exactly one")
	})

	t.Run("multiple fields set is error", func(t *testing.T) {
		t.Parallel()
		_, err := NewAttributeGenerator(AttributeValueConfig{
			Value:  "static",
			Values: map[string]int{"a": 1},
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "exactly one")
	})

	t.Run("empty values map is error", func(t *testing.T) {
		t.Parallel()
		_, err := NewAttributeGenerator(AttributeValueConfig{
			Values: map[string]int{},
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "exactly one")
	})

	t.Run("zero weight is error", func(t *testing.T) {
		t.Parallel()
		_, err := NewAttributeGenerator(AttributeValueConfig{
			Values: map[string]int{"ok": 0},
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "positive")
	})

	t.Run("negative weight is error", func(t *testing.T) {
		t.Parallel()
		_, err := NewAttributeGenerator(AttributeValueConfig{
			Values: map[string]int{"ok": -1},
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "positive")
	})
}

func TestTypedAttribute(t *testing.T) {
	t.Parallel()

	t.Run("string", func(t *testing.T) {
		t.Parallel()
		kv := typedAttribute("key", "value")
		assert.Equal(t, "key", string(kv.Key))
		assert.Equal(t, "value", kv.Value.AsString())
	})

	t.Run("bool", func(t *testing.T) {
		t.Parallel()
		kv := typedAttribute("key", true)
		assert.Equal(t, true, kv.Value.AsBool())
	})

	t.Run("int", func(t *testing.T) {
		t.Parallel()
		kv := typedAttribute("key", 42)
		assert.Equal(t, int64(42), kv.Value.AsInt64())
	})

	t.Run("int64", func(t *testing.T) {
		t.Parallel()
		kv := typedAttribute("key", int64(99))
		assert.Equal(t, int64(99), kv.Value.AsInt64())
	})

	t.Run("float64", func(t *testing.T) {
		t.Parallel()
		kv := typedAttribute("key", 3.14)
		assert.InDelta(t, 3.14, kv.Value.AsFloat64(), 0.001)
	})

	t.Run("fallback to string", func(t *testing.T) {
		t.Parallel()
		kv := typedAttribute("key", uint32(7))
		assert.Equal(t, "7", kv.Value.AsString())
	})
}
