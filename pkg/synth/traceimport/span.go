// Normalised span type and format-specific parsers for trace inference
// Handles stdouttrace, OTLP protobuf JSON, and Jaeger JSON formats
package traceimport

import (
	"bufio"
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
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

// Supported trace input formats.
const (
	FormatAuto        Format = "auto"         // Detects the format from the input.
	FormatStdouttrace Format = "stdouttrace"  // Line-delimited JSON from the OTel stdout exporter.
	FormatOTLP        Format = "otlp"         // OTLP protobuf JSON.
	FormatJaeger      Format = "jaeger"       // Jaeger JSON export format (also used by Grafana Tempo).
	FormatMetaSummary Format = "meta-summary" // Meta ATC 2023 parent-data.csv summary rows.
)

// maxInputSize is the maximum input size to prevent OOM on large trace exports.
const maxInputSize = 256 * 1024 * 1024 // 256 MB
const maxStdouttraceLineSize = 10 * 1024 * 1024

// ParseSpans reads spans from the given reader in the specified format.
// FormatAuto inspects the first JSON object to determine the format.
// Whole-document JSON inputs are limited to 256 MB to prevent OOM on large trace
// exports. Line-delimited stdouttrace inputs are scanned incrementally.
func ParseSpans(r io.Reader, format Format) ([]Span, error) {
	switch format {
	case FormatStdouttrace:
		return parseStdouttraceReader(r)
	case FormatAuto:
		return parseAutoSpans(r)
	case FormatOTLP:
		data, err := readLimitedInput(r)
		if err != nil {
			return nil, err
		}
		return parseOTLP(data)
	case FormatJaeger:
		data, err := readLimitedInput(r)
		if err != nil {
			return nil, err
		}
		return parseJaeger(data)
	case FormatMetaSummary:
		return nil, fmt.Errorf("meta-summary input is summary data, not trace spans; use Import")
	default:
		return nil, fmt.Errorf("unknown format %q, valid formats: auto, stdouttrace, otlp, jaeger, meta-summary", format)
	}
}

func parseAutoSpans(r io.Reader) ([]Span, error) {
	br := bufio.NewReader(r)
	prefix, firstLine, err := readDetectionPrefix(br)
	if err != nil {
		return nil, err
	}
	if firstLineFormat(firstLine) == FormatStdouttrace {
		return parseStdouttraceReader(io.MultiReader(bytes.NewReader(prefix), br))
	}

	data, err := readLimitedInput(io.MultiReader(bytes.NewReader(prefix), br))
	if err != nil {
		return nil, err
	}
	format, err := detectFormat(data)
	if err != nil {
		return nil, err
	}
	switch format {
	case FormatStdouttrace:
		return parseStdouttrace(data)
	case FormatOTLP:
		return parseOTLP(data)
	case FormatJaeger:
		return parseJaeger(data)
	default:
		return nil, fmt.Errorf("unknown format %q, valid formats: auto, stdouttrace, otlp, jaeger, meta-summary", format)
	}
}

func readLimitedInput(r io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, maxInputSize+1))
	if err != nil {
		return nil, fmt.Errorf("reading input: %w", err)
	}
	if len(data) > maxInputSize {
		return nil, fmt.Errorf("input exceeds maximum size of %d MB; stdouttrace and meta-summary inputs can be streamed with explicit --format", maxInputSize/(1024*1024))
	}
	data = bytes.TrimSpace(data)
	if len(data) == 0 {
		return nil, fmt.Errorf("no spans found in input")
	}
	return data, nil
}

func readDetectionPrefix(br *bufio.Reader) ([]byte, []byte, error) {
	var prefix []byte
	for {
		line, err := br.ReadBytes('\n')
		if len(line) > 0 {
			prefix = append(prefix, line...)
			trimmed := bytes.TrimSpace(line)
			if len(trimmed) > 0 {
				return prefix, trimmed, nil
			}
		}
		if errors.Is(err, io.EOF) {
			if len(bytes.TrimSpace(prefix)) == 0 {
				return nil, nil, fmt.Errorf("no spans found in input")
			}
			return prefix, bytes.TrimSpace(prefix), nil
		}
		if err != nil {
			return nil, nil, fmt.Errorf("reading input: %w", err)
		}
	}
}

