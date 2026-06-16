// Unit tests for span parsing across supported trace import formats
// Covers format detection, field extraction, and error handling
package traceimport

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDetectFormat_Stdouttrace(t *testing.T) {
	input := `{"Name":"op","SpanContext":{"TraceID":"abc","SpanID":"def"},"Parent":{"TraceID":"abc","SpanID":"000"},"StartTime":"2024-01-01T00:00:00Z","EndTime":"2024-01-01T00:00:01Z","Attributes":[],"Status":{"Code":"Unset"},"InstrumentationScope":{"Name":"svc"}}`
	format, err := detectFormat([]byte(input))
	require.NoError(t, err)
	assert.Equal(t, FormatStdouttrace, format)
}

func TestDetectFormat_OTLP(t *testing.T) {
	input := `{"resourceSpans":[{"resource":{},"scopeSpans":[]}]}`
	format, err := detectFormat([]byte(input))
	require.NoError(t, err)
	assert.Equal(t, FormatOTLP, format)
}

func TestDetectFormat_Unknown(t *testing.T) {
	input := `{"something":"else"}`
	_, err := detectFormat([]byte(input))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot detect format")
}

func TestDetectFormat_InvalidJSON(t *testing.T) {
	_, err := detectFormat([]byte(`not json`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot detect format")
}

func TestParseStdouttrace_Basic(t *testing.T) {
	line := `{"Name":"query","SpanContext":{"TraceID":"aaa","SpanID":"bbb"},"Parent":{"TraceID":"aaa","SpanID":"0000000000000000"},"StartTime":"2024-01-01T00:00:00Z","EndTime":"2024-01-01T00:00:00.005Z","Attributes":[{"Key":"synth.service","Value":{"Type":"STRING","Value":"postgres"}},{"Key":"db.system","Value":{"Type":"STRING","Value":"postgresql"}}],"Status":{"Code":"Unset"},"InstrumentationScope":{"Name":"postgres"}}`

	spans, err := ParseSpans(strings.NewReader(line), FormatStdouttrace)
	require.NoError(t, err)
	require.Len(t, spans, 1)

	s := spans[0]
	assert.Equal(t, "aaa", s.TraceID)
	assert.Equal(t, "bbb", s.SpanID)
	assert.Empty(t, s.ParentID, "all-zeros parent should be empty")
	assert.Equal(t, "postgres", s.Service)
	assert.Equal(t, "query", s.Operation)
	assert.False(t, s.IsError)
	assert.Equal(t, "postgresql", s.Attributes["db.system"])
	assert.NotContains(t, s.Attributes, "synth.service", "engine-internal attrs should be excluded")
}

func TestParseStdouttrace_Error(t *testing.T) {
	line := `{"Name":"fail","SpanContext":{"TraceID":"aaa","SpanID":"ccc"},"Parent":{"TraceID":"aaa","SpanID":"bbb"},"StartTime":"2024-01-01T00:00:00Z","EndTime":"2024-01-01T00:00:00.005Z","Attributes":[],"Status":{"Code":"Error"},"InstrumentationScope":{"Name":"svc"}}`

	spans, err := ParseSpans(strings.NewReader(line), FormatStdouttrace)
	require.NoError(t, err)
	require.Len(t, spans, 1)
	assert.True(t, spans[0].IsError)
	assert.Equal(t, "bbb", spans[0].ParentID)
}

func TestParseStdouttrace_EmptyInput(t *testing.T) {
	_, err := ParseSpans(strings.NewReader(""), FormatStdouttrace)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no spans found")
}

func TestParseOTLP_Basic(t *testing.T) {
	// Base64 "AQIDBAUGBwgJCgsMDQ4PEA==" decodes to bytes [1..16], hex = "0102030405060708090a0b0c0d0e0f10"
	input := `{
		"resourceSpans": [{
			"resource": {"attributes": [{"key": "service.name", "value": {"stringValue": "api"}}]},
			"scopeSpans": [{"scope": {"name": "api"}, "spans": [{
				"traceId": "AQIDBAUGBwgJCgsMDQ4PEA==",
				"spanId": "AQIDBAUGBwg=",
				"name": "GET /users",
				"startTimeUnixNano": "1700000000000000000",
				"endTimeUnixNano": "1700000000030000000",
				"status": {},
				"attributes": [{"key": "http.method", "value": {"stringValue": "GET"}}]
			}]}]
		}]
	}`

	spans, err := ParseSpans(strings.NewReader(input), FormatOTLP)
	require.NoError(t, err)
	require.Len(t, spans, 1)

	s := spans[0]
	assert.Equal(t, "0102030405060708090a0b0c0d0e0f10", s.TraceID)
	assert.Equal(t, "0102030405060708", s.SpanID)
	assert.Empty(t, s.ParentID)
	assert.Equal(t, "api", s.Service)
	assert.Equal(t, "GET /users", s.Operation)
	assert.False(t, s.IsError)
	assert.Equal(t, "GET", s.Attributes["http.method"])
}

func TestParseOTLP_Error(t *testing.T) {
	input := `{
		"resourceSpans": [{
			"resource": {"attributes": [{"key": "service.name", "value": {"stringValue": "api"}}]},
			"scopeSpans": [{"scope": {"name": "api"}, "spans": [{
				"traceId": "AQIDBAUGBwgJCgsMDQ4PEA==",
				"spanId": "AQIDBAUGBwg=",
				"name": "fail",
				"startTimeUnixNano": "1700000000000000000",
				"endTimeUnixNano": "1700000000030000000",
				"status": {"code": 2},
				"attributes": []
			}]}]
		}]
	}`

	spans, err := ParseSpans(strings.NewReader(input), FormatOTLP)
	require.NoError(t, err)
	require.Len(t, spans, 1)
	assert.True(t, spans[0].IsError)
}

func TestParseOTLP_AttributeValueTypes(t *testing.T) {
	input := `{
		"resourceSpans": [{
			"resource": {"attributes": [{"key": "service.name", "value": {"stringValue": "api"}}]},
			"scopeSpans": [{"scope": {"name": "api"}, "spans": [{
				"traceId": "AQIDBAUGBwgJCgsMDQ4PEA==",
				"spanId": "AQIDBAUGBwg=",
				"name": "GET /users",
				"startTimeUnixNano": "1700000000000000000",
				"endTimeUnixNano": "1700000000030000000",
				"status": {},
				"attributes": [
					{"key": "http.method", "value": {"stringValue": "GET"}},
					{"key": "http.status_code", "value": {"intValue": "200"}},
					{"key": "http.resent", "value": {"boolValue": true}},
					{"key": "sampling.priority", "value": {"doubleValue": 0.5}}
				]
			}]}]
		}]
	}`

	spans, err := ParseSpans(strings.NewReader(input), FormatOTLP)
	require.NoError(t, err)
	require.Len(t, spans, 1)

	attrs := spans[0].Attributes
	assert.Equal(t, "GET", attrs["http.method"])
	assert.Equal(t, "200", attrs["http.status_code"])
	assert.Equal(t, "true", attrs["http.resent"])
	assert.Equal(t, "0.5", attrs["sampling.priority"])
}

func TestParseOTLP_StatusCodeAsEnumName(t *testing.T) {
	input := `{
		"resourceSpans": [{
			"resource": {"attributes": [{"key": "service.name", "value": {"stringValue": "api"}}]},
			"scopeSpans": [{"scope": {"name": "api"}, "spans": [{
				"traceId": "AQIDBAUGBwgJCgsMDQ4PEA==",
				"spanId": "AQIDBAUGBwg=",
				"name": "fail",
				"startTimeUnixNano": "1700000000000000000",
				"endTimeUnixNano": "1700000000030000000",
				"status": {"code": "STATUS_CODE_ERROR"},
				"attributes": []
			}]}]
		}]
	}`

	spans, err := ParseSpans(strings.NewReader(input), FormatOTLP)
	require.NoError(t, err)
	require.Len(t, spans, 1)
	assert.True(t, spans[0].IsError)
}

func TestParseSpans_AutoDetect(t *testing.T) {
	stdouttrace := `{"Name":"op","SpanContext":{"TraceID":"aaa","SpanID":"bbb"},"Parent":{"TraceID":"aaa","SpanID":"0000000000000000"},"StartTime":"2024-01-01T00:00:00Z","EndTime":"2024-01-01T00:00:01Z","Attributes":[],"Status":{"Code":"Unset"},"InstrumentationScope":{"Name":"svc"}}`

	spans, err := ParseSpans(strings.NewReader(stdouttrace), FormatAuto)
	require.NoError(t, err)
	require.Len(t, spans, 1)
	assert.Equal(t, "op", spans[0].Operation)
}

func TestParseSpans_AutoDetectSingleLineJSONRespectsInputLimit(t *testing.T) {
	input := `{"resourceSpans":[]}` + strings.Repeat(" ", 32)
	_, err := parseAutoSpans(strings.NewReader(input), len(`{"resourceSpans":[]}`)-1)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "input exceeds maximum size")
}

func TestDetectFormat_PrettyPrintedOTLP(t *testing.T) {
	input := "{\n  \"resourceSpans\": []\n}"
	format, err := detectFormat([]byte(input))
	require.NoError(t, err)
	assert.Equal(t, FormatOTLP, format)
}

func TestDetectFormat_PrettyPrintedStdouttrace(t *testing.T) {
	input := "{\n  \"SpanContext\": {\"TraceID\": \"abc\"}\n}"
	format, err := detectFormat([]byte(input))
	require.NoError(t, err)
	assert.Equal(t, FormatStdouttrace, format)
}

func TestDetectFormat_MultilineUnknown(t *testing.T) {
	input := "{\n  \"other\": true\n}"
	_, err := detectFormat([]byte(input))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot detect format")
}

func TestDetectFormat_MultilineInvalidJSON(t *testing.T) {
	input := "not json\nbut has multiple lines"
	_, err := detectFormat([]byte(input))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot detect format")
}

func TestParseSpans_UnknownFormat(t *testing.T) {
	_, err := ParseSpans(strings.NewReader("{}"), "badformat")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown format")
}

func TestParseStdouttrace_ServiceFallback(t *testing.T) {
	line := `{"Name":"op","SpanContext":{"TraceID":"t1","SpanID":"s1"},"Parent":{"TraceID":"t1","SpanID":"0000000000000000"},"StartTime":"2024-01-01T00:00:00Z","EndTime":"2024-01-01T00:00:01Z","Attributes":[{"Key":"synth.service","Value":{"Type":"STRING","Value":"fallback-svc"}}],"Status":{"Code":"Unset"},"InstrumentationScope":{"Name":""}}`

	spans, err := ParseSpans(strings.NewReader(line), FormatStdouttrace)
	require.NoError(t, err)
	require.Len(t, spans, 1)
	assert.Equal(t, "fallback-svc", spans[0].Service)
}

func TestParseStdouttrace_ServiceNamePrecedence(t *testing.T) {
	tests := []struct {
		name string
		line string
		want string
	}{
		{
			// Current binaries: per-service resource, constant module-path scope name.
			name: "resource service.name wins over scope name",
			line: `{"Name":"op","SpanContext":{"TraceID":"t1","SpanID":"s1"},"Parent":{"TraceID":"t1","SpanID":"0000000000000000"},"StartTime":"2024-01-01T00:00:00Z","EndTime":"2024-01-01T00:00:01Z","Attributes":[{"Key":"synth.service","Value":{"Type":"STRING","Value":"checkout"}}],"Resource":[{"Key":"service.name","Value":{"Type":"STRING","Value":"checkout"}}],"Status":{"Code":"Unset"},"InstrumentationScope":{"Name":"github.com/andrewh/motel"}}`,
			want: "checkout",
		},
		{
			// Older binaries: placeholder resource, synth.service attribute set.
			name: "unknown_service placeholder falls back to synth.service",
			line: `{"Name":"op","SpanContext":{"TraceID":"t1","SpanID":"s1"},"Parent":{"TraceID":"t1","SpanID":"0000000000000000"},"StartTime":"2024-01-01T00:00:00Z","EndTime":"2024-01-01T00:00:01Z","Attributes":[{"Key":"synth.service","Value":{"Type":"STRING","Value":"checkout"}}],"Resource":[{"Key":"service.name","Value":{"Type":"STRING","Value":"unknown_service:motel-synth"}}],"Status":{"Code":"Unset"},"InstrumentationScope":{"Name":"checkout-scope"}}`,
			want: "checkout",
		},
		{
			name: "unknown_service placeholder without synth.service falls back to scope name",
			line: `{"Name":"op","SpanContext":{"TraceID":"t1","SpanID":"s1"},"Parent":{"TraceID":"t1","SpanID":"0000000000000000"},"StartTime":"2024-01-01T00:00:00Z","EndTime":"2024-01-01T00:00:01Z","Attributes":[],"Resource":[{"Key":"service.name","Value":{"Type":"STRING","Value":"unknown_service:app"}}],"Status":{"Code":"Unset"},"InstrumentationScope":{"Name":"checkout"}}`,
			want: "checkout",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spans, err := ParseSpans(strings.NewReader(tt.line), FormatStdouttrace)
			require.NoError(t, err)
			require.Len(t, spans, 1)
			assert.Equal(t, tt.want, spans[0].Service)
		})
	}
}

