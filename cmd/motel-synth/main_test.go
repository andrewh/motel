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
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	return path
}

const validConfig = `
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
	})

	t.Run("invalid config", func(t *testing.T) {
		t.Parallel()
		path := writeTestConfig(t, `
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
}
