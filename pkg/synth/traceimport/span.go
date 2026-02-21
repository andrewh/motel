// Normalised span type and format-specific parsers for trace inference
// Handles both stdouttrace (line-delimited JSON) and OTLP protobuf JSON formats
package traceimport

import (
	"bufio"
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/encoding/protojson"
)

// Span is the format-independent representation of a trace span.
type Span struct {
	TraceID    string
	SpanID     string
	ParentID   string // empty for root spans
	Service    string
	Operation  string
	StartTime  time.Time
	EndTime    time.Time
	IsError    bool
	Attributes map[string]string
}

// Format identifies the input trace format.
type Format string

const (
	FormatAuto        Format = "auto"
	FormatStdouttrace Format = "stdouttrace"
	FormatOTLP        Format = "otlp"
)

// maxInputSize is the maximum input size to prevent OOM on large trace exports.
const maxInputSize = 256 * 1024 * 1024 // 256 MB

// ParseSpans reads spans from the given reader in the specified format.
// FormatAuto inspects the first JSON object to determine the format.
// Input is limited to 256 MB to prevent OOM on large trace exports.
func ParseSpans(r io.Reader, format Format) ([]Span, error) {
	data, err := io.ReadAll(io.LimitReader(r, maxInputSize+1))
	if err != nil {
		return nil, fmt.Errorf("reading input: %w", err)
	}
	if len(data) > maxInputSize {
		return nil, fmt.Errorf("input exceeds maximum size of %d MB", maxInputSize/(1024*1024))
	}
	data = bytes.TrimSpace(data)
	if len(data) == 0 {
		return nil, fmt.Errorf("no spans found in input\n\nProvide a file or pipe stdin:\n  motel import traces.json\n  cat traces.json | motel import")
	}

	if format == FormatAuto {
		format, err = detectFormat(data)
		if err != nil {
			return nil, err
		}
	}

	switch format {
	case FormatStdouttrace:
		return parseStdouttrace(data)
	case FormatOTLP:
		return parseOTLP(data)
	default:
		return nil, fmt.Errorf("unknown format %q, valid formats: auto, stdouttrace, otlp", format)
	}
}

// detectFormat examines the input to determine the format.
// Tries the first line (for line-delimited stdouttrace), then the full data
// (for pretty-printed OTLP JSON).
func detectFormat(data []byte) (Format, error) {
	firstLine, _, hasMore := bytes.Cut(data, []byte{'\n'})
	firstLine = bytes.TrimSpace(firstLine)

	var probe map[string]json.RawMessage
	if err := json.Unmarshal(firstLine, &probe); err == nil {
		if _, ok := probe["SpanContext"]; ok {
			return FormatStdouttrace, nil
		}
		if _, ok := probe["resourceSpans"]; ok {
			return FormatOTLP, nil
		}
	}

	// First line wasn't a complete JSON object (e.g. pretty-printed OTLP).
	// Try the full input as a single JSON document.
	if hasMore {
		if err := json.Unmarshal(data, &probe); err == nil {
			if _, ok := probe["resourceSpans"]; ok {
				return FormatOTLP, nil
			}
			if _, ok := probe["SpanContext"]; ok {
				return FormatStdouttrace, nil
			}
		}
	}

	return "", fmt.Errorf("cannot detect format: input has neither SpanContext (stdouttrace) nor resourceSpans (OTLP)")
}

// stdouttraceEvent mirrors the Go SDK's stdouttrace JSON output.
type stdouttraceEvent struct {
	Name        string `json:"Name"`
	SpanContext struct {
		TraceID string `json:"TraceID"`
		SpanID  string `json:"SpanID"`
	} `json:"SpanContext"`
	Parent struct {
		TraceID string `json:"TraceID"`
		SpanID  string `json:"SpanID"`
	} `json:"Parent"`
	StartTime            time.Time `json:"StartTime"`
	EndTime              time.Time `json:"EndTime"`
	Attributes           []sdkAttr `json:"Attributes"`
	Status               sdkStatus `json:"Status"`
	InstrumentationScope struct {
		Name string `json:"Name"`
	} `json:"InstrumentationScope"`
}

type sdkAttr struct {
	Key   string `json:"Key"`
	Value struct {
		Type  string `json:"Type"`
		Value any    `json:"Value"`
	} `json:"Value"`
}

type sdkStatus struct {
	Code string `json:"Code"`
}

// excludedAttributes are engine-internal or infrastructure attributes to omit.
var excludedAttributes = map[string]bool{
	"synth.service":          true,
	"synth.operation":        true,
	"telemetry.sdk.language": true,
	"telemetry.sdk.name":     true,
	"telemetry.sdk.version":  true,
	"service.name":           true,
}

