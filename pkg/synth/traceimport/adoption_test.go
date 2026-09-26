package traceimport

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/andrewh/motel/pkg/synth"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace/noop"
)

type durationObserver map[string][]time.Duration

func (o durationObserver) Observe(s synth.SpanInfo) {
	o[s.Operation] = append(o[s.Operation], s.Duration)
}

func adoptionCapture(t *testing.T, count int, children [][2]int) string {
	t.Helper()
	var b strings.Builder
	for i := range count {
		start := time.Date(2024, 1, 1, 0, 0, i, 0, time.UTC)
		emit := func(id, parent, name string, from, to int) {
			evt := map[string]any{"Name": name, "SpanContext": map[string]string{"TraceID": fmt.Sprint(i), "SpanID": id}, "Parent": map[string]string{"SpanID": parent}, "StartTime": start.Add(time.Duration(from) * time.Millisecond), "EndTime": start.Add(time.Duration(to) * time.Millisecond), "InstrumentationScope": map[string]string{"Name": "api"}}
			require.NoError(t, json.NewEncoder(&b).Encode(evt))
		}
		emit("root", "", "handle", 0, 30)
		for j, interval := range children {
			emit(fmt.Sprint(j), "root", fmt.Sprintf("child%d", j), interval[0], interval[1])
		}
	}
	return b.String()
}

func TestImportOwnTimeRegeneration(t *testing.T) {
	for _, tc := range []struct {
		name     string
		children [][2]int
		own      string
		total    time.Duration
	}{
		{"single", [][2]int{{10, 20}}, "20ms", 30 * time.Millisecond},
		{"sequential", [][2]int{{5, 10}, {15, 20}}, "20ms", 30 * time.Millisecond},
		{"parallel", [][2]int{{5, 15}, {5, 25}}, "10ms", 30 * time.Millisecond},
		{"overlap", [][2]int{{5, 20}, {10, 25}}, "10ms", 25 * time.Millisecond},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, err := Import(strings.NewReader(adoptionCapture(t, 1, tc.children)), Options{Format: FormatStdouttrace, Warnings: io.Discard})
			require.NoError(t, err)
			cfg, err := synth.ParseConfig(result.YAML)
			require.NoError(t, err)
			topo, err := synth.BuildTopology(cfg, nil)
			require.NoError(t, err)
			require.Equal(t, tc.own, topo.Services["api"].Operations["handle"].Duration.String())
			obs := durationObserver{}
			_, err = synth.GenerateTraces(context.Background(), topo, synth.TracerProviderSource(noop.NewTracerProvider()), synth.GenerateOptions{Traces: 1, Seed: 1, Observers: []synth.SpanObserver{obs}})
			require.NoError(t, err)
			require.Equal(t, []time.Duration{tc.total}, obs["handle"])
		})
	}
}

func TestImportLatencyEvidence(t *testing.T) {
	for _, count := range []int{1, 100} {
		result, err := Import(strings.NewReader(adoptionCapture(t, count, [][2]int{{10, 20}})), Options{Format: FormatStdouttrace, Warnings: io.Discard})
		require.NoError(t, err)
		cfg, err := synth.ParseConfig(result.YAML)
		require.NoError(t, err)
		require.NotNil(t, cfg.Import)
		require.Equal(t, count, result.Evidence.SourceTraceCount)
		op := result.Evidence.Operations[OperationKey{"api", "handle"}]
		require.Equal(t, count, op.Latency.ObservationCount)
		want := "insufficient_evidence"
		if count == 100 {
			want = "fit"
		}
		require.Equal(t, want, op.Latency.OwnTime.ImportStatus)
		require.Equal(t, want, op.Latency.TotalTime.ImportStatus)
		require.Equal(t, float64(30*time.Millisecond), op.Latency.TotalTime.Observed.MedianNS)
		require.Equal(t, float64(30*time.Millisecond), op.Latency.TotalTime.Modeled.MedianNS)
		require.Contains(t, result.MarkdownReport(), "api | handle")
		for _, operation := range cfg.Services[0].Operations {
			require.NotNil(t, operation.Import)
		}
	}
}

