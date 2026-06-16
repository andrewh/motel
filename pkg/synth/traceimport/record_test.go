package traceimport

import (
	"bytes"
	"strings"
	"testing"

	"github.com/andrewh/motel/pkg/synth"
)

func TestImportWritesRecording(t *testing.T) {
	input := strings.Join([]string{
		`{"Name":"GET /x","SpanContext":{"TraceID":"aaa","SpanID":"bbb"},"Parent":{"TraceID":"aaa","SpanID":"0000000000000000"},"StartTime":"2024-01-01T00:00:00Z","EndTime":"2024-01-01T00:00:00.010Z","InstrumentationScope":{"Name":"api"},"Status":{"Code":"Unset"}}`,
		`{"Name":"query","SpanContext":{"TraceID":"aaa","SpanID":"ccc"},"Parent":{"TraceID":"aaa","SpanID":"bbb"},"StartTime":"2024-01-01T00:00:00.002Z","EndTime":"2024-01-01T00:00:00.008Z","InstrumentationScope":{"Name":"db"},"Status":{"Code":"Error"}}`,
	}, "\n")

	var rec bytes.Buffer
	_, err := Import(strings.NewReader(input), Options{
		Format:   FormatStdouttrace,
		Warnings: discardWriter{},
		RecordTo: &rec,
	})
	if err != nil {
		t.Fatalf("import: %v", err)
	}

	var traces []synth.RecordedTrace
	if err := synth.ReadRecording(&rec, func(tr synth.RecordedTrace) error {
		traces = append(traces, tr)
		return nil
	}); err != nil {
		t.Fatalf("read recording: %v", err)
	}

	if len(traces) != 1 {
		t.Fatalf("got %d traces, want 1", len(traces))
	}
	if len(traces[0].Spans) != 2 {
		t.Fatalf("got %d spans, want 2", len(traces[0].Spans))
	}
}

func TestImportRecordingRejectedForMetaSummary(t *testing.T) {
	var rec bytes.Buffer
	_, err := Import(strings.NewReader(""), Options{
		Format:   FormatMetaSummary,
		Warnings: discardWriter{},
		RecordTo: &rec,
	})
	if err == nil || !strings.Contains(err.Error(), "not supported for meta-summary") {
		t.Fatalf("expected meta-summary rejection, got %v", err)
	}
}

// discardWriter is a minimal io.Writer that discards warnings.
type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }
