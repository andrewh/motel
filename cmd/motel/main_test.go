// Tests for the motel CLI commands
package main

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writeTestConfig(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	return path
}

const validConfig = `
version: 1
services:
  gateway:
    operations:
      GET /users:
        duration: 30ms +/- 10ms
        error_rate: 0.1%
        calls:
          - backend.list
  backend:
    operations:
      list:
        duration: 20ms +/- 5ms
        error_rate: 0.1%
traffic:
  rate: 100/s
`

func TestValidateCommand(t *testing.T) {
	t.Parallel()

	t.Run("valid config", func(t *testing.T) {
		t.Parallel()
		path := writeTestConfig(t, validConfig)

		root := rootCmd()
		root.SetArgs([]string{"validate", path})
		var out bytes.Buffer
		root.SetOut(&out)

		err := root.Execute()
		require.NoError(t, err)
		assert.Contains(t, out.String(), "Configuration valid")
		assert.Contains(t, out.String(), "2 services")
		assert.Contains(t, out.String(), "1 root operation\n")
	})

	t.Run("plural root operations", func(t *testing.T) {
		t.Parallel()
		cfg := `
version: 1
services:
  svc-a:
    operations:
      op-a:
        duration: 10ms
  svc-b:
    operations:
      op-b:
        duration: 10ms
traffic:
  rate: 10/s
`
		path := writeTestConfig(t, cfg)
		root := rootCmd()
		root.SetArgs([]string{"validate", path})
		var out bytes.Buffer
		root.SetOut(&out)

		err := root.Execute()
		require.NoError(t, err)
		assert.Contains(t, out.String(), "2 services")
		assert.Contains(t, out.String(), "2 root operations")
	})

	t.Run("singular service", func(t *testing.T) {
		t.Parallel()
		cfg := `
version: 1
services:
  svc:
    operations:
      op:
        duration: 10ms
traffic:
  rate: 10/s
`
		path := writeTestConfig(t, cfg)
		root := rootCmd()
		root.SetArgs([]string{"validate", path})
		var out bytes.Buffer
		root.SetOut(&out)

		err := root.Execute()
		require.NoError(t, err)
		assert.Contains(t, out.String(), "1 service,")
	})

	t.Run("invalid config", func(t *testing.T) {
		t.Parallel()
		path := writeTestConfig(t, `
version: 1
services:
  svc:
    operations:
      op:
        duration: not-a-duration
traffic:
  rate: 100/s
`)
		root := rootCmd()
		root.SetArgs([]string{"validate", path})

		err := root.Execute()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "duration")
	})

	t.Run("missing file", func(t *testing.T) {
		t.Parallel()
		root := rootCmd()
		root.SetArgs([]string{"validate", "/nonexistent/config.yaml"})

		err := root.Execute()
		require.Error(t, err)
	})
}

func TestVersionCommand(t *testing.T) {
	t.Parallel()

	root := rootCmd()
	root.SetArgs([]string{"version"})

	var out bytes.Buffer
	root.SetOut(&out)

	err := root.Execute()
	require.NoError(t, err)
}

func TestRunCommand(t *testing.T) {
	t.Parallel()

	t.Run("with stdout and short duration", func(t *testing.T) {
		t.Parallel()
		path := writeTestConfig(t, validConfig)

		root := rootCmd()
		root.SetArgs([]string{"run", "--stdout", "--duration", "100ms", path})

		err := root.Execute()
		require.NoError(t, err)
	})

	t.Run("missing config file", func(t *testing.T) {
		t.Parallel()
		root := rootCmd()
		root.SetArgs([]string{"run", "--stdout", "/nonexistent.yaml"})

		err := root.Execute()
		require.Error(t, err)
	})

	t.Run("no args shows usage error", func(t *testing.T) {
		t.Parallel()
		root := rootCmd()
		root.SetArgs([]string{"run"})

		err := root.Execute()
		require.Error(t, err)
	})

	t.Run("all signals with stdout", func(t *testing.T) {
		t.Parallel()
		path := writeTestConfig(t, validConfig)

		root := rootCmd()
		root.SetArgs([]string{"run", "--stdout", "--duration", "100ms", "--signals", "traces,metrics,logs", path})

		err := root.Execute()
		require.NoError(t, err)
	})

	t.Run("metrics only with stdout", func(t *testing.T) {
		t.Parallel()
		path := writeTestConfig(t, validConfig)

		root := rootCmd()
		root.SetArgs([]string{"run", "--stdout", "--duration", "100ms", "--signals", "metrics", path})

		err := root.Execute()
		require.NoError(t, err)
	})

	t.Run("logs only with stdout", func(t *testing.T) {
		t.Parallel()
		path := writeTestConfig(t, validConfig)

		root := rootCmd()
		root.SetArgs([]string{"run", "--stdout", "--duration", "100ms", "--signals", "logs", "--slow-threshold", "1ms", path})

		err := root.Execute()
		require.NoError(t, err)
	})
}