func TestImportTypedOperationAttributes(t *testing.T) {
	input := adoptionCapture(t, 2, nil)
	lines := strings.Split(strings.TrimSpace(input), "\n")
	for i, line := range lines {
		var evt map[string]any
		require.NoError(t, json.Unmarshal([]byte(line), &evt))
		attrs := []any{
			map[string]any{"Key": "content.type", "Value": map[string]any{"Type": "STRING", "Value": "application/json"}},
			map[string]any{"Key": "size", "Value": map[string]any{"Type": "INT64", "Value": 42}},
			map[string]any{"Key": "ratio", "Value": map[string]any{"Type": "FLOAT64", "Value": 1.0}},
			map[string]any{"Key": "cached", "Value": map[string]any{"Type": "BOOL", "Value": false}},
			map[string]any{"Key": "varies", "Value": map[string]any{"Type": "INT64", "Value": i}},
			map[string]any{"Key": "array", "Value": map[string]any{"Type": "STRINGSLICE", "Value": []string{"x"}}},
		}
		if i == 0 {
			attrs = append(attrs, map[string]any{"Key": "partial", "Value": map[string]any{"Type": "STRING", "Value": "x"}})
		}
		evt["Attributes"] = attrs
		evt["Resource"] = []any{map[string]any{"Key": "host.name", "Value": map[string]any{"Type": "STRING", "Value": "host"}}}
		data, err := json.Marshal(evt)
		require.NoError(t, err)
		lines[i] = string(data)
	}
	result, err := Import(strings.NewReader(strings.Join(lines, "\n")), Options{Format: FormatStdouttrace, Warnings: io.Discard})
	require.NoError(t, err)
	cfg, err := synth.ParseConfig(result.YAML)
	require.NoError(t, err)
	require.Empty(t, cfg.Services[0].ResourceAttributes)
	attrs := cfg.Services[0].Operations[0].Attributes
	require.Len(t, attrs, 4)
	require.Equal(t, "application/json", attrs["content.type"].Value)
	require.Equal(t, 42, attrs["size"].Value)
	require.IsType(t, float64(0), attrs["ratio"].Value)
	require.Equal(t, false, attrs["cached"].Value)
	evidence := result.Evidence.Operations[OperationKey{"api", "handle"}].Attributes
	require.Equal(t, "partial_presence", evidence.Keys["partial"].Reasons[0])
	require.Equal(t, "varying_value_or_type", evidence.Keys["varies"].Reasons[0])
	require.Equal(t, "unsupported_value", evidence.Keys["array"].Reasons[0])
	require.Equal(t, 2, result.Evidence.ResourceOmissions["host.name"])
}

func TestImportFitLimitations(t *testing.T) {
	for _, tc := range []struct {
		name      string
		children  [][2]int
		change    func(int, map[string]any)
		want      string
		ownStatus string
	}{
		{name: "staggered overlap", children: [][2]int{{1, 17}, {14, 29}}, want: "poor_fit", ownStatus: "fit"},
		{name: "timestamp anomaly", children: [][2]int{{-5, 20}}, want: "insufficient_evidence", ownStatus: "insufficient_evidence"},
		{name: "missing parent", change: func(_ int, e map[string]any) { e["Parent"] = map[string]string{"SpanID": "absent"} }, want: "insufficient_evidence", ownStatus: "insufficient_evidence"},
		{name: "bimodal", change: func(i int, e map[string]any) {
			start, _ := time.Parse(time.RFC3339Nano, e["StartTime"].(string))
			d := time.Millisecond
			if i%2 == 0 {
				d = 99 * time.Millisecond
			}
			e["EndTime"] = start.Add(d)
		}, want: "poor_fit", ownStatus: "poor_fit"},
		{name: "tiny duration rounding", change: func(_ int, e map[string]any) {
			start, _ := time.Parse(time.RFC3339Nano, e["StartTime"].(string))
			e["EndTime"] = start.Add(time.Nanosecond)
		}, want: "fit", ownStatus: "fit"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := adoptionCapture(t, 100, tc.children)
			if tc.change != nil {
				lines := strings.Split(strings.TrimSpace(input), "\n")
				for i, line := range lines {
					var evt map[string]any
					require.NoError(t, json.Unmarshal([]byte(line), &evt))
					tc.change(i, evt)
					data, err := json.Marshal(evt)
					require.NoError(t, err)
					lines[i] = string(data)
				}
				input = strings.Join(lines, "\n")
			}
			result, err := Import(strings.NewReader(input), Options{Format: FormatStdouttrace, Warnings: io.Discard})
			require.NoError(t, err)
			latency := result.Evidence.Operations[OperationKey{"api", "handle"}].Latency
			require.Equal(t, tc.want, latency.ImportStatus)
			require.Equal(t, tc.ownStatus, latency.OwnTime.ImportStatus)
			if tc.name == "bimodal" {
				require.Greater(t, latency.OwnTime.Modeled.MedianNS, float64(0))
				require.Equal(t, float64(time.Millisecond), latency.OwnTime.Observed.MedianNS)
			}
		})
	}
}