func TestParseStdouttrace_MultipleSpans(t *testing.T) {
	lines := strings.Join([]string{
		`{"Name":"root","SpanContext":{"TraceID":"t1","SpanID":"s1"},"Parent":{"TraceID":"t1","SpanID":"0000000000000000"},"StartTime":"2024-01-01T00:00:00Z","EndTime":"2024-01-01T00:00:01Z","Attributes":[],"Status":{"Code":"Unset"},"InstrumentationScope":{"Name":"svc"}}`,
		"",
		`{"Name":"child","SpanContext":{"TraceID":"t1","SpanID":"s2"},"Parent":{"TraceID":"t1","SpanID":"s1"},"StartTime":"2024-01-01T00:00:00Z","EndTime":"2024-01-01T00:00:01Z","Attributes":[],"Status":{"Code":"Unset"},"InstrumentationScope":{"Name":"svc"}}`,
	}, "\n")

	spans, err := ParseSpans(strings.NewReader(lines), FormatStdouttrace)
	require.NoError(t, err)
	require.Len(t, spans, 2)
}

func TestParseStdouttrace_InvalidJSON(t *testing.T) {
	_, err := ParseSpans(strings.NewReader("not json"), FormatStdouttrace)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "line 1")
}

func TestParseStdouttrace_BlankLinesOnly(t *testing.T) {
	_, err := ParseSpans(strings.NewReader("\n\n\n"), FormatStdouttrace)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no spans found")
}

