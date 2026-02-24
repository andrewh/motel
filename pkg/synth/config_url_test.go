package synth

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadConfigFromURL(t *testing.T) {
	t.Parallel()

	validYAML := `
version: 1
services:
  gateway:
    operations:
      GET /users:
        duration: 30ms +/- 10ms
        error_rate: 0.1%
traffic:
  rate: 10/s
`

	t.Run("loads config from HTTP URL", func(t *testing.T) {
		t.Parallel()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte(validYAML))
		}))
		defer srv.Close()

		cfg, err := LoadConfig(srv.URL + "/topology.yaml")
		require.NoError(t, err)
		assert.Equal(t, 1, cfg.Version)
		require.Len(t, cfg.Services, 1)
		assert.Equal(t, "gateway", cfg.Services[0].Name)
	})

	t.Run("returns error for non-2xx status", func(t *testing.T) {
		t.Parallel()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "not found", http.StatusNotFound)
		}))
		defer srv.Close()

		_, err := LoadConfig(srv.URL + "/missing.yaml")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "HTTP 404")
	})

	t.Run("returns error for unreachable server", func(t *testing.T) {
		t.Parallel()
		_, err := LoadConfig("http://127.0.0.1:1/topology.yaml")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "fetching URL")
	})
}