func TestImportMetadataDoesNotAffectExecution(t *testing.T) {
	result, err := Import(strings.NewReader(adoptionCapture(t, 1, [][2]int{{10, 20}})), Options{Format: FormatStdouttrace, Warnings: io.Discard})
	require.NoError(t, err)
	cfg, err := synth.ParseConfig(result.YAML)
	require.NoError(t, err)
	generate := func() durationObserver {
		topo, err := synth.BuildTopology(cfg, nil)
		require.NoError(t, err)
		obs := durationObserver{}
		_, err = synth.GenerateTraces(context.Background(), topo, synth.TracerProviderSource(noop.NewTracerProvider()), synth.GenerateOptions{Traces: 5, Seed: 42, Observers: []synth.SpanObserver{obs}})
		require.NoError(t, err)
		return obs
	}
	before := generate()
	cfg.Import = "arbitrary informational value"
	for i := range cfg.Services {
		for j := range cfg.Services[i].Operations {
			cfg.Services[i].Operations[j].Import = map[string]any{"latency": "unrecognized", "other": false}
		}
	}
	require.NoError(t, synth.ValidateConfig(cfg))
	require.Equal(t, before, generate())
	for j := range cfg.Services[0].Operations {
		if cfg.Services[0].Operations[j].Name == "handle" {
			cfg.Services[0].Operations[j].Duration = "100ms"
		}
	}
	require.Equal(t, 110*time.Millisecond, generate()["handle"][0])
}