func TestDetectFormat_Jaeger(t *testing.T) {
	input := `{"data":[{"traceID":"abc","spans":[{"traceID":"abc","spanID":"def","operationName":"op","references":[],"startTime":1700000000000000,"duration":30000,"tags":[],"processID":"p1","process":{"serviceName":"api"}}],"processes":{"p1":{"serviceName":"api"}}}]}`
	format, err := detectFormat([]byte(input))
	require.NoError(t, err)
	assert.Equal(t, FormatJaeger, format)
}

func TestDetectFormat_PrettyPrintedJaeger(t *testing.T) {
	input := "{\n  \"data\": [{\"spans\":[{\"operationName\":\"op\"}]}]\n}"
	format, err := detectFormat([]byte(input))
	require.NoError(t, err)
	assert.Equal(t, FormatJaeger, format)
}

func TestParseJaeger_Basic(t *testing.T) {
	input := `{
		"data": [{
			"traceID": "abc123",
			"spans": [{
				"traceID": "abc123",
				"spanID": "def456",
				"operationName": "HTTP GET /users",
				"references": [],
				"startTime": 1700000000000000,
				"duration": 30000,
				"tags": [{"key": "http.method", "value": "GET"}],
				"processID": "p1",
				"process": {"serviceName": "api-gateway"}
			}],
			"processes": {"p1": {"serviceName": "api-gateway"}}
		}]
	}`

	spans, err := ParseSpans(strings.NewReader(input), FormatJaeger)
	require.NoError(t, err)
	require.Len(t, spans, 1)

	s := spans[0]
	assert.Equal(t, "abc123", s.TraceID)
	assert.Equal(t, "def456", s.SpanID)
	assert.Empty(t, s.ParentID)
	assert.Equal(t, "api-gateway", s.Service)
	assert.Equal(t, "HTTP GET /users", s.Operation)
	assert.False(t, s.IsError)
	assert.Equal(t, "GET", s.Attributes["http.method"])
	// startTime 1700000000000000 µs = 1700000000 s
	assert.Equal(t, int64(1700000000), s.StartTime.Unix())
	assert.Equal(t, int64(1700000000000000+30000), s.EndTime.UnixMicro())
}

