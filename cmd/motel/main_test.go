// Tests for the motel CLI commands
// Validates validate, version, and run subcommands
package main

import (
	"bytes"
	"net"
	"os"
	"path/filepath"
	"testing"

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

func TestCheckEndpoint(t *testing.T) {
	t.Parallel()

	t.Run("unreachable default endpoint", func(t *testing.T) {
		t.Parallel()
		err := checkEndpoint("", "http/protobuf")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "cannot reach OTLP collector at localhost:4318")
		assert.Contains(t, err.Error(), "--stdout")
		assert.Contains(t, err.Error(), "--endpoint")
	})

	t.Run("unreachable grpc default endpoint", func(t *testing.T) {
		t.Parallel()
		err := checkEndpoint("", "grpc")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "cannot reach OTLP collector at localhost:4317")
	})

	t.Run("unreachable custom endpoint", func(t *testing.T) {
		t.Parallel()
		err := checkEndpoint("192.0.2.1:9999", "http/protobuf")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "cannot reach OTLP collector at 192.0.2.1:9999")
	})

	t.Run("reachable endpoint succeeds", func(t *testing.T) {
		t.Parallel()
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, err)
		defer ln.Close() //nolint:errcheck // best-effort close in test

		err = checkEndpoint(ln.Addr().String(), "http/protobuf")
		require.NoError(t, err)
	})

	t.Run("endpoint without port gets default", func(t *testing.T) {
		t.Parallel()
		err := checkEndpoint("192.0.2.1", "http/protobuf")
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