func firstLineFormat(firstLine []byte) Format {
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(firstLine, &probe); err != nil {
		return ""
	}
	if _, ok := probe["SpanContext"]; ok {
		return FormatStdouttrace
	}
	if _, ok := probe["resourceSpans"]; ok {
		return FormatOTLP
	}
	if isJaegerData(probe["data"]) {
		return FormatJaeger
	}
	return ""
}

// detectFormat examines the input to determine the format.
// Tries the first line (for line-delimited stdouttrace), then the full data
// (for pretty-printed OTLP or Jaeger JSON).
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
		if isJaegerData(probe["data"]) {
			return FormatJaeger, nil
		}
	}

	// First line wasn't a complete JSON object (e.g. pretty-printed OTLP or Jaeger).
	// Try the full input as a single JSON document.
	if hasMore {
		if err := json.Unmarshal(data, &probe); err == nil {
			if _, ok := probe["resourceSpans"]; ok {
				return FormatOTLP, nil
			}
			if _, ok := probe["SpanContext"]; ok {
				return FormatStdouttrace, nil
			}
			if isJaegerData(probe["data"]) {
				return FormatJaeger, nil
			}
		}
	}

	return "", fmt.Errorf("cannot detect format: input has neither SpanContext (stdouttrace), resourceSpans (OTLP), nor data[].spans[].operationName (Jaeger/Tempo)")
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
	Resource             []sdkAttr `json:"Resource"`
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

// Attribute keys consulted when determining a span's service name.
// unknownServicePrefix is the placeholder the OTel SDK assigns to
// service.name when none was configured; it carries no service identity.
const (
	serviceNameKey       = "service.name"
	synthServiceKey      = "synth.service"
	unknownServicePrefix = "unknown_service"
)

// excludedAttributes are engine-internal or infrastructure attributes to omit.
var excludedAttributes = map[string]bool{
	synthServiceKey:          true,
	"synth.operation":        true,
	"synth.scenarios":        true,
	"telemetry.sdk.language": true,
	"telemetry.sdk.name":     true,
	"telemetry.sdk.version":  true,
	serviceNameKey:           true,
}

func parseStdouttrace(data []byte) ([]Span, error) {
	return parseStdouttraceReader(bytes.NewReader(data))
}

func parseStdouttraceReader(r io.Reader) ([]Span, error) {
	var spans []Span
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 1024*1024), maxStdouttraceLineSize)
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

		// Service name precedence: resource service.name, then the
		// synth.service attribute, then the instrumentation scope name —
		// matching the OTLP path. The scope name comes last because current
		// motel binaries emit a constant module-path scope name on every span.
		service := realServiceName(stringAttr(evt.Resource, serviceNameKey))
		if service == "" {
			service = stringAttr(evt.Attributes, synthServiceKey)
		}
		if service == "" {
			service = evt.InstrumentationScope.Name
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
		return nil, fmt.Errorf("no spans found in input")
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
			if attr.Key == serviceNameKey {
				serviceName = attr.Value.GetStringValue()
			}
		}
		serviceName = realServiceName(serviceName)

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
		return nil, fmt.Errorf("no spans found in input")
	}
	return spans, nil
}

// stringAttr returns the string value of the attribute with the given key,
// or empty if the key is absent or not a string.
func stringAttr(attrs []sdkAttr, key string) string {
	for _, attr := range attrs {
		if attr.Key == key {
			if s, ok := attr.Value.Value.(string); ok {
				return s
			}
		}
	}
	return ""
}