func TestParseJaeger_ParentRef(t *testing.T) {
	input := `{
		"data": [{
			"spans": [
				{
					"traceID": "t1", "spanID": "root",
					"operationName": "root-op",
					"references": [],
					"startTime": 1700000000000000, "duration": 50000,
					"tags": [],
					"process": {"serviceName": "frontend"}
				},
				{
					"traceID": "t1", "spanID": "child",
					"operationName": "child-op",
					"references": [{"refType": "CHILD_OF", "traceID": "t1", "spanID": "root"}],
					"startTime": 1700000000010000, "duration": 20000,
					"tags": [],
					"process": {"serviceName": "backend"}
				}
			],
			"processes": {}
		}]
	}`

	spans, err := ParseSpans(strings.NewReader(input), FormatJaeger)
	require.NoError(t, err)
	require.Len(t, spans, 2)

	root := spans[0]
	child := spans[1]
	assert.Empty(t, root.ParentID)
	assert.Equal(t, "root", child.ParentID)
	assert.Equal(t, "frontend", root.Service)
	assert.Equal(t, "backend", child.Service)
}

func TestParseJaeger_ErrorTag(t *testing.T) {
	input := `{
		"data": [{
			"spans": [{
				"traceID": "t1", "spanID": "s1",
				"operationName": "fail",
				"references": [],
				"startTime": 1700000000000000, "duration": 5000,
				"tags": [{"key": "error", "value": true}],
				"process": {"serviceName": "svc"}
			}],
			"processes": {}
		}]
	}`

	spans, err := ParseSpans(strings.NewReader(input), FormatJaeger)
	require.NoError(t, err)
	require.Len(t, spans, 1)
	assert.True(t, spans[0].IsError)
}

