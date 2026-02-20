// Full pipeline tests for trace-to-config import
// Tests end-to-end from raw input to validated YAML output
package traceimport

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestImport_StdouttraceFixture(t *testing.T) {
	f, err := os.Open("testdata/basic-topology-stdout.jsonl")
	require.NoError(t, err)
	defer f.Close() //nolint:errcheck // test cleanup

	var warnings bytes.Buffer
	yamlBytes, err := Import(f, Options{
		Format:   FormatStdouttrace,
		Warnings: &warnings,
	})
	require.NoError(t, err)

	yaml := string(yamlBytes)

	// Verify expected services are present
	assert.Contains(t, yaml, "gateway:")
	assert.Contains(t, yaml, "user-service:")
	assert.Contains(t, yaml, "order-service:")
	assert.Contains(t, yaml, "postgres:")
	assert.Contains(t, yaml, "redis:")

	// Verify expected operations
	assert.Contains(t, yaml, "GET /users:")
	assert.Contains(t, yaml, "POST /orders:")
	assert.Contains(t, yaml, "list:")
	assert.Contains(t, yaml, "create:")
	assert.Contains(t, yaml, "query:")
	assert.Contains(t, yaml, "get:")

	// Verify calls are present
	assert.Contains(t, yaml, "user-service.list")
	assert.Contains(t, yaml, "order-service.create")
	assert.Contains(t, yaml, "postgres.query")
	assert.Contains(t, yaml, "redis.get")

	// Verify service attributes
	assert.Contains(t, yaml, "deployment.environment: production")
	assert.Contains(t, yaml, "db.system: postgresql")
	assert.Contains(t, yaml, "db.system: redis")

	// Verify traffic rate is present
	assert.Contains(t, yaml, "rate:")

	// Verify round-trip validation passed (Infer does this internally)
}

func TestImport_OTLPFixture(t *testing.T) {
	f, err := os.Open("testdata/single-trace-otlp.json")
	require.NoError(t, err)
	defer f.Close() //nolint:errcheck // test cleanup

	var warnings bytes.Buffer
	yamlBytes, err := Import(f, Options{
		Format:   FormatOTLP,
		Warnings: &warnings,
	})
	require.NoError(t, err)

	yaml := string(yamlBytes)
	assert.Contains(t, yaml, "api-gateway:")
	assert.Contains(t, yaml, "user-service:")
	assert.Contains(t, yaml, "GET /users:")
	assert.Contains(t, yaml, "user-service.list")

	// Single trace should emit a warning
	assert.Contains(t, warnings.String(), "only 1 trace")
}

func TestImport_AutoDetect(t *testing.T) {
	f, err := os.Open("testdata/basic-topology-stdout.jsonl")
	require.NoError(t, err)
	defer f.Close() //nolint:errcheck // test cleanup

	var warnings bytes.Buffer
	yamlBytes, err := Import(f, Options{
		Format:   FormatAuto,
		Warnings: &warnings,
	})
	require.NoError(t, err)
	assert.Contains(t, string(yamlBytes), "gateway:")
}

func TestImport_MinimalSingleSpan(t *testing.T) {
	line := `{"Name":"ping","SpanContext":{"TraceID":"aaa","SpanID":"bbb"},"Parent":{"TraceID":"aaa","SpanID":"0000000000000000"},"StartTime":"2024-01-01T00:00:00Z","EndTime":"2024-01-01T00:00:00.010Z","Attributes":[],"Status":{"Code":"Unset"},"InstrumentationScope":{"Name":"health"}}`

	var warnings bytes.Buffer
	yamlBytes, err := Import(strings.NewReader(line), Options{
		Format:   FormatStdouttrace,
		Warnings: &warnings,
	})
	require.NoError(t, err)

	yaml := string(yamlBytes)
	assert.Contains(t, yaml, "health:")
	assert.Contains(t, yaml, "ping:")
	assert.Contains(t, yaml, "10ms")
}

func TestImport_WithErrors(t *testing.T) {
	lines := strings.Join([]string{
		`{"Name":"op","SpanContext":{"TraceID":"t1","SpanID":"s1"},"Parent":{"TraceID":"t1","SpanID":"0000000000000000"},"StartTime":"2024-01-01T00:00:00Z","EndTime":"2024-01-01T00:00:00.010Z","Attributes":[],"Status":{"Code":"Error"},"InstrumentationScope":{"Name":"svc"}}`,
		`{"Name":"op","SpanContext":{"TraceID":"t2","SpanID":"s2"},"Parent":{"TraceID":"t2","SpanID":"0000000000000000"},"StartTime":"2024-01-01T00:00:01Z","EndTime":"2024-01-01T00:00:01.010Z","Attributes":[],"Status":{"Code":"Unset"},"InstrumentationScope":{"Name":"svc"}}`,
	}, "\n")

	var warnings bytes.Buffer
	yamlBytes, err := Import(strings.NewReader(lines), Options{
		Format:   FormatStdouttrace,
		Warnings: &warnings,
	})
	require.NoError(t, err)
	assert.Contains(t, string(yamlBytes), "error_rate: 50%")
}

func TestImport_EmptyInput(t *testing.T) {
	_, err := Import(strings.NewReader(""), Options{Format: FormatStdouttrace})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no spans found")
}
