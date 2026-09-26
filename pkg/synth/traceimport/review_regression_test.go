package traceimport

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/andrewh/motel/pkg/synth"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestImportExcludesInfrastructureAttributes(t *testing.T) {
	keys := []string{"service.name", "telemetry.sdk.language", "telemetry.sdk.name", "telemetry.sdk.version", "synth.scenarios", "custom"}
	for _, format := range []Format{FormatStdouttrace, FormatOTLP, FormatJaeger} {
		t.Run(string(format), func(t *testing.T) {
			var attrs []any
			for _, key := range keys {
				switch format {
				case FormatStdouttrace:
					attrs = append(attrs, map[string]any{"Key": key, "Value": map[string]any{"Type": "STRING", "Value": "value"}})
				case FormatOTLP:
					attrs = append(attrs, map[string]any{"key": key, "value": map[string]any{"stringValue": "value"}})
				case FormatJaeger:
					attrs = append(attrs, map[string]any{"key": key, "type": "string", "value": "value"})
				}
			}
			data, err := json.Marshal(attrs)
			require.NoError(t, err)
			var input string
			switch format {
			case FormatStdouttrace:
				input = fmt.Sprintf(`{"Name":"handle","SpanContext":{"TraceID":"1","SpanID":"1"},"StartTime":"2024-01-01T00:00:00Z","EndTime":"2024-01-01T00:00:00.030Z","InstrumentationScope":{"Name":"api"},"Attributes":%s}`, data)
			case FormatOTLP:
				input = fmt.Sprintf(`{"resourceSpans":[{"resource":{"attributes":[{"key":"service.name","value":{"stringValue":"api"}}]},"scopeSpans":[{"spans":[{"traceId":"00000000000000000000000000000001","spanId":"0000000000000001","name":"handle","startTimeUnixNano":"1000000000","endTimeUnixNano":"1030000000","attributes":%s}]}]}]}`, data)
			case FormatJaeger:
				input = fmt.Sprintf(`{"data":[{"processes":{"p":{"serviceName":"api"}},"spans":[{"traceID":"1","spanID":"1","operationName":"handle","processID":"p","startTime":1000000,"duration":30000,"tags":%s}]}]}`, data)
			}
			result, err := Import(strings.NewReader(input), Options{Format: format, Warnings: io.Discard})
			require.NoError(t, err)
			cfg, err := synth.ParseConfig(result.YAML)
			require.NoError(t, err)
			require.Len(t, cfg.Services[0].Operations[0].Attributes, 1)
			require.Equal(t, "value", cfg.Services[0].Operations[0].Attributes["custom"].Value)
			evidence := result.Evidence.Operations[OperationKey{"api", "handle"}].Attributes
			for _, key := range keys[:4] {
				require.NotContains(t, evidence.Keys, key)
			}
			require.Contains(t, evidence.Keys["synth.scenarios"].Reasons, "reserved_engine_attribute")
		})
	}
}

func TestImportFoldedTimestampAnomalies(t *testing.T) {
	for _, interval := range [][2]int{{-1, 20}, {10, 40}, {20, 10}} {
		t.Run(fmt.Sprint(interval), func(t *testing.T) {
			input := strings.ReplaceAll(adoptionCapture(t, minimumFitObservations, [][2]int{interval}), `"child0"`, `"handle"`)
			result, err := Import(strings.NewReader(input), Options{Format: FormatStdouttrace, Warnings: io.Discard})
			require.NoError(t, err)
			latency := result.Evidence.Operations[OperationKey{"api", "handle"}].Latency
			require.Equal(t, minimumFitObservations, latency.TimingAnomalyCount)
			require.Equal(t, statusInsufficient, latency.ImportStatus)
			require.Contains(t, latency.Reasons, "timestamp_anomaly")
		})
	}
}

func TestImportDottedOperationEvidence(t *testing.T) {
	var input strings.Builder
	for i := range minimumFitObservations {
		for j, key := range []OperationKey{{"a.b", "c"}, {"a", "b.c"}} {
			start := time.Date(2024, 1, 1, 0, 0, i, 0, time.UTC)
			require.NoError(t, json.NewEncoder(&input).Encode(map[string]any{
				"Name": key.Operation, "InstrumentationScope": map[string]string{"Name": key.Service},
				"SpanContext": map[string]string{"TraceID": fmt.Sprintf("%d-%d", i, j), "SpanID": "root"},
				"StartTime":   start, "EndTime": start.Add(time.Duration(j+1) * 10 * time.Millisecond),
				"Attributes": []any{map[string]any{"Key": "label", "Value": map[string]any{"Type": "STRING", "Value": key.Service}}},
			}))
		}
	}
	result, err := Import(strings.NewReader(input.String()), Options{Format: FormatStdouttrace, Warnings: io.Discard})
	require.NoError(t, err)
	require.Len(t, result.Evidence.Operations, 2)
	for j, key := range []OperationKey{{"a.b", "c"}, {"a", "b.c"}} {
		op := result.Evidence.Operations[key]
		require.Equal(t, statusFit, op.Latency.ImportStatus)
		require.Equal(t, float64(time.Duration(j+1)*10*time.Millisecond), op.Latency.TotalTime.Modeled.MedianNS)
		require.Equal(t, key.Service, op.RetainedAttributes["label"].Value)
	}
	cfg, err := synth.ParseConfig(result.YAML)
	require.NoError(t, err)
	for _, svc := range cfg.Services {
		require.Equal(t, svc.Name, svc.Operations[0].Attributes["label"].Value)
	}
	report := strings.Split(result.MarkdownReport(), "```yaml\n")[1]
	var structured map[string]any
	require.NoError(t, yaml.Unmarshal([]byte(strings.TrimSuffix(report, "```\n")), &structured))
	require.Len(t, structured["operations"], 2)
}

func TestImportAssessmentSpanBudget(t *testing.T) {
	const children = 100
	input := adoptionCapture(t, minimumFitObservations, make([][2]int, children))
	for i := range children {
		input = strings.ReplaceAll(input, fmt.Sprintf(`"child%d"`, i), `"child"`)
	}
	result, err := Import(strings.NewReader(input), Options{Format: FormatStdouttrace, Warnings: io.Discard})
	require.NoError(t, err)
	require.Equal(t, modelSpanBudget, result.Evidence.ModeledSpans)
	require.Less(t, result.Evidence.ModeledTraces, modelSamples)
	require.Contains(t, result.Evidence.Reasons, "assessment_span_budget")
	samples := 0
	for _, op := range result.Evidence.Operations {
		samples += op.Latency.TotalTime.ModeledCount
		require.Equal(t, statusInsufficient, op.Latency.ImportStatus)
		require.Contains(t, op.Latency.Reasons, "assessment_span_budget")
	}
	require.Equal(t, modelSpanBudget, samples)
}