func TestParseJaeger_ServiceFromProcessesMap(t *testing.T) {
	// No inline process field; service resolved from processes map via processID.
	input := `{
		"data": [{
			"spans": [{
				"traceID": "t1", "spanID": "s1",
				"operationName": "op",
				"references": [],
				"startTime": 1700000000000000, "duration": 1000,
				"tags": [],
				"processID": "p2"
			}],
			"processes": {
				"p1": {"serviceName": "other"},
				"p2": {"serviceName": "target-service"}
			}
		}]
	}`

	spans, err := ParseSpans(strings.NewReader(input), FormatJaeger)
	require.NoError(t, err)
	require.Len(t, spans, 1)
	assert.Equal(t, "target-service", spans[0].Service)
}

func TestParseJaeger_MultipleTraces(t *testing.T) {
	input := `{
		"data": [
			{
				"spans": [{"traceID":"t1","spanID":"s1","operationName":"op1","references":[],"startTime":1700000000000000,"duration":1000,"tags":[],"process":{"serviceName":"svc"}}],
				"processes": {}
			},
			{
				"spans": [{"traceID":"t2","spanID":"s2","operationName":"op2","references":[],"startTime":1700000001000000,"duration":1000,"tags":[],"process":{"serviceName":"svc"}}],
				"processes": {}
			}
		]
	}`

	spans, err := ParseSpans(strings.NewReader(input), FormatJaeger)
	require.NoError(t, err)
	require.Len(t, spans, 2)
	assert.Equal(t, "t1", spans[0].TraceID)
	assert.Equal(t, "t2", spans[1].TraceID)
}

func TestParseJaeger_EmptyData(t *testing.T) {
	_, err := ParseSpans(strings.NewReader(`{"data":[]}`), FormatJaeger)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no spans found")
}

func TestParseJaeger_AutoDetect(t *testing.T) {
	input := `{"data":[{"spans":[{"traceID":"t1","spanID":"s1","operationName":"op","references":[],"startTime":1700000000000000,"duration":1000,"tags":[],"process":{"serviceName":"svc"}}],"processes":{}}]}`
	spans, err := ParseSpans(strings.NewReader(input), FormatAuto)
	require.NoError(t, err)
	require.Len(t, spans, 1)
	assert.Equal(t, "op", spans[0].Operation)
}

func TestIsZeroID(t *testing.T) {
	assert.True(t, isZeroID("0000000000000000"))
	assert.True(t, isZeroID("00"))
	assert.False(t, isZeroID("0a00000000000000"))
	assert.False(t, isZeroID(""))
}
