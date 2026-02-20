// Tests for the motel-synth CLI commands
// Validates validate, version, and run subcommands
package main

import (
	"bytes"
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
		assert.Contains(t, out.String(), "2 root operations")
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