func TestImportAttributeFormatsAndTelemetry(t *testing.T) {
	for _, tc := range []struct {
		name   string
		format Format
		input  string
	}{
		{"otlp", FormatOTLP, `{"resourceSpans":[{"resource":{"attributes":[{"key":"service.name","value":{"stringValue":"api"}},{"key":"host.name","value":{"stringValue":"host"}}]},"scopeSpans":[{"spans":[{"traceId":"00000000000000000000000000000001","spanId":"0000000000000001","name":"handle","startTimeUnixNano":"1000000000","endTimeUnixNano":"1030000000","attributes":[{"key":"large","value":{"intValue":"9007199254740993"}},{"key":"ratio","value":{"doubleValue":1}},{"key":"flag","value":{"boolValue":false}},{"key":"label","value":{"stringValue":"001"}},{"key":"array","value":{"arrayValue":{"values":[{"stringValue":"x"}]}}}]}]}]}]}`},
		{"jaeger", FormatJaeger, `{"data":[{"processes":{"p1":{"serviceName":"api","tags":[{"key":"host.name","type":"string","value":"host"}]}},"spans":[{"traceID":"1","spanID":"1","operationName":"handle","processID":"p1","startTime":1000000,"duration":30000,"tags":[{"key":"large","type":"int64","value":9007199254740993},{"key":"ratio","type":"float64","value":1},{"key":"flag","type":"bool","value":false},{"key":"label","type":"string","value":"001"},{"key":"array","type":"binary","value":"YWJj"}]}]}]}`},
		{"stdouttrace", FormatStdouttrace, `{"Name":"handle","SpanContext":{"TraceID":"1","SpanID":"1"},"StartTime":"2024-01-01T00:00:00Z","EndTime":"2024-01-01T00:00:00.030Z","Resource":[{"Key":"service.name","Value":{"Type":"STRING","Value":"api"}},{"Key":"host.name","Value":{"Type":"STRING","Value":"host"}}],"Attributes":[{"Key":"large","Value":{"Type":"INT64","Value":9007199254740993}},{"Key":"ratio","Value":{"Type":"FLOAT64","Value":1}},{"Key":"flag","Value":{"Type":"BOOL","Value":false}},{"Key":"label","Value":{"Type":"STRING","Value":"001"}},{"Key":"array","Value":{"Type":"STRINGSLICE","Value":["x"]}}]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, err := Import(strings.NewReader(tc.input), Options{Format: tc.format, Warnings: io.Discard})
			require.NoError(t, err)
			cfg, err := synth.ParseConfig(result.YAML)
			require.NoError(t, err)
			topo, err := synth.BuildTopology(cfg, nil)
			require.NoError(t, err)
			exporter := tracetest.NewInMemoryExporter()
			provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
			t.Cleanup(func() { require.NoError(t, provider.Shutdown(context.Background())) })
			_, err = synth.GenerateTraces(context.Background(), topo, synth.TracerProviderSource(provider), synth.GenerateOptions{Traces: 1, Seed: 1})
			require.NoError(t, err)
			spans := exporter.GetSpans()
			require.Len(t, spans, 1)
			attrs := map[string]attribute.Value{}
			for _, kv := range spans[0].Attributes {
				attrs[string(kv.Key)] = kv.Value
				require.NotContains(t, string(kv.Key), "import")
			}
			require.Equal(t, attribute.Int64Value(9007199254740993), attrs["large"])
			require.Equal(t, attribute.Float64Value(1), attrs["ratio"])
			require.Equal(t, attribute.BoolValue(false), attrs["flag"])
			require.Equal(t, attribute.StringValue("001"), attrs["label"])
			require.NotContains(t, attrs, "array")
			require.NotContains(t, attrs, "host.name")
			require.Equal(t, 1, result.Evidence.ResourceOmissions["host.name"])
		})
	}
}

func TestImportTypeDistinctAttributes(t *testing.T) {
	input := adoptionCapture(t, 2, nil)
	lines := strings.Split(strings.TrimSpace(input), "\n")
	for i, line := range lines {
		var evt map[string]any
		require.NoError(t, json.Unmarshal([]byte(line), &evt))
		kind := "INT64"
		if i == 1 {
			kind = "FLOAT64"
		}
		evt["Attributes"] = []any{map[string]any{"Key": "number", "Value": map[string]any{"Type": kind, "Value": 1}}}
		data, err := json.Marshal(evt)
		require.NoError(t, err)
		lines[i] = string(data)
	}
	result, err := Import(strings.NewReader(strings.Join(lines, "\n")), Options{Format: FormatStdouttrace, Warnings: io.Discard})
	require.NoError(t, err)
	cfg, err := synth.ParseConfig(result.YAML)
	require.NoError(t, err)
	require.Empty(t, cfg.Services[0].Operations[0].Attributes)
	require.Contains(t, result.Evidence.Operations[OperationKey{"api", "handle"}].Attributes.Keys["number"].Reasons, "varying_value_or_type")
}

func TestImportRejectsTrailingJSON(t *testing.T) {
	line := strings.TrimSpace(adoptionCapture(t, 1, nil))
	for _, suffix := range []string{" garbage", " {}"} {
		_, err := Import(strings.NewReader(line+suffix), Options{Format: FormatStdouttrace, Warnings: io.Discard})
		require.Error(t, err)
	}
}

func TestImportSingleTraceRateEvidence(t *testing.T) {
	lines := strings.Split(strings.TrimSpace(adoptionCapture(t, 2, nil)), "\n")
	for i, line := range lines {
		var evt map[string]any
		require.NoError(t, json.Unmarshal([]byte(line), &evt))
		evt["SpanContext"] = map[string]string{"TraceID": "same", "SpanID": fmt.Sprint(i)}
		data, err := json.Marshal(evt)
		require.NoError(t, err)
		lines[i] = string(data)
	}
	result, err := Import(strings.NewReader(strings.Join(lines, "\n")), Options{Format: FormatStdouttrace, Warnings: io.Discard})
	require.NoError(t, err)
	require.Equal(t, "default_1_per_second", result.Evidence.TrafficRateBasis)
}

func TestImportConfidenceEvidence(t *testing.T) {
	input := adoptionCapture(t, 2, [][2]int{{5, 10}, {15, 20}})
	lines := strings.Split(strings.TrimSpace(input), "\n")
	input = strings.Join(lines[:len(lines)-1], "\n")
	var warnings strings.Builder
	result, err := Import(strings.NewReader(input), Options{Format: FormatStdouttrace, MinTraces: 5, Warnings: &warnings})
	require.NoError(t, err)
	require.Equal(t, 5, result.Evidence.RequestedMinSamples)
	evidence := result.Evidence.Operations[OperationKey{"api", "handle"}].Inference
	require.Contains(t, evidence.Reasons, "below_requested_operation_samples")
	require.Equal(t, 1, evidence.Calls["api.child1"].PresentCount)
	require.Contains(t, evidence.Calls["api.child1"].Reasons, "below_requested_call_samples")
	require.Equal(t, 1, evidence.CallStyle.SequentialVotes)
	require.Contains(t, evidence.CallStyle.Reasons, "weak_or_mixed_call_style")
	require.Contains(t, result.MarkdownReport(), "below_requested_call_samples")
}
