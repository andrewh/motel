package synth

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
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
			fmt.Fprint(w, validYAML)
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

	t.Run("returns error when response exceeds size limit", func(t *testing.T) {
		t.Parallel()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Write just over the 10 MB limit
			padding := strings.Repeat("x", maxSourceBytes+1)
			fmt.Fprint(w, padding)
		}))
		defer srv.Close()

		_, err := LoadConfig(srv.URL + "/huge.yaml")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "exceeds")
	})

	t.Run("follows redirects up to limit", func(t *testing.T) {
		t.Parallel()
		hops := 0
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			hops++
			if r.URL.Path == "/final" {
				fmt.Fprint(w, validYAML)
				return
			}
			http.Redirect(w, r, "/final", http.StatusFound)
		}))
		defer srv.Close()

		cfg, err := LoadConfig(srv.URL + "/start")
		require.NoError(t, err)
		assert.Equal(t, 1, cfg.Version)
		assert.Equal(t, 2, hops)
	})

	t.Run("returns error on too many redirects", func(t *testing.T) {
		t.Parallel()
		counter := 0
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			counter++
			http.Redirect(w, r, fmt.Sprintf("/hop%d", counter), http.StatusFound)
		}))
		defer srv.Close()

		_, err := LoadConfig(srv.URL + "/start")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "redirect")
	})
}