func TestParseSignals(t *testing.T) {
	t.Parallel()

	t.Run("valid signals", func(t *testing.T) {
		t.Parallel()
		tests := []struct {
			input    string
			expected map[string]bool
		}{
			{"traces", map[string]bool{"traces": true}},
			{"traces,metrics,logs", map[string]bool{"traces": true, "metrics": true, "logs": true}},
			{"metrics", map[string]bool{"metrics": true}},
			{" traces , logs ", map[string]bool{"traces": true, "logs": true}},
			{"", map[string]bool{}},
		}

		for _, tt := range tests {
			t.Run(tt.input, func(t *testing.T) {
				t.Parallel()
				result, err := parseSignals(tt.input)
				require.NoError(t, err)
				assert.Equal(t, tt.expected, result)
			})
		}
	})

	t.Run("unknown signal returns error", func(t *testing.T) {
		t.Parallel()
		tests := []struct {
			name  string
			input string
		}{
			{"typo", "trace"},
			{"mixed valid and invalid", "traces,metric"},
			{"completely unknown", "spans"},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				t.Parallel()
				_, err := parseSignals(tt.input)
				require.Error(t, err)
				assert.Contains(t, err.Error(), "unknown signal")
			})
		}
	})
}

func TestRunCommandInvalidSignal(t *testing.T) {
	t.Parallel()

	path := writeTestConfig(t, validConfig)
	root := rootCmd()
	root.SetArgs([]string{"run", "--stdout", "--duration", "100ms", "--signals", "trace", path})

	err := root.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown signal")
}

func TestRunCommandNegativeSlowThreshold(t *testing.T) {
	t.Parallel()

	path := writeTestConfig(t, validConfig)
	root := rootCmd()
	root.SetArgs([]string{"run", "--stdout", "--duration", "100ms", "--slow-threshold", "-1s", path})

	err := root.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "slow-threshold")
}

func TestRunCommandSlowThresholdWithoutLogs(t *testing.T) {
	t.Parallel()

	path := writeTestConfig(t, validConfig)
	root := rootCmd()
	root.SetArgs([]string{"run", "--stdout", "--duration", "100ms", "--slow-threshold", "50ms", path})
	var stderr bytes.Buffer
	root.SetErr(&stderr)

	err := root.Execute()
	require.NoError(t, err)
	assert.Contains(t, stderr.String(), "--slow-threshold has no effect without --signals logs")
}

