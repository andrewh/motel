// Mapping functions that create attribute value generators from semantic
// convention definitions.
package semconv

import (
	"fmt"
	"slices"
	"strings"

	"github.com/andrewh/motel/pkg/synth"
)

// GeneratorFor creates an AttributeGenerator for the given semantic convention
// attribute. Returns an error for unsupported types (templates, arrays, empty).
func GeneratorFor(attr *Attribute) (synth.AttributeGenerator, error) {
	typ := attr.Type.Value

	switch {
	case typ == "enum":
		return generatorForEnum(attr)
	case typ == "boolean":
		return equalWeightChoice([]any{false, true}), nil
	case typ == "string":
		return generatorForScalar(attr, "unknown")
	case typ == "int":
		return generatorForScalar(attr, int64(0))
	case typ == "double":
		return generatorForScalar(attr, float64(0.0))
	case strings.HasPrefix(typ, "template["):
		return nil, fmt.Errorf("unsupported type: %s", typ)
	case strings.HasSuffix(typ, "[]"):
		return nil, fmt.Errorf("unsupported type: %s", typ)
	case typ == "":
		return nil, fmt.Errorf("no type information")
	default:
		return nil, fmt.Errorf("unsupported type: %s", typ)
	}
}

// GeneratorsFor creates AttributeGenerators for all supported attributes in
// a group. Attributes with empty ID, deprecated status, or unsupported types
// are silently skipped.
func GeneratorsFor(group *Group) map[string]synth.AttributeGenerator {
	result := make(map[string]synth.AttributeGenerator)
	for i := range group.Attributes {
		attr := &group.Attributes[i]
		if attr.ID == "" || attr.Deprecated != nil {
			continue
		}
		gen, err := GeneratorFor(attr)
		if err != nil {
			continue
		}
		result[attr.ID] = gen
	}
	return result
}

func generatorForEnum(attr *Attribute) (synth.AttributeGenerator, error) {
	values := enumValues(attr)
	if len(values) > 0 {
		return equalWeightChoice(values), nil
	}
	if len(attr.Type.Members) > 0 {
		return &synth.StaticValue{Value: attr.Type.Members[0].Value}, nil
	}
	return nil, fmt.Errorf("enum with no members")
}

func generatorForScalar(attr *Attribute, fallback any) (synth.AttributeGenerator, error) {
	examples := scalarExamples(attr)
	if len(examples) > 0 {
		return equalWeightChoice(examples), nil
	}
	return &synth.StaticValue{Value: fallback}, nil
}

func equalWeightChoice(values []any) *synth.WeightedChoice {
	sorted := make([]any, len(values))
	copy(sorted, values)
	slices.SortFunc(sorted, func(a, b any) int {
		return strings.Compare(fmt.Sprint(a), fmt.Sprint(b))
	})

	cumul := make([]int, len(sorted))
	for i := range sorted {
		cumul[i] = i + 1
	}

	return &synth.WeightedChoice{
		Choices:      sorted,
		CumulWeights: cumul,
		TotalWeight:  len(sorted),
	}
}

func scalarExamples(attr *Attribute) []any {
	result := make([]any, 0, len(attr.Examples.Values))
	for _, v := range attr.Examples.Values {
		if _, ok := v.([]any); ok {
			continue
		}
		result = append(result, v)
	}
	return result
}

func enumValues(attr *Attribute) []any {
	result := make([]any, 0, len(attr.Type.Members))
	for _, m := range attr.Type.Members {
		if m.Deprecated != nil {
			continue
		}
		result = append(result, m.Value)
	}
	return result
}
