// Full pipeline tests for trace-to-config import
// Tests end-to-end from raw input to validated YAML output
package traceimport

import (
	"bytes"
	"compress/gzip"
	"os"
	"strings"
	"testing"

	"github.com/andrewh/motel/pkg/synth"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestImport_StdouttraceFixture(t *testing.T) {
	const (
		expectedTraceCount = 55
		expectedSpanCount  = 200
	)

	f, err := os.Open("testdata/basic-topology-stdout.jsonl")
	require.NoError(t, err)
	defer f.Close() //nolint:errcheck // test cleanup

	var warnings bytes.Buffer
	result, err := Import(f, Options{
		Format:   FormatStdouttrace,
		Warnings: &warnings,
	})
	require.NoError(t, err)
	assert.Equal(t, expectedTraceCount, result.TraceCount)
	assert.Equal(t, expectedSpanCount, result.SpanCount)

	yaml := string(result.YAML)

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
	const (
		expectedTraceCount = 1
		expectedSpanCount  = 2
	)

	f, err := os.Open("testdata/single-trace-otlp.json")
	require.NoError(t, err)
	defer f.Close() //nolint:errcheck // test cleanup

	var warnings bytes.Buffer
	result, err := Import(f, Options{
		Format:   FormatOTLP,
		Warnings: &warnings,
	})
	require.NoError(t, err)
	assert.Equal(t, expectedTraceCount, result.TraceCount)
	assert.Equal(t, expectedSpanCount, result.SpanCount)

	yaml := string(result.YAML)
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
	result, err := Import(f, Options{
		Format:   FormatAuto,
		Warnings: &warnings,
	})
	require.NoError(t, err)
	assert.Contains(t, string(result.YAML), "gateway:")
}

func TestImport_MinimalSingleSpan(t *testing.T) {
	const (
		expectedTraceCount = 1
		expectedSpanCount  = 1
	)

	line := `{"Name":"ping","SpanContext":{"TraceID":"aaa","SpanID":"bbb"},"Parent":{"TraceID":"aaa","SpanID":"0000000000000000"},"StartTime":"2024-01-01T00:00:00Z","EndTime":"2024-01-01T00:00:00.010Z","Attributes":[],"Status":{"Code":"Unset"},"InstrumentationScope":{"Name":"health"}}`

	var warnings bytes.Buffer
	result, err := Import(strings.NewReader(line), Options{
		Format:   FormatStdouttrace,
		Warnings: &warnings,
	})
	require.NoError(t, err)
	assert.Equal(t, expectedTraceCount, result.TraceCount)
	assert.Equal(t, expectedSpanCount, result.SpanCount)

	yaml := string(result.YAML)
	assert.Contains(t, yaml, "health:")
	assert.Contains(t, yaml, "ping:")
	assert.Contains(t, yaml, "10ms")
}

// TestImport_SelfNestedSpansRoundTrip imports a trace with a span nested inside
// another span of the same (service, operation) — the shape produced by nested
// DB transactions or HTTP clients that wrap their request span. Import must
// succeed: the round-trip check now runs full topology construction (cycle
// detection included), and the emitted YAML must not contain a self edge.
func TestImport_SelfNestedSpansRoundTrip(t *testing.T) {
	lines := strings.Join([]string{
		`{"Name":"work","SpanContext":{"TraceID":"t1","SpanID":"s1"},"Parent":{"TraceID":"t1","SpanID":"0000000000000000"},"StartTime":"2024-01-01T00:00:00Z","EndTime":"2024-01-01T00:00:00.030Z","Attributes":[],"Status":{"Code":"Unset"},"InstrumentationScope":{"Name":"svc"}}`,
		`{"Name":"transaction","SpanContext":{"TraceID":"t1","SpanID":"s2"},"Parent":{"TraceID":"t1","SpanID":"s1"},"StartTime":"2024-01-01T00:00:00.001Z","EndTime":"2024-01-01T00:00:00.025Z","Attributes":[],"Status":{"Code":"Unset"},"InstrumentationScope":{"Name":"svc"}}`,
		`{"Name":"transaction","SpanContext":{"TraceID":"t1","SpanID":"s3"},"Parent":{"TraceID":"t1","SpanID":"s2"},"StartTime":"2024-01-01T00:00:00.002Z","EndTime":"2024-01-01T00:00:00.020Z","Attributes":[],"Status":{"Code":"Unset"},"InstrumentationScope":{"Name":"svc"}}`,
		`{"Name":"db-write","SpanContext":{"TraceID":"t1","SpanID":"s4"},"Parent":{"TraceID":"t1","SpanID":"s3"},"StartTime":"2024-01-01T00:00:00.003Z","EndTime":"2024-01-01T00:00:00.010Z","Attributes":[],"Status":{"Code":"Unset"},"InstrumentationScope":{"Name":"svc"}}`,
	}, "\n")

	var warnings bytes.Buffer
	result, err := Import(strings.NewReader(lines), Options{
		Format:   FormatStdouttrace,
		Warnings: &warnings,
	})
	require.NoError(t, err)

	// Import succeeding at all proves the round-trip (now including BuildTopology
	// and its cycle detection) accepted the result. Confirm the legitimate edges
	// survive while the transaction operation gains no self edge.
	cfg, err := synth.ParseConfig(result.YAML)
	require.NoError(t, err)
	var tx *synth.OperationConfig
	for i := range cfg.Services[0].Operations {
		if cfg.Services[0].Operations[i].Name == "transaction" {
			tx = &cfg.Services[0].Operations[i]
		}
	}
	require.NotNil(t, tx)
	for _, call := range tx.Calls {
		assert.NotEqual(t, "svc.transaction", call.Target, "self-referential edge must not be emitted")
	}
}

func TestImport_WithErrors(t *testing.T) {
	const (
		expectedTraceCount = 2
		expectedSpanCount  = 2
	)

	lines := strings.Join([]string{
		`{"Name":"op","SpanContext":{"TraceID":"t1","SpanID":"s1"},"Parent":{"TraceID":"t1","SpanID":"0000000000000000"},"StartTime":"2024-01-01T00:00:00Z","EndTime":"2024-01-01T00:00:00.010Z","Attributes":[],"Status":{"Code":"Error"},"InstrumentationScope":{"Name":"svc"}}`,
		`{"Name":"op","SpanContext":{"TraceID":"t2","SpanID":"s2"},"Parent":{"TraceID":"t2","SpanID":"0000000000000000"},"StartTime":"2024-01-01T00:00:01Z","EndTime":"2024-01-01T00:00:01.010Z","Attributes":[],"Status":{"Code":"Unset"},"InstrumentationScope":{"Name":"svc"}}`,
	}, "\n")

	var warnings bytes.Buffer
	result, err := Import(strings.NewReader(lines), Options{
		Format:   FormatStdouttrace,
		Warnings: &warnings,
	})
	require.NoError(t, err)
	assert.Equal(t, expectedTraceCount, result.TraceCount)
	assert.Equal(t, expectedSpanCount, result.SpanCount)
	assert.Contains(t, string(result.YAML), "error_rate: 50%")
}

func TestImport_EmptyInput(t *testing.T) {
	_, err := Import(strings.NewReader(""), Options{Format: FormatStdouttrace})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no spans found")
}

func TestValidateRoundTrip_ValidYAML(t *testing.T) {
	yaml := []byte(`version: 1
services:
  svc:
    operations:
      op:
        duration: 10ms
traffic:
  rate: 10/s
`)
	err := validateRoundTrip(yaml)
	require.NoError(t, err)
}

func TestValidateRoundTrip_InvalidYAML(t *testing.T) {
	err := validateRoundTrip([]byte(`not: valid: yaml: [[[`))
	require.Error(t, err)
}

func TestValidateRoundTrip_BadConfig(t *testing.T) {
	yaml := []byte(`version: 1
services:
  svc:
    operations:
      op:
        duration: 10ms
        calls:
          - nonexistent.op
traffic:
  rate: 10/s
`)
	err := validateRoundTrip(yaml)
	require.Error(t, err)
}

func TestImport_MinTracesWarning(t *testing.T) {
	line := `{"Name":"op","SpanContext":{"TraceID":"t1","SpanID":"s1"},"Parent":{"TraceID":"t1","SpanID":"0000000000000000"},"StartTime":"2024-01-01T00:00:00Z","EndTime":"2024-01-01T00:00:00.010Z","Attributes":[],"Status":{"Code":"Unset"},"InstrumentationScope":{"Name":"svc"}}`

	var warnings bytes.Buffer
	_, err := Import(strings.NewReader(line), Options{
		Format:    FormatStdouttrace,
		MinTraces: 5,
		Warnings:  &warnings,
	})
	require.NoError(t, err)
	assert.Contains(t, warnings.String(), "only 1 trace")
	assert.Contains(t, warnings.String(), "requested minimum: 5")
}

func TestImport_MetaSummaryGzipProfileFilter(t *testing.T) {
	const (
		expectedTraceCount = 11
		expectedSpanCount  = 32
	)

	csvData := strings.Join([]string{
		"parent_name,children_set,num_calls,num_returning_calls,concurrency_rate,profile",
		`root,"{'childA', 'childB'}",10.0,8.0,1,ads`,
		`root,"{'childA'}",1,1,0,ads`,
		`root,"{'fetchOnly'}",1,1,0,fetch`,
		"leaf,set(),0,0,0,ads",
	}, "\n")

	var compressed bytes.Buffer
	gz := gzip.NewWriter(&compressed)
	_, err := gz.Write([]byte(csvData))
	require.NoError(t, err)
	require.NoError(t, gz.Close())

	var warnings bytes.Buffer
	result, err := Import(&compressed, Options{
		Format:      FormatMetaSummary,
		MetaProfile: "ads",
		Warnings:    &warnings,
	})
	require.NoError(t, err)
	assert.Equal(t, expectedTraceCount, result.TraceCount)
	assert.Equal(t, expectedSpanCount, result.SpanCount)

	yaml := string(result.YAML)
	assert.Contains(t, yaml, metaServiceName("root")+":")
	assert.Contains(t, yaml, metaServiceName("childA")+".invoke")
	assert.Contains(t, yaml, metaServiceName("childB")+".invoke")
	assert.Contains(t, yaml, "probability: 0.91")
	assert.Contains(t, yaml, "error_rate: 18%")
	assert.Contains(t, yaml, "meta.ingress_id: root")
	assert.Contains(t, yaml, "meta.ingress_id: childA")
	assert.NotContains(t, yaml, metaServiceName("fetchOnly"))
	assert.NotContains(t, yaml, metaServiceName("leaf"))
}

func TestImport_MetaSummarySequentialCallStyle(t *testing.T) {
	const (
		expectedTraceCount = 1
		expectedSpanCount  = 3
	)

	csvData := strings.Join([]string{
		"parent_name,children_set,num_calls,num_returning_calls,concurrency_rate,profile",
		`root,"{'childA', 'childB'}",1,1,0,ads`,
	}, "\n")

	var warnings bytes.Buffer
	result, err := Import(strings.NewReader(csvData), Options{
		Format:   FormatMetaSummary,
		Warnings: &warnings,
	})
	require.NoError(t, err)
	assert.Equal(t, expectedTraceCount, result.TraceCount)
	assert.Equal(t, expectedSpanCount, result.SpanCount)

	yaml := string(result.YAML)
	assert.Contains(t, yaml, "call_style: sequential")
	assert.Contains(t, warnings.String(), "only 1 weighted Meta parent sample")
}

func TestImport_MetaSummaryPreservesDistinctIngressIDs(t *testing.T) {
	const (
		expectedTraceCount = 2
		expectedSpanCount  = 4
	)

	csvData := strings.Join([]string{
		"parent_name,children_set,num_calls,num_returning_calls,concurrency_rate,profile",
		`Service.A,"{'leaf'}",1,1,0,ads`,
		`service-a,"{'leaf'}",1,1,0,ads`,
	}, "\n")

	result, err := Import(strings.NewReader(csvData), Options{
		Format: FormatMetaSummary,
	})
	require.NoError(t, err)
	assert.Equal(t, expectedTraceCount, result.TraceCount)
	assert.Equal(t, expectedSpanCount, result.SpanCount)

	firstName := metaServiceName("Service.A")
	secondName := metaServiceName("service-a")
	require.NotEqual(t, firstName, secondName)

	yaml := string(result.YAML)
	assert.Contains(t, yaml, firstName+":")
	assert.Contains(t, yaml, secondName+":")
	assert.Contains(t, yaml, "meta.ingress_id: Service.A")
	assert.Contains(t, yaml, "meta.ingress_id: service-a")
}

func TestMetaServiceNameUsesStableCollisionResistantSuffix(t *testing.T) {
	firstName := metaServiceName("Service.A")
	secondName := metaServiceName("service-a")

	assert.NotEqual(t, firstName, secondName)
	assert.Regexp(t, `^meta-service-a-[0-9a-f]{16}$`, firstName)
	assert.Regexp(t, `^meta-service-a-[0-9a-f]{16}$`, secondName)
	assert.Equal(t, firstName, metaServiceName("Service.A"))
}
