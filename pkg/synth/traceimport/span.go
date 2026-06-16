// Normalised span type and format-specific parsers for trace inference
// Handles stdouttrace, OTLP protobuf JSON, and Jaeger JSON formats
package traceimport

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
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
const bytesPerMegabyte = 1024 * 1024

// ParseSpans reads spans from the given reader in the specified format.
// FormatAuto inspects the first JSON object to determine the format.
// Whole-document JSON inputs are limited to 256 MB to prevent OOM on large trace
// exports. Explicit line-delimited stdouttrace inputs are scanned incrementally.
func ParseSpans(r io.Reader, format Format) ([]Span, error) {
	switch format {
	case FormatStdouttrace:
		return parseStdouttraceReader(r)
	case FormatAuto:
		return parseAutoSpans(r, maxInputSize)
	case FormatOTLP:
		data, err := readLimitedInput(r, maxInputSize)
		if err != nil {
			return nil, err
		}
		return parseOTLP(data)
	case FormatJaeger:
		data, err := readLimitedInput(r, maxInputSize)
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

func parseAutoSpans(r io.Reader, maxSize int) ([]Span, error) {
	data, err := readLimitedInput(r, maxSize)
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

func readLimitedInput(r io.Reader, maxSize int) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, int64(maxSize)+1))
	if err != nil {
		return nil, fmt.Errorf("reading input: %w", err)
	}
	if len(data) > maxSize {
		return nil, fmt.Errorf("input exceeds maximum size of %s; stdouttrace and meta-summary inputs can be streamed with explicit --format", formatInputSize(maxSize))
	}
	data = bytes.TrimSpace(data)
	if len(data) == 0 {
		return nil, fmt.Errorf("no spans found in input")
	}
	return data, nil
}