func TestSemconvFlag(t *testing.T) {
	t.Parallel()

	t.Run("validate with custom semconv dir", func(t *testing.T) {
		t.Parallel()
		semconvDir := t.TempDir()
		myappDir := filepath.Join(semconvDir, "myapp")
		require.NoError(t, os.MkdirAll(myappDir, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(myappDir, "registry.yaml"), []byte(`
groups:
  - id: registry.myapp
    type: attribute_group
    brief: 'My app attributes.'
    attributes:
      - id: myapp.request_id
        type: string
        brief: 'Request ID.'
        examples: ["abc-123"]
`), 0o600))

		cfg := `
version: 1
services:
  svc:
    operations:
      op:
        duration: 10ms
        domain: myapp
traffic:
  rate: 10/s
`
		path := writeTestConfig(t, cfg)
		root := rootCmd()
		root.SetArgs([]string{"validate", "--semconv", semconvDir, path})
		var out bytes.Buffer
		root.SetOut(&out)

		err := root.Execute()
		require.NoError(t, err)
		assert.Contains(t, out.String(), "Configuration valid")
	})

	t.Run("run with custom semconv dir", func(t *testing.T) {
		t.Parallel()
		semconvDir := t.TempDir()
		myappDir := filepath.Join(semconvDir, "myapp")
		require.NoError(t, os.MkdirAll(myappDir, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(myappDir, "registry.yaml"), []byte(`
groups:
  - id: registry.myapp
    type: attribute_group
    brief: 'My app attributes.'
    attributes:
      - id: myapp.request_id
        type: string
        brief: 'Request ID.'
        examples: ["abc-123"]
`), 0o600))

		cfg := `
version: 1
services:
  svc:
    operations:
      op:
        duration: 10ms
        domain: myapp
traffic:
  rate: 10/s
`
		path := writeTestConfig(t, cfg)
		root := rootCmd()
		root.SetArgs([]string{"run", "--stdout", "--duration", "100ms", "--semconv", semconvDir, path})

		err := root.Execute()
		require.NoError(t, err)
	})

	t.Run("nonexistent semconv dir", func(t *testing.T) {
		t.Parallel()
		path := writeTestConfig(t, validConfig)
		root := rootCmd()
		root.SetArgs([]string{"validate", "--semconv", "/nonexistent/semconv", path})

		err := root.Execute()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "does not exist")
	})

	t.Run("semconv path is a file not a directory", func(t *testing.T) {
		t.Parallel()
		f := filepath.Join(t.TempDir(), "not-a-dir.yaml")
		require.NoError(t, os.WriteFile(f, []byte("hello"), 0o600))

		path := writeTestConfig(t, validConfig)
		root := rootCmd()
		root.SetArgs([]string{"validate", "--semconv", f, path})

		err := root.Execute()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "not a directory")
	})
}

func TestCheckEndpoint(t *testing.T) {
	t.Parallel()

	t.Run("unreachable default endpoint", func(t *testing.T) {
		t.Parallel()
		err := checkEndpoint("", "http/protobuf", "test.yaml")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "cannot reach OTLP collector at localhost:4318")
		assert.Contains(t, err.Error(), "--stdout")
		assert.Contains(t, err.Error(), "--endpoint")
		assert.Contains(t, err.Error(), "Without --duration, motel runs for 1 minute")
		assert.Contains(t, err.Error(), "test.yaml")
	})

	t.Run("unreachable grpc default endpoint", func(t *testing.T) {
		t.Parallel()
		err := checkEndpoint("", "grpc", "test.yaml")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "cannot reach OTLP collector at localhost:4317")
	})

	t.Run("unreachable custom endpoint", func(t *testing.T) {
		t.Parallel()
		err := checkEndpoint("192.0.2.1:9999", "http/protobuf", "test.yaml")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "cannot reach OTLP collector at 192.0.2.1:9999")
	})

	t.Run("reachable endpoint succeeds", func(t *testing.T) {
		t.Parallel()
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, err)
		defer ln.Close() //nolint:errcheck // best-effort close in test

		err = checkEndpoint(ln.Addr().String(), "http/protobuf", "test.yaml")
		require.NoError(t, err)
	})

	t.Run("endpoint without port gets default", func(t *testing.T) {
		t.Parallel()
		err := checkEndpoint("192.0.2.1", "http/protobuf", "test.yaml")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "192.0.2.1:4318")
	})

	t.Run("run command fails fast without collector", func(t *testing.T) {
		t.Parallel()
		path := writeTestConfig(t, validConfig)
		root := rootCmd()
		root.SetArgs([]string{"run", "--endpoint", "192.0.2.1:9999", path})

		err := root.Execute()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "cannot reach OTLP collector")
	})
}

