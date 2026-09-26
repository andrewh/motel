package traceimport

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"math"
	"reflect"
	"strconv"

	"github.com/andrewh/motel/pkg/synth"
)

const (
	attributePreserved = "preserved"
	attributeOmitted   = "omitted"
	attributeOmissions = "omissions"
	scalarString       = "string"
	scalarInt64        = "int64"
	scalarFloat64      = "float64"
	scalarBool         = "bool"
)

var errTrailingJSON = errors.New("unexpected trailing JSON value")

type AttributeEvidence struct {
	ObservationCount int                            `yaml:"observation_count"`
	ImportStatus     string                         `yaml:"import_status"`
	Keys             map[string]AttributeAssessment `yaml:"keys"`
}

type AttributeAssessment struct {
	PresentCount     int      `yaml:"present_count"`
	UnsupportedCount int      `yaml:"unsupported_count"`
	ImportStatus     string   `yaml:"import_status"`
	ScalarType       string   `yaml:"scalar_type,omitempty"`
	Reasons          []string `yaml:"reasons"`
}

func decodeNumbers(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err != nil {
			return err
		}
		return errTrailingJSON
	}
	return nil
}

func numericScalar(value any, integer bool) any {
	number, ok := value.(json.Number)
	if !ok {
		return nil
	}
	if integer {
		v, err := strconv.ParseInt(string(number), 10, 64)
		if err == nil {
			return v
		}
		return nil
	}
	v, err := strconv.ParseFloat(string(number), 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
		return nil
	}
	return v
}

func sdkScalar(attr sdkAttr) any {
	switch attr.Value.Type {
	case "STRING":
		v, ok := attr.Value.Value.(string)
		if ok {
			return v
		}
	case "BOOL":
		v, ok := attr.Value.Value.(bool)
		if ok {
			return v
		}
	case "INT64":
		return numericScalar(attr.Value.Value, true)
	case "FLOAT64":
		return numericScalar(attr.Value.Value, false)
	}
	return nil
}

func (v otlpAnyValue) scalar() any {
	switch {
	case v.StringValue != nil:
		return *v.StringValue
	case v.IntValue != nil:
		return int64(*v.IntValue)
	case v.DoubleValue != nil:
		return float64(*v.DoubleValue)
	case v.BoolValue != nil:
		return *v.BoolValue
	default:
		return nil
	}
}

func jaegerScalar(tag jaegerTag) any {
	var value any
	if err := decodeNumbers(tag.Value, &value); err != nil {
		return nil
	}
	switch v := value.(type) {
	case string:
		if tag.Type == "" || tag.Type == scalarString {
			return v
		}
	case bool:
		if tag.Type == "" || tag.Type == scalarBool {
			return v
		}
	case json.Number:
		switch tag.Type {
		case scalarInt64:
			return numericScalar(v, true)
		case scalarFloat64:
			return numericScalar(v, false)
		case "":
			if n := numericScalar(v, true); n != nil {
				return n
			}
			return numericScalar(v, false)
		}
	}
	return nil
}

func sdkKeys(attrs []sdkAttr) []string {
	keys := make([]string, 0, len(attrs))
	for _, attr := range attrs {
		keys = append(keys, attr.Key)
	}
	return keys
}
func otlpKeys(attrs []otlpKeyValue) []string {
	keys := make([]string, 0, len(attrs))
	for _, attr := range attrs {
		keys = append(keys, attr.Key)
	}
	return keys
}
func jaegerResourceKeys(span jaegerSpan, processes map[string]*jaegerProcess) []string {
	process := span.Process
	if process == nil {
		process = processes[span.ProcessID]
	}
	if process == nil {
		return nil
	}
	keys := make([]string, 0, len(process.Tags))
	for _, tag := range process.Tags {
		keys = append(keys, tag.Key)
	}
	return keys
}

func assessAttributes(trees []*TraceTree, evidence *ImportEvidence) {
	grouped := map[OperationKey][]Span{}
	evidence.ResourceOmissions = map[string]int{}
	for _, tree := range trees {
		for _, node := range tree.AllNodes {
			span := node.Span
			ref := OperationKey{span.Service, span.Operation}
			grouped[ref] = append(grouped[ref], span)
			for _, key := range span.ResourceKeys {
				if key != serviceNameKey {
					evidence.ResourceOmissions[key]++
				}
			}
		}
	}
	for ref, op := range evidence.Operations {
		spans := grouped[ref]
		assessment := AttributeEvidence{ObservationCount: len(spans), ImportStatus: attributePreserved, Keys: map[string]AttributeAssessment{}}
		first := map[string]any{}
		varied := map[string]bool{}
		for _, span := range spans {
			for key, value := range span.TypedAttributes {
				a := assessment.Keys[key]
				if a.PresentCount == 0 {
					first[key] = value
				} else if !reflect.DeepEqual(first[key], value) {
					varied[key] = true
				}
				a.PresentCount++
				if value == nil {
					a.UnsupportedCount++
				}
				assessment.Keys[key] = a
			}
		}
		op.RetainedAttributes = map[string]synth.AttributeValueConfig{}
		for key, a := range assessment.Keys {
			a.Reasons = []string{}
			if a.PresentCount < len(spans) {
				a.Reasons = append(a.Reasons, "partial_presence")
			}
			if varied[key] {
				a.Reasons = append(a.Reasons, "varying_value_or_type")
			}
			if a.UnsupportedCount > 0 {
				a.Reasons = append(a.Reasons, "unsupported_value")
			}
			if reservedEngineAttribute(key) {
				a.Reasons = append(a.Reasons, "reserved_engine_attribute")
			}
			if len(a.Reasons) == 0 {
				a.ImportStatus = attributePreserved
				switch first[key].(type) {
				case string:
					a.ScalarType = scalarString
				case int64:
					a.ScalarType = scalarInt64
				case float64:
					a.ScalarType = scalarFloat64
				case bool:
					a.ScalarType = scalarBool
				}
				op.RetainedAttributes[key] = synth.AttributeValueConfig{Value: first[key]}
			} else {
				a.ImportStatus = attributeOmitted
				assessment.ImportStatus = attributeOmissions
			}
			assessment.Keys[key] = a
		}
		op.Attributes = assessment
	}
}