func parseStdouttrace(data []byte) ([]Span, error) {
	var spans []Span
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 0, 1024*1024), 10*1024*1024)
	lineNum := 0

	for scanner.Scan() {
		lineNum++
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}

		var evt stdouttraceEvent
		if err := json.Unmarshal(line, &evt); err != nil {
			return nil, fmt.Errorf("line %d: %w", lineNum, err)
		}

		// Determine service name: InstrumentationScope.Name, with synth.service fallback
		service := evt.InstrumentationScope.Name
		if service == "" {
			for _, attr := range evt.Attributes {
				if attr.Key == "synth.service" {
					if s, ok := attr.Value.Value.(string); ok {
						service = s
					}
				}
			}
		}

		// Determine parent ID, treating all-zeros as empty (root span)
		parentID := evt.Parent.SpanID
		if isZeroID(parentID) {
			parentID = ""
		}

		// Flatten attributes, excluding engine-internal ones
		attrs := make(map[string]string)
		for _, attr := range evt.Attributes {
			if excludedAttributes[attr.Key] {
				continue
			}
			attrs[attr.Key] = fmt.Sprint(attr.Value.Value)
		}

		spans = append(spans, Span{
			TraceID:    evt.SpanContext.TraceID,
			SpanID:     evt.SpanContext.SpanID,
			ParentID:   parentID,
			Service:    service,
			Operation:  evt.Name,
			StartTime:  evt.StartTime,
			EndTime:    evt.EndTime,
			IsError:    evt.Status.Code == "Error",
			Attributes: attrs,
		})
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("reading input: %w", err)
	}
	if len(spans) == 0 {
		return nil, fmt.Errorf("no spans found in input\n\nProvide a file or pipe stdin:\n  motel import traces.json\n  cat traces.json | motel import")
	}
	return spans, nil
}

func parseOTLP(data []byte) ([]Span, error) {
	var req coltracepb.ExportTraceServiceRequest
	opts := protojson.UnmarshalOptions{DiscardUnknown: true}
	if err := opts.Unmarshal(data, &req); err != nil {
		return nil, fmt.Errorf("parsing OTLP: %w", err)
	}

	var spans []Span
	for _, rs := range req.ResourceSpans {
		// Extract service.name from resource attributes
		serviceName := ""
		for _, attr := range rs.Resource.GetAttributes() {
			if attr.Key == "service.name" {
				serviceName = attr.Value.GetStringValue()
			}
		}

		for _, ss := range rs.ScopeSpans {
			scopeName := ss.Scope.GetName()

			for _, span := range ss.Spans {
				svc := serviceName
				if svc == "" {
					svc = scopeName
				}

				parentID := hex.EncodeToString(span.ParentSpanId)
				if isZeroID(parentID) || len(span.ParentSpanId) == 0 {
					parentID = ""
				}

				isError := span.Status != nil && span.Status.Code == tracepb.Status_STATUS_CODE_ERROR

				attrs := make(map[string]string)
				for _, attr := range span.Attributes {
					if excludedAttributes[attr.Key] {
						continue
					}
					attrs[attr.Key] = attrValueString(attr.Value)
				}

				spans = append(spans, Span{
					TraceID:    hex.EncodeToString(span.TraceId),
					SpanID:     hex.EncodeToString(span.SpanId),
					ParentID:   parentID,
					Service:    svc,
					Operation:  span.Name,
					StartTime:  time.Unix(0, int64(span.StartTimeUnixNano)), //nolint:gosec // nanosecond timestamps are always positive
					EndTime:    time.Unix(0, int64(span.EndTimeUnixNano)),   //nolint:gosec // nanosecond timestamps are always positive
					IsError:    isError,
					Attributes: attrs,
				})
			}
		}
	}

	if len(spans) == 0 {
		return nil, fmt.Errorf("no spans found in input\n\nProvide a file or pipe stdin:\n  motel import traces.json\n  cat traces.json | motel import")
	}
	return spans, nil
}

// isZeroID checks if a hex-encoded ID is all zeros.
func isZeroID(id string) bool {
	for _, c := range id {
		if c != '0' {
			return false
		}
	}
	return len(id) > 0
}

// attrValueString extracts a string representation from an OTLP AnyValue.
// For non-string values, proto oneofs format as "type_key:value" so we
// extract just the value portion.
func attrValueString(v interface{ GetStringValue() string }) string {
	s := v.GetStringValue()
	if s != "" {
		return s
	}
	str := fmt.Sprintf("%v", v)
	if _, after, ok := strings.Cut(str, ":"); ok {
		return strings.TrimSpace(after)
	}
	return strings.TrimSpace(str)
}
