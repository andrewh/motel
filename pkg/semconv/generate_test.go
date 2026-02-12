// Unit tests for semantic convention attribute generator mapping functions.
// Uses inline Attribute/Group structs for isolation; real ModelFS for smoke tests.
package semconv

import (
	"math/rand/v2"
	"slices"
	"testing"

	"github.com/andrewh/motel/pkg/synth"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- Phase 1: Individual Type Generators ---

func TestGeneratorFor_Enum(t *testing.T) {
	t.Parallel()
	attr := &Attribute{
		Type: AttributeType{
			Value: "enum",
			Members: []EnumMember{
				{ID: "get", Value: "GET", Stability: "stable"},
				{ID: "post", Value: "POST", Stability: "stable"},
				{ID: "head", Value: "HEAD", Stability: "stable"},
			},
		},
	}
	gen, err := GeneratorFor(attr)
	require.NoError(t, err)
	wc, ok := gen.(*synth.WeightedChoice)
	require.True(t, ok, "expected *synth.WeightedChoice")
	assert.Equal(t, []any{"GET", "HEAD", "POST"}, wc.Choices)
	assert.Equal(t, 3, wc.TotalWeight)
}

func TestGeneratorFor_Enum_SkipsDeprecated(t *testing.T) {
	t.Parallel()
	attr := &Attribute{
		Type: AttributeType{
			Value: "enum",
			Members: []EnumMember{
				{ID: "get", Value: "GET", Stability: "stable"},
				{ID: "post", Value: "POST", Stability: "stable"},
				{ID: "old", Value: "OLD", Deprecated: "Use something else"},
			},
		},
	}
	gen, err := GeneratorFor(attr)
	require.NoError(t, err)
	wc, ok := gen.(*synth.WeightedChoice)
	require.True(t, ok)
	assert.Equal(t, []any{"GET", "POST"}, wc.Choices)
}

func TestGeneratorFor_Enum_AllDeprecated(t *testing.T) {
	t.Parallel()
	attr := &Attribute{
		Type: AttributeType{
			Value: "enum",
			Members: []EnumMember{
				{ID: "old1", Value: "OLD1", Deprecated: "removed"},
				{ID: "old2", Value: "OLD2", Deprecated: "removed"},
			},
		},
	}
	gen, err := GeneratorFor(attr)
	require.NoError(t, err)
	sv, ok := gen.(*synth.StaticValue)
	require.True(t, ok, "expected *synth.StaticValue")
	assert.Equal(t, "OLD1", sv.Value)
}

func TestGeneratorFor_Enum_IntValues(t *testing.T) {
	t.Parallel()
	attr := &Attribute{
		Type: AttributeType{
			Value: "enum",
			Members: []EnumMember{
				{ID: "ok", Value: 200, Stability: "stable"},
				{ID: "not_found", Value: 404, Stability: "stable"},
			},
		},
	}
	gen, err := GeneratorFor(attr)
	require.NoError(t, err)
	wc, ok := gen.(*synth.WeightedChoice)
	require.True(t, ok)
	assert.Equal(t, []any{200, 404}, wc.Choices)
}

func TestGeneratorFor_StringWithExamples(t *testing.T) {
	t.Parallel()
	attr := &Attribute{
		Type:     AttributeType{Value: "string"},
		Examples: Examples{Values: []any{"foo", "bar", "baz"}},
	}
	gen, err := GeneratorFor(attr)
	require.NoError(t, err)
	wc, ok := gen.(*synth.WeightedChoice)
	require.True(t, ok)
	assert.Equal(t, []any{"bar", "baz", "foo"}, wc.Choices)
}

func TestGeneratorFor_StringWithoutExamples(t *testing.T) {
	t.Parallel()
	attr := &Attribute{
		Type: AttributeType{Value: "string"},
	}
	gen, err := GeneratorFor(attr)
	require.NoError(t, err)
	sv, ok := gen.(*synth.StaticValue)
	require.True(t, ok)
	assert.Equal(t, "unknown", sv.Value)
}

func TestGeneratorFor_StringNestedExamples(t *testing.T) {
	t.Parallel()
	attr := &Attribute{
		Type: AttributeType{Value: "string"},
		Examples: Examples{Values: []any{
			[]any{"a", "b"},
			"scalar1",
			[]any{"c"},
			"scalar2",
		}},
	}
	gen, err := GeneratorFor(attr)
	require.NoError(t, err)
	wc, ok := gen.(*synth.WeightedChoice)
	require.True(t, ok)
	assert.Equal(t, []any{"scalar1", "scalar2"}, wc.Choices)
}

func TestGeneratorFor_IntWithExamples(t *testing.T) {
	t.Parallel()
	attr := &Attribute{
		Type:     AttributeType{Value: "int"},
		Examples: Examples{Values: []any{80, 443, 8080}},
	}
	gen, err := GeneratorFor(attr)
	require.NoError(t, err)
	wc, ok := gen.(*synth.WeightedChoice)
	require.True(t, ok)
	assert.Equal(t, []any{443, 80, 8080}, wc.Choices)
}

func TestGeneratorFor_IntWithoutExamples(t *testing.T) {
	t.Parallel()
	attr := &Attribute{
		Type: AttributeType{Value: "int"},
	}
	gen, err := GeneratorFor(attr)
	require.NoError(t, err)
	sv, ok := gen.(*synth.StaticValue)
	require.True(t, ok)
	assert.Equal(t, int64(0), sv.Value)
}

func TestGeneratorFor_DoubleWithExamples(t *testing.T) {
	t.Parallel()
	attr := &Attribute{
		Type:     AttributeType{Value: "double"},
		Examples: Examples{Values: []any{0.5, 1.0, 2.5}},
	}
	gen, err := GeneratorFor(attr)
	require.NoError(t, err)
	wc, ok := gen.(*synth.WeightedChoice)
	require.True(t, ok)
	assert.Equal(t, []any{0.5, 1.0, 2.5}, wc.Choices)
}

func TestGeneratorFor_DoubleWithoutExamples(t *testing.T) {
	t.Parallel()
	attr := &Attribute{
		Type: AttributeType{Value: "double"},
	}
	gen, err := GeneratorFor(attr)
	require.NoError(t, err)
	sv, ok := gen.(*synth.StaticValue)
	require.True(t, ok)
	assert.Equal(t, float64(0.0), sv.Value)
}

func TestGeneratorFor_Boolean(t *testing.T) {
	t.Parallel()
	attr := &Attribute{
		Type: AttributeType{Value: "boolean"},
	}
	gen, err := GeneratorFor(attr)
	require.NoError(t, err)
	wc, ok := gen.(*synth.WeightedChoice)
	require.True(t, ok)
	assert.Equal(t, []any{false, true}, wc.Choices)
	assert.Equal(t, 2, wc.TotalWeight)
}

// --- Phase 2: Error Cases ---

func TestGeneratorFor_Template(t *testing.T) {
	t.Parallel()
	attr := &Attribute{
		Type: AttributeType{Value: "template[string]"},
	}
	_, err := GeneratorFor(attr)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported type")
}

func TestGeneratorFor_StringArray(t *testing.T) {
	t.Parallel()
	attr := &Attribute{
		Type: AttributeType{Value: "string[]"},
	}
	_, err := GeneratorFor(attr)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported type")
}

func TestGeneratorFor_EmptyType(t *testing.T) {
	t.Parallel()
	attr := &Attribute{}
	_, err := GeneratorFor(attr)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no type information")
}

// --- Phase 3: Group Generation ---

func TestGeneratorsFor_BasicGroup(t *testing.T) {
	t.Parallel()
	group := &Group{
		Attributes: []Attribute{
			{
				ID:       "test.method",
				Type:     AttributeType{Value: "string"},
				Examples: Examples{Values: []any{"GET", "POST"}},
			},
			{
				ID:       "test.status",
				Type:     AttributeType{Value: "int"},
				Examples: Examples{Values: []any{200, 404}},
			},
		},
	}
	gens := GeneratorsFor(group)
	assert.Len(t, gens, 2)
	assert.Contains(t, gens, "test.method")
	assert.Contains(t, gens, "test.status")
}

func TestGeneratorsFor_SkipsDeprecated(t *testing.T) {
	t.Parallel()
	group := &Group{
		Attributes: []Attribute{
			{
				ID:       "test.active",
				Type:     AttributeType{Value: "string"},
				Examples: Examples{Values: []any{"yes"}},
			},
			{
				ID:         "test.old",
				Type:       AttributeType{Value: "string"},
				Deprecated: "Use test.active instead",
				Examples:   Examples{Values: []any{"old"}},
			},
		},
	}
	gens := GeneratorsFor(group)
	assert.Len(t, gens, 1)
	assert.Contains(t, gens, "test.active")
	assert.NotContains(t, gens, "test.old")
}

func TestGeneratorsFor_SkipsUnsupported(t *testing.T) {
	t.Parallel()
	group := &Group{
		Attributes: []Attribute{
			{
				ID:       "test.name",
				Type:     AttributeType{Value: "string"},
				Examples: Examples{Values: []any{"foo"}},
			},
			{
				ID:   "test.template",
				Type: AttributeType{Value: "template[string]"},
			},
		},
	}
	gens := GeneratorsFor(group)
	assert.Len(t, gens, 1)
	assert.Contains(t, gens, "test.name")
}

func TestGeneratorsFor_EmptyGroup(t *testing.T) {
	t.Parallel()
	group := &Group{}
	gens := GeneratorsFor(group)
	require.NotNil(t, gens)
	assert.Empty(t, gens)
}

// --- Phase 4: Embedded Smoke Tests ---

func TestGeneratorFor_RealHTTPMethod(t *testing.T) {
	t.Parallel()
	reg, err := LoadEmbedded()
	require.NoError(t, err)

	attr := reg.Attribute("http.request.method")
	require.NotNil(t, attr)

	gen, err := GeneratorFor(attr)
	require.NoError(t, err)

	wc, ok := gen.(*synth.WeightedChoice)
	require.True(t, ok, "expected *synth.WeightedChoice")
	assert.Greater(t, len(wc.Choices), 5)

	knownMethods := map[string]bool{
		"CONNECT": true, "DELETE": true, "GET": true, "HEAD": true,
		"OPTIONS": true, "PATCH": true, "POST": true, "PUT": true,
		"TRACE": true, "_OTHER": true,
	}
	rng := rand.New(rand.NewPCG(42, 0)) //nolint:gosec // deterministic RNG for test reproducibility
	for range 10 {
		val, ok := gen.Generate(rng).(string)
		require.True(t, ok, "expected string from HTTP method generator")
		assert.True(t, knownMethods[val], "unexpected method: %s", val)
	}
}

func TestGeneratorsFor_RealHTTPRegistry(t *testing.T) {
	t.Parallel()
	reg, err := LoadEmbedded()
	require.NoError(t, err)

	group := reg.Group("registry.http")
	require.NotNil(t, group)

	gens := GeneratorsFor(group)
	assert.Greater(t, len(gens), 0)

	ids := make([]string, 0, len(gens))
	for id := range gens {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	assert.True(t, slices.Contains(ids, "http.request.method"),
		"expected http.request.method in %v", ids)
}
