// Package semconv loads and indexes OTel semantic convention definitions
// from the OpenTelemetry semantic convention YAML format.
package semconv

import "fmt"

// AttributeType represents the type of a semantic convention attribute.
// For scalar types (string, int, boolean, etc.) Value holds the type name.
// For enum types, Value is "enum" and Members is populated.
type AttributeType struct {
	Value   string
	Members []EnumMember
}

// UnmarshalYAML handles both scalar type strings and enum definitions with members.
func (t *AttributeType) UnmarshalYAML(unmarshal func(any) error) error {
	// Try scalar string first (e.g. "string", "int", "boolean").
	var scalar string
	if err := unmarshal(&scalar); err == nil {
		t.Value = scalar
		return nil
	}

	// Must be a mapping with members (enum type).
	var mapping struct {
		Members []EnumMember `yaml:"members"`
	}
	if err := unmarshal(&mapping); err != nil {
		return fmt.Errorf("attribute type: expected string or mapping with members: %w", err)
	}
	t.Value = "enum"
	t.Members = mapping.Members
	return nil
}

// EnumMember represents a single member of an enum attribute type.
type EnumMember struct {
	ID         string `yaml:"id"`
	Value      any    `yaml:"value"`
	Brief      string `yaml:"brief"`
	Stability  string `yaml:"stability"`
	Note       string `yaml:"note"`
	Deprecated any    `yaml:"deprecated"`
}

// RequirementLevel represents the requirement level of an attribute within a group.
// For simple levels (required, recommended, opt_in), Level holds the value.
// For conditional levels, Level is the condition key and Explanation holds the detail.
type RequirementLevel struct {
	Level       string
	Explanation string
}

// UnmarshalYAML handles both scalar levels and conditional requirement mappings.
func (r *RequirementLevel) UnmarshalYAML(unmarshal func(any) error) error {
	// Try scalar string first (e.g. "required", "recommended").
	var scalar string
	if err := unmarshal(&scalar); err == nil {
		r.Level = scalar
		return nil
	}

	// Must be a mapping like {conditionally_required: "explanation"}.
	var mapping map[string]string
	if err := unmarshal(&mapping); err != nil {
		return fmt.Errorf("requirement level: expected string or mapping: %w", err)
	}
	for k, v := range mapping {
		r.Level = k
		r.Explanation = v
		break
	}
	return nil
}

// Examples holds example values for an attribute.
// The YAML may contain a scalar, a flat array, or nested arrays.
type Examples struct {
	Values []any
}

// UnmarshalYAML handles scalar values and sequences of examples.
func (e *Examples) UnmarshalYAML(unmarshal func(any) error) error {
	// Try as a sequence first.
	var seq []any
	if err := unmarshal(&seq); err == nil {
		e.Values = seq
		return nil
	}

	// Must be a scalar value.
	var scalar any
	if err := unmarshal(&scalar); err != nil {
		return fmt.Errorf("examples: expected scalar or sequence: %w", err)
	}
	e.Values = []any{scalar}
	return nil
}

// Attribute represents a single semantic convention attribute definition or reference.
// When Ref is set, this attribute references another attribute by ID.
type Attribute struct {
	ID               string           `yaml:"id"`
	Type             AttributeType    `yaml:"type"`
	Brief            string           `yaml:"brief"`
	Note             string           `yaml:"note"`
	Stability        string           `yaml:"stability"`
	Examples         Examples         `yaml:"examples"`
	Deprecated       any              `yaml:"deprecated"`
	Ref              string           `yaml:"ref"`
	RequirementLevel RequirementLevel `yaml:"requirement_level"`
	SamplingRelevant bool             `yaml:"sampling_relevant"`
}

// Group represents a semantic convention group (attribute_group, metric, span, event, entity).
type Group struct {
	ID          string      `yaml:"id"`
	Type        string      `yaml:"type"`
	DisplayName string      `yaml:"display_name"`
	Brief       string      `yaml:"brief"`
	Note        string      `yaml:"note"`
	Stability   string      `yaml:"stability"`
	Extends     string      `yaml:"extends"`
	SpanKind    string      `yaml:"span_kind"`
	MetricName  string      `yaml:"metric_name"`
	Instrument  string      `yaml:"instrument"`
	Unit        string      `yaml:"unit"`
	Name        string      `yaml:"name"`
	Attributes  []Attribute `yaml:"attributes"`

	domain string // derived from the file path directory, not serialised
}
