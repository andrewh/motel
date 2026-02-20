// Unit tests for span parsing across stdouttrace and OTLP formats
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

func TestParseSpans_AutoDetect(t *testing.T) {
	stdouttrace := `{"Name":"op","SpanContext":{"TraceID":"aaa","SpanID":"bbb"},"Parent":{"TraceID":"aaa","SpanID":"0000000000000000"},"StartTime":"2024-01-01T00:00:00Z","EndTime":"2024-01-01T00:00:01Z","Attributes":[],"Status":{"Code":"Unset"},"InstrumentationScope":{"Name":"svc"}}`

	spans, err := ParseSpans(strings.NewReader(stdouttrace), FormatAuto)
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