// realServiceName filters out the SDK's unknown_service placeholder so
// callers fall through to more specific sources of the service name.
func realServiceName(name string) string {
	if strings.HasPrefix(name, unknownServicePrefix) {
		return ""
	}
	return name
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

// isJaegerData returns true when raw is a JSON array whose first element contains
// both "spans" and within it "operationName" — the distinguishing marks of a
// Jaeger JSON export (also produced by Grafana Explore Tempo downloads).
func isJaegerData(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return false
	}
	var traces []struct {
		Spans []struct {
			OperationName *json.RawMessage `json:"operationName"`
		} `json:"spans"`
	}
	if err := json.Unmarshal(raw, &traces); err != nil || len(traces) == 0 {
		return false
	}
	if len(traces[0].Spans) == 0 {
		return false
	}
	return traces[0].Spans[0].OperationName != nil
}

// jaegerExport is the top-level structure of a Jaeger JSON export.
type jaegerExport struct {
	Data []jaegerTrace `json:"data"`
}

type jaegerTrace struct {
	Spans     []jaegerSpan              `json:"spans"`
	Processes map[string]*jaegerProcess `json:"processes"`
}

type jaegerSpan struct {
	TraceID       string         `json:"traceID"`
	SpanID        string         `json:"spanID"`
	OperationName string         `json:"operationName"`
	References    []jaegerRef    `json:"references"`
	StartTime     int64          `json:"startTime"` // microseconds since epoch
	Duration      int64          `json:"duration"`  // microseconds
	Tags          []jaegerTag    `json:"tags"`
	ProcessID     string         `json:"processID"`
	Process       *jaegerProcess `json:"process"`
}

type jaegerRef struct {
	RefType string `json:"refType"`
	SpanID  string `json:"spanID"`
}

type jaegerProcess struct {
	ServiceName string `json:"serviceName"`
}

type jaegerTag struct {
	Key   string          `json:"key"`
	Value json.RawMessage `json:"value"`
}

func parseJaeger(data []byte) ([]Span, error) {
	var export jaegerExport
	if err := json.Unmarshal(data, &export); err != nil {
		return nil, fmt.Errorf("parsing Jaeger JSON: %w", err)
	}

	var spans []Span
	for _, trace := range export.Data {
		for _, js := range trace.Spans {
			service := jaegerService(js, trace.Processes)

			parentID := ""
			for _, ref := range js.References {
				if ref.RefType == "CHILD_OF" {
					parentID = ref.SpanID
					break
				}
			}

			startTime := time.UnixMicro(js.StartTime)
			endTime := time.UnixMicro(js.StartTime + js.Duration)

			attrs := make(map[string]string)
			isError := false
			for _, tag := range js.Tags {
				val := jaegerTagString(tag.Value)
				if tag.Key == "error" && val == "true" {
					isError = true
				}
				attrs[tag.Key] = val
			}

			spans = append(spans, Span{
				TraceID:    js.TraceID,
				SpanID:     js.SpanID,
				ParentID:   parentID,
				Service:    service,
				Operation:  js.OperationName,
				StartTime:  startTime,
				EndTime:    endTime,
				IsError:    isError,
				Attributes: attrs,
			})
		}
	}

	if len(spans) == 0 {
		return nil, fmt.Errorf("no spans found in input")
	}
	return spans, nil
}

// jaegerService resolves the service name: prefers the span's inline process
// field, falls back to the trace-level processes map.
func jaegerService(js jaegerSpan, processes map[string]*jaegerProcess) string {
	if js.Process != nil && js.Process.ServiceName != "" {
		return js.Process.ServiceName
	}
	if js.ProcessID != "" && processes != nil {
		if p := processes[js.ProcessID]; p != nil {
			return p.ServiceName
		}
	}
	return ""
}

// jaegerTagString converts a raw Jaeger tag JSON value to a string.
// Strings are unquoted; other JSON scalars (numbers, booleans) use their
// literal representation so callers can match against "true"/"false"/etc.
func jaegerTagString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	return string(raw)
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