func TestCheckCommand(t *testing.T) {
	t.Parallel()

	t.Run("passing checks", func(t *testing.T) {
		t.Parallel()
		path := writeTestConfig(t, validConfig)
		root := rootCmd()
		root.SetArgs([]string{"check", path})
		var out bytes.Buffer
		root.SetOut(&out)

		err := root.Execute()
		require.NoError(t, err)
		assert.Contains(t, out.String(), "PASS  max-depth:")
		assert.Contains(t, out.String(), "PASS  max-fan-out:")
		assert.Contains(t, out.String(), "PASS  max-spans:")
		assert.Contains(t, out.String(), "path:")
		assert.Contains(t, out.String(), "worst:")
	})

	t.Run("failing depth limit", func(t *testing.T) {
		t.Parallel()
		path := writeTestConfig(t, validConfig)
		root := rootCmd()
		root.SetArgs([]string{"check", "--max-depth", "0", path})
		var out bytes.Buffer
		root.SetOut(&out)

		err := root.Execute()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "one or more checks failed")
		assert.Contains(t, out.String(), "FAIL  max-depth:")
	})

	t.Run("failing fan-out limit", func(t *testing.T) {
		t.Parallel()
		cfg := `
version: 1
services:
  gateway:
    operations:
      request:
        duration: 5ms
        calls:
          - target: backend.op
            count: 5
  backend:
    operations:
      op:
        duration: 10ms
traffic:
  rate: 10/s
`
		path := writeTestConfig(t, cfg)
		root := rootCmd()
		root.SetArgs([]string{"check", "--max-fan-out", "1", path})
		var out bytes.Buffer
		root.SetOut(&out)

		err := root.Execute()
		require.Error(t, err)
		assert.Contains(t, out.String(), "FAIL  max-fan-out:")
	})

	t.Run("failing spans limit", func(t *testing.T) {
		t.Parallel()
		path := writeTestConfig(t, validConfig)
		root := rootCmd()
		root.SetArgs([]string{"check", "--max-spans", "1", path})
		var out bytes.Buffer
		root.SetOut(&out)

		err := root.Execute()
		require.Error(t, err)
		assert.Contains(t, out.String(), "FAIL  max-spans:")
	})

	t.Run("static only with samples 0", func(t *testing.T) {
		t.Parallel()
		path := writeTestConfig(t, validConfig)
		root := rootCmd()
		root.SetArgs([]string{"check", "--samples", "0", path})
		var out bytes.Buffer
		root.SetOut(&out)

		err := root.Execute()
		require.NoError(t, err)
		assert.Contains(t, out.String(), "static worst-case")
		assert.NotContains(t, out.String(), "observed")
		assert.NotContains(t, out.String(), "p50:")
	})

	t.Run("percentile lines with samples", func(t *testing.T) {
		t.Parallel()
		path := writeTestConfig(t, validConfig)
		root := rootCmd()
		root.SetArgs([]string{"check", "--seed", "42", path})
		var out bytes.Buffer
		root.SetOut(&out)

		err := root.Execute()
		require.NoError(t, err)
		assert.Contains(t, out.String(), "p50:")
		assert.Contains(t, out.String(), "p95:")
		assert.Contains(t, out.String(), "p99:")
		assert.Contains(t, out.String(), "samples)")
	})

	t.Run("with seed for reproducibility", func(t *testing.T) {
		t.Parallel()
		path := writeTestConfig(t, validConfig)

		run := func() string {
			root := rootCmd()
			root.SetArgs([]string{"check", "--seed", "42", path})
			var out bytes.Buffer
			root.SetOut(&out)
			err := root.Execute()
			require.NoError(t, err)
			return out.String()
		}
		assert.Equal(t, run(), run())
	})

	t.Run("max-spans-per-trace flag", func(t *testing.T) {
		t.Parallel()
		path := writeTestConfig(t, validConfig)
		root := rootCmd()
		root.SetArgs([]string{"check", "--max-spans-per-trace", "2", "--seed", "1", path})
		var out bytes.Buffer
		root.SetOut(&out)

		err := root.Execute()
		require.NoError(t, err)
		assert.Contains(t, out.String(), "observed")
	})

	t.Run("missing file", func(t *testing.T) {
		t.Parallel()
		root := rootCmd()
		root.SetArgs([]string{"check", "/nonexistent.yaml"})

		err := root.Execute()
		require.Error(t, err)
	})

	t.Run("no args", func(t *testing.T) {
		t.Parallel()
		root := rootCmd()
		root.SetArgs([]string{"check"})

		err := root.Execute()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "missing topology file")
	})

	t.Run("negative limit rejected", func(t *testing.T) {
		t.Parallel()
		path := writeTestConfig(t, validConfig)
		root := rootCmd()
		root.SetArgs([]string{"check", "--max-depth", "-1", path})

		err := root.Execute()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "non-negative")
	})

	t.Run("invalid config", func(t *testing.T) {
		t.Parallel()
		path := writeTestConfig(t, `
version: 1
services:
  svc:
    operations:
      op:
        duration: bad
traffic:
  rate: 10/s
`)
		root := rootCmd()
		root.SetArgs([]string{"check", path})

		err := root.Execute()
		require.Error(t, err)
	})

	t.Run("validation error", func(t *testing.T) {
		t.Parallel()
		path := writeTestConfig(t, `
version: 1
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
		root := rootCmd()
		root.SetArgs([]string{"check", path})

		err := root.Execute()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "unknown operation")
	})

	t.Run("semconv flag", func(t *testing.T) {
		t.Parallel()
		path := writeTestConfig(t, validConfig)
		root := rootCmd()
		root.SetArgs([]string{"check", "--semconv", "/nonexistent", path})

		err := root.Execute()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "does not exist")
	})
}

func TestImportCommand(t *testing.T) {
	t.Parallel()

	stdouttraceSpans := strings.Join([]string{
		`{"Name":"request","SpanContext":{"TraceID":"t1","SpanID":"s1"},"Parent":{"TraceID":"t1","SpanID":"0000000000000000"},"StartTime":"2024-01-01T00:00:00Z","EndTime":"2024-01-01T00:00:00.050Z","Attributes":[],"Status":{"Code":"Unset"},"InstrumentationScope":{"Name":"gateway"}}`,
		`{"Name":"query","SpanContext":{"TraceID":"t1","SpanID":"s2"},"Parent":{"TraceID":"t1","SpanID":"s1"},"StartTime":"2024-01-01T00:00:00.010Z","EndTime":"2024-01-01T00:00:00.040Z","Attributes":[],"Status":{"Code":"Unset"},"InstrumentationScope":{"Name":"db"}}`,
	}, "\n")

	t.Run("from file", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		path := filepath.Join(dir, "traces.jsonl")
		require.NoError(t, os.WriteFile(path, []byte(stdouttraceSpans), 0o600))

		root := rootCmd()
		root.SetArgs([]string{"import", path})
		var out bytes.Buffer
		root.SetOut(&out)

		err := root.Execute()
		require.NoError(t, err)
		assert.Contains(t, out.String(), "gateway:")
		assert.Contains(t, out.String(), "db:")
	})

	t.Run("explicit format", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		path := filepath.Join(dir, "traces.jsonl")
		require.NoError(t, os.WriteFile(path, []byte(stdouttraceSpans), 0o600))

		root := rootCmd()
		root.SetArgs([]string{"import", "--format", "stdouttrace", path})
		var out bytes.Buffer
		root.SetOut(&out)

		err := root.Execute()
		require.NoError(t, err)
		assert.Contains(t, out.String(), "gateway:")
	})

	t.Run("nonexistent file", func(t *testing.T) {
		t.Parallel()
		root := rootCmd()
		root.SetArgs([]string{"import", "/nonexistent/traces.json"})

		err := root.Execute()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "opening input")
	})

	t.Run("empty file", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		path := filepath.Join(dir, "empty.jsonl")
		require.NoError(t, os.WriteFile(path, []byte(""), 0o600))

		root := rootCmd()
		root.SetArgs([]string{"import", path})

		err := root.Execute()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no spans found")
		assert.Contains(t, err.Error(), "motel import")
	})

	t.Run("min-traces flag warns", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		path := filepath.Join(dir, "traces.jsonl")
		require.NoError(t, os.WriteFile(path, []byte(stdouttraceSpans), 0o600))

		root := rootCmd()
		root.SetArgs([]string{"import", "--min-traces", "10", path})
		var out, stderr bytes.Buffer
		root.SetOut(&out)
		root.SetErr(&stderr)

		err := root.Execute()
		require.NoError(t, err)
		assert.Contains(t, stderr.String(), "only 1 trace")
	})
}

// mockShutdownable records shutdown calls and executes a configurable function.
type mockShutdownable struct {
	shutdownFunc func(context.Context) error
	called       atomic.Bool
	shutdownAt   atomic.Int64
}

func (m *mockShutdownable) Shutdown(ctx context.Context) error {
	m.called.Store(true)
	m.shutdownAt.Store(time.Now().UnixNano())
	if m.shutdownFunc != nil {
		return m.shutdownFunc(ctx)
	}
	return nil
}

func TestShutdownAllBasic(t *testing.T) {
	t.Parallel()

	items := make([]*mockShutdownable, 5)
	for i := range items {
		items[i] = &mockShutdownable{}
	}

	ctx := context.Background()
	shutdownAll(ctx, items, "test")

	for i, item := range items {
		assert.True(t, item.called.Load(), "item %d was not shut down", i)
	}
}

func TestShutdownAllConcurrent(t *testing.T) {
	t.Parallel()

	const n = 10
	items := make([]*mockShutdownable, n)
	for i := range items {
		items[i] = &mockShutdownable{
			shutdownFunc: func(ctx context.Context) error {
				time.Sleep(100 * time.Millisecond)
				return nil
			},
		}
	}

	start := time.Now()
	shutdownAll(context.Background(), items, "test")
	elapsed := time.Since(start)

	// Sequential would take n*100ms = 1s. Concurrent should be ~100ms.
	assert.Less(t, elapsed, 500*time.Millisecond,
		"expected concurrent shutdown in ~100ms, got %v", elapsed)

	for i, item := range items {
		assert.True(t, item.called.Load(), "item %d was not shut down", i)
	}
}

func TestShutdownAllErrorDoesNotBlock(t *testing.T) {
	// Not parallel: swaps os.Stderr which is a global.
	items := []*mockShutdownable{
		{},
		{shutdownFunc: func(ctx context.Context) error {
			return fmt.Errorf("provider broken")
		}},
		{},
	}

	origStderr := os.Stderr
	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stderr = w

	shutdownAll(context.Background(), items, "test widget")

	w.Close()
	os.Stderr = origStderr
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(r)

	for i, item := range items {
		assert.True(t, item.called.Load(), "item %d was not shut down", i)
	}
	assert.Contains(t, buf.String(), "error shutting down test widget")
	assert.Contains(t, buf.String(), "provider broken")
}

func TestShutdownAllRespectsContext(t *testing.T) {
	// Not parallel: swaps os.Stderr which is a global.

	items := []*mockShutdownable{
		{},
		{shutdownFunc: func(ctx context.Context) error {
			<-ctx.Done()
			return ctx.Err()
		}},
		{},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	// Capture stderr to suppress expected error log.
	origStderr := os.Stderr
	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stderr = w

	start := time.Now()
	shutdownAll(ctx, items, "test")
	elapsed := time.Since(start)

	w.Close()
	os.Stderr = origStderr
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(r)

	assert.Less(t, elapsed, time.Second,
		"expected shutdownAll to return after context deadline, got %v", elapsed)

	for i, item := range items {
		assert.True(t, item.called.Load(), "item %d was not shut down", i)
	}
	assert.Contains(t, buf.String(), "context deadline exceeded")
}

func TestRunCommandTimeOffset(t *testing.T) {
	t.Parallel()

	path := writeTestConfig(t, validConfig)
	root := rootCmd()
	root.SetArgs([]string{"run", "--stdout", "--duration", "100ms", "--time-offset", "-1h", path})

	err := root.Execute()
	require.NoError(t, err)
}

func TestRunCommandRealtime(t *testing.T) {
	t.Parallel()

	path := writeTestConfig(t, validConfig)
	root := rootCmd()
	root.SetArgs([]string{"run", "--stdout", "--duration", "200ms", "--realtime", path})

	err := root.Execute()
	require.NoError(t, err)
}

func TestRunCommandRealtimeWithTimeOffset(t *testing.T) {
	t.Parallel()

	path := writeTestConfig(t, validConfig)
	root := rootCmd()
	root.SetArgs([]string{"run", "--stdout", "--duration", "100ms", "--realtime", "--time-offset", "-1h", path})

	err := root.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--realtime and --time-offset cannot be used together")
}

func TestShutdownAllEmpty(t *testing.T) {
	t.Parallel()

	// Should not panic or hang.
	shutdownAll(context.Background(), []*mockShutdownable{}, "test")
}

func TestMetricShutdownExporterOrder(t *testing.T) {
	t.Parallel()

	var exporterShutdownAt atomic.Int64
	exporter := &mockShutdownable{
		shutdownFunc: func(ctx context.Context) error {
			exporterShutdownAt.Store(time.Now().UnixNano())
			return nil
		},
	}

	providers := make([]*mockShutdownable, 5)
	for i := range providers {
		providers[i] = &mockShutdownable{
			shutdownFunc: func(ctx context.Context) error {
				time.Sleep(50 * time.Millisecond)
				return nil
			},
		}
	}

	// Replicate the metrics shutdown pattern: drain providers, then shut down exporter.
	ctx := context.Background()
	shutdownAll(ctx, providers, "meter provider")
	_ = exporter.Shutdown(ctx)

	exporterTime := exporterShutdownAt.Load()
	for i, p := range providers {
		assert.Less(t, p.shutdownAt.Load(), exporterTime,
			"provider %d shut down after exporter", i)
	}
}
