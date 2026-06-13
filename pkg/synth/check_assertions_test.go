package synth

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadCheckAssertions(t *testing.T) {
	t.Parallel()

	t.Run("valid thresholds", func(t *testing.T) {
		t.Parallel()
		path := writeTestConfig(t, `
version: 1
checks:
  max_depth: 8
  max_fan_out: 20
  max_spans: 200
  p95_depth: 6
  p99_spans: 150
`)

		assertions, err := LoadCheckAssertions(path)
		require.NoError(t, err)
		assert.Equal(t, CurrentVersion, assertions.Version)
		assert.Equal(t, 8, *assertions.Checks.MaxDepth)
		assert.Equal(t, 20, *assertions.Checks.MaxFanOut)
		assert.Equal(t, 200, *assertions.Checks.MaxSpans)
		assert.Equal(t, 6, *assertions.Checks.P95Depth)
		assert.Equal(t, 150, *assertions.Checks.P99Spans)
		assert.True(t, assertions.Checks.HasAny())
		assert.True(t, assertions.Checks.HasPercentile())
	})

	t.Run("from URL", func(t *testing.T) {
		t.Parallel()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`
version: 1
checks:
  max_depth: 8
`))
		}))
		t.Cleanup(srv.Close)

		assertions, err := LoadCheckAssertions(srv.URL + "/checks.yaml")
		require.NoError(t, err)
		assert.Equal(t, 8, *assertions.Checks.MaxDepth)
	})

	t.Run("missing version", func(t *testing.T) {
		t.Parallel()
		path := writeTestConfig(t, `
checks:
  max_depth: 8
`)

		_, err := LoadCheckAssertions(path)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "missing required field: version")
	})

	t.Run("unsupported version", func(t *testing.T) {
		t.Parallel()
		path := writeTestConfig(t, `
version: 99
checks:
  max_depth: 8
`)

		_, err := LoadCheckAssertions(path)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "unsupported checks version 99")
	})

	t.Run("empty checks", func(t *testing.T) {
		t.Parallel()
		path := writeTestConfig(t, `
version: 1
checks: {}
`)

		_, err := LoadCheckAssertions(path)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "checks section must define at least one threshold")
	})

	t.Run("negative threshold", func(t *testing.T) {
		t.Parallel()
		path := writeTestConfig(t, `
version: 1
checks:
  p99_depth: -1
`)

		_, err := LoadCheckAssertions(path)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "checks.p99_depth must be non-negative")
	})

	t.Run("unknown threshold", func(t *testing.T) {
		t.Parallel()
		path := writeTestConfig(t, `
version: 1
checks:
  p99_dept: 8
`)

		_, err := LoadCheckAssertions(path)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "field p99_dept not found")
	})
}
