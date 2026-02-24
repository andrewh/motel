package synth

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
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
		assert.Contains(t, err.Error(), "connection refused")
	})

	t.Run("returns error when response exceeds size limit", func(t *testing.T) {
		t.Parallel()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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

func TestUnwrapHTTPError(t *testing.T) {
	t.Parallel()

	t.Run("unwraps url.Error to inner error", func(t *testing.T) {
		t.Parallel()
		inner := errors.New("something broke")
		err := &url.Error{Op: "Get", URL: "http://example.com", Err: inner}
		got := unwrapHTTPError(err)
		assert.Equal(t, "something broke", got.Error())
	})

	t.Run("unwraps url.Error wrapping net.OpError", func(t *testing.T) {
		t.Parallel()
		syscallErr := errors.New("connection refused")
		opErr := &net.OpError{Op: "dial", Net: "tcp", Err: syscallErr}
		err := &url.Error{Op: "Get", URL: "http://example.com", Err: opErr}
		got := unwrapHTTPError(err)
		assert.Equal(t, "connection refused", got.Error())
	})

	t.Run("returns timeout message for timeout errors", func(t *testing.T) {
		t.Parallel()
		err := &url.Error{
			Op:  "Get",
			URL: "http://example.com",
			Err: &timeoutError{msg: "context deadline exceeded"},
		}
		got := unwrapHTTPError(err)
		assert.Equal(t, "timed out after 10s", got.Error())
	})

	t.Run("passes through non-url.Error", func(t *testing.T) {
		t.Parallel()
		err := errors.New("plain error")
		got := unwrapHTTPError(err)
		assert.Equal(t, "plain error", got.Error())
	})
}

// timeoutError is a test helper that satisfies net.Error with Timeout() == true.
type timeoutError struct {
	msg string
}

func (e *timeoutError) Error() string   { return e.msg }
func (e *timeoutError) Timeout() bool   { return true }
func (e *timeoutError) Temporary() bool { return false }