func formatInputSize(size int) string {
	if size >= bytesPerMegabyte {
		return fmt.Sprintf("%d MB", size/bytesPerMegabyte)
	}
	return fmt.Sprintf("%d bytes", size)
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
		if isOTLPProbe(probe) {
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
			if isOTLPProbe(probe) {
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

	return "", fmt.Errorf("cannot detect format: input has neither SpanContext (stdouttrace), resourceSpans/batches (OTLP), nor data[].spans[].operationName (Jaeger/Tempo)")
}

func isOTLPProbe(probe map[string]json.RawMessage) bool {
	if _, ok := probe["resourceSpans"]; ok {
		return true
	}
	_, ok := probe["batches"]
	return ok
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
	var req otlpTraces
	if err := json.Unmarshal(data, &req); err != nil {
		return nil, fmt.Errorf("parsing OTLP: %w", err)
	}

	var spans []Span
	for _, rs := range req.ResourceSpans {
		// Extract service.name from resource attributes
		serviceName := ""
		for _, attr := range rs.Resource.Attributes {
			if attr.Key == serviceNameKey && attr.Value.StringValue != nil {
				serviceName = *attr.Value.StringValue
			}
		}
		serviceName = realServiceName(serviceName)

		for _, ss := range rs.ScopeSpans {
			scopeName := ss.Scope.Name

			for _, span := range ss.Spans {
				svc := serviceName
				if svc == "" {
					svc = scopeName
				}

				parentBytes := decodeOTLPID(span.ParentSpanID)
				parentID := hex.EncodeToString(parentBytes)
				if isZeroID(parentID) || len(parentBytes) == 0 {
					parentID = ""
				}

				attrs := make(map[string]string)
				for _, attr := range span.Attributes {
					if excludedAttributes[attr.Key] {
						continue
					}
					attrs[attr.Key] = attr.Value.asString()
				}

				spans = append(spans, Span{
					TraceID:    hex.EncodeToString(decodeOTLPID(span.TraceID)),
					SpanID:     hex.EncodeToString(decodeOTLPID(span.SpanID)),
					ParentID:   parentID,
					Service:    svc,
					Operation:  span.Name,
					StartTime:  time.Unix(0, int64(span.StartTimeUnixNano.uint64())), //nolint:gosec // nanosecond timestamps are always positive
					EndTime:    time.Unix(0, int64(span.EndTimeUnixNano.uint64())),   //nolint:gosec // nanosecond timestamps are always positive
					IsError:    span.Status.Code.isError(),
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

// OTLP/JSON wire types. OTLP import decodes the proto3 JSON mapping with
// encoding/json rather than the generated protobuf messages, so that it does
// not pull in the protobuf reflection runtime (which is large in a WASM build).
type otlpTraces struct {
	ResourceSpans []otlpResourceSpans `json:"resourceSpans"`
}

func (t *otlpTraces) UnmarshalJSON(data []byte) error {
	var wire struct {
		ResourceSpans []otlpResourceSpans `json:"resourceSpans"`
		Batches       []otlpResourceSpans `json:"batches"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	t.ResourceSpans = wire.ResourceSpans
	if t.ResourceSpans == nil {
		t.ResourceSpans = wire.Batches
	}
	return nil
}

type otlpResourceSpans struct {
	Resource   otlpResource     `json:"resource"`
	ScopeSpans []otlpScopeSpans `json:"scopeSpans"`
}

func (rs *otlpResourceSpans) UnmarshalJSON(data []byte) error {
	var wire struct {
		Resource                    otlpResource     `json:"resource"`
		ScopeSpans                  []otlpScopeSpans `json:"scopeSpans"`
		InstrumentationLibrarySpans []otlpScopeSpans `json:"instrumentationLibrarySpans"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	rs.Resource = wire.Resource
	rs.ScopeSpans = wire.ScopeSpans
	if rs.ScopeSpans == nil {
		rs.ScopeSpans = wire.InstrumentationLibrarySpans
	}
	return nil
}

type otlpResource struct {
	Attributes []otlpKeyValue `json:"attributes"`
}

type otlpScopeSpans struct {
	Scope otlpScope  `json:"scope"`
	Spans []otlpSpan `json:"spans"`
}

func (ss *otlpScopeSpans) UnmarshalJSON(data []byte) error {
	var wire struct {
		Scope                  *otlpScope `json:"scope"`
		InstrumentationLibrary *otlpScope `json:"instrumentationLibrary"`
		Spans                  []otlpSpan `json:"spans"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	if wire.Scope != nil {
		ss.Scope = *wire.Scope
	} else if wire.InstrumentationLibrary != nil {
		ss.Scope = *wire.InstrumentationLibrary
	} else {
		ss.Scope = otlpScope{}
	}
	ss.Spans = wire.Spans
	return nil
}

type otlpScope struct {
	Name string `json:"name"`
}

type otlpSpan struct {
	TraceID           string         `json:"traceId"`
	SpanID            string         `json:"spanId"`
	ParentSpanID      string         `json:"parentSpanId"`
	Name              string         `json:"name"`
	StartTimeUnixNano otlpInt        `json:"startTimeUnixNano"`
	EndTimeUnixNano   otlpInt        `json:"endTimeUnixNano"`
	Status            otlpStatus     `json:"status"`
	Attributes        []otlpKeyValue `json:"attributes"`
}

type otlpStatus struct {
	Code otlpStatusCode `json:"code"`
}

type otlpKeyValue struct {
	Key   string       `json:"key"`
	Value otlpAnyValue `json:"value"`
}

// otlpAnyValue mirrors the OTLP AnyValue. Only scalar variants feed topology
// inference; arrayValue and kvlistValue are intentionally not represented.
type otlpAnyValue struct {
	StringValue *string  `json:"stringValue"`
	IntValue    *otlpInt `json:"intValue"`
	BoolValue   *bool    `json:"boolValue"`
	DoubleValue *float64 `json:"doubleValue"`
}

// asString renders the AnyValue as the inference engine's string form. Scalar
// values match the previous protojson-based formatting.
func (v otlpAnyValue) asString() string {
	switch {
	case v.StringValue != nil:
		return *v.StringValue
	case v.IntValue != nil:
		return string(*v.IntValue)
	case v.BoolValue != nil:
		return strconv.FormatBool(*v.BoolValue)
	case v.DoubleValue != nil:
		return strconv.FormatFloat(*v.DoubleValue, 'g', -1, 64)
	default:
		return ""
	}
}

// otlpInt is a 64-bit integer that OTLP/JSON encodes as either a JSON number
// or, per the proto3 JSON mapping for 64-bit integers, a decimal string.
type otlpInt string

func (n *otlpInt) UnmarshalJSON(data []byte) error {
	*n = otlpInt(bytes.Trim(data, `"`))
	return nil
}

func (n otlpInt) uint64() uint64 {
	v, _ := strconv.ParseUint(string(n), 10, 64)
	return v
}

// otlpStatusCode accepts the OTLP status code as its proto3 JSON enum name
// ("STATUS_CODE_ERROR") or its numeric value (2).
type otlpStatusCode string

func (c *otlpStatusCode) UnmarshalJSON(data []byte) error {
	*c = otlpStatusCode(bytes.Trim(data, `"`))
	return nil
}

func (c otlpStatusCode) isError() bool {
	return c == "STATUS_CODE_ERROR" || c == "2"
}

// decodeOTLPID decodes an OTLP/JSON trace or span ID. proto3 JSON encodes bytes
// as base64; emitters vary on alphabet and padding, so all four forms are tried.
func decodeOTLPID(s string) []byte {
	if s == "" {
		return nil
	}
	for _, enc := range []*base64.Encoding{
		base64.StdEncoding, base64.RawStdEncoding,
		base64.URLEncoding, base64.RawURLEncoding,
	} {
		if b, err := enc.DecodeString(s); err == nil {
			return b
		}
	}
	return nil
}
