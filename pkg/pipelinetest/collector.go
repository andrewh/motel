package pipelinetest

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"text/template"
	"time"
)

// BinaryEnv names the environment variable that overrides the collector
// binary the harness runs. When unset, the harness looks for "otelcol" on
// PATH.
const BinaryEnv = "MOTEL_COLLECTOR_BIN"

// ErrNoCollector is returned by Start when no collector binary can be found.
// Tests use errors.Is to skip when the harness cannot run.
var ErrNoCollector = errors.New("no collector binary found")

// CollectorBinary resolves the collector binary path from BinaryEnv, falling
// back to "otelcol" on PATH. The boolean reports whether one was found.
func CollectorBinary() (string, bool) {
	if p := os.Getenv(BinaryEnv); p != "" {
		if _, err := os.Stat(p); err == nil {
			return p, true
		}
		return "", false
	}
	if p, err := exec.LookPath("otelcol"); err == nil {
		return p, true
	}
	return "", false
}

// SupportsComponent reports whether the collector binary advertises the named
// component (for example "tail_sampling") in its `components` output. It
// returns false when no binary is available or the subcommand fails, so tests
// can skip cleanly on collector builds that lack an optional component.
func SupportsComponent(name string) bool {
	bin, ok := CollectorBinary()
	if !ok {
		return false
	}
	out, err := exec.Command(bin, "components").Output() //nolint:gosec // binary path is operator-controlled
	if err != nil {
		return false
	}
	re := regexp.MustCompile(`\b` + regexp.QuoteMeta(name) + `\b`)
	return re.Match(out)
}

// passthroughConfig is the default pipeline: OTLP in, OTLP out, no processors.
// It exercises the harness end to end without changing the signal, so the only
// invariant it can break is span loss.
const passthroughConfig = `receivers:
  otlp:
    protocols:
      http:
        endpoint: 127.0.0.1:{{.OTLPHTTPPort}}
exporters:
  otlphttp:
    endpoint: {{.SinkURL}}
    compression: none
extensions:
  health_check:
    endpoint: 127.0.0.1:{{.HealthPort}}
service:
  extensions: [health_check]
  pipelines:
    traces:
      receivers: [otlp]
      exporters: [otlphttp]
  telemetry:
    metrics:
      level: none
    logs:
      level: warn
`

// configParams are the template fields the harness fills in. A custom config
// passed to Start may reference any of them.
type configParams struct {
	OTLPHTTPPort int
	HealthPort   int
	SinkURL      string
}

// Collector is a running OpenTelemetry Collector subprocess.
type Collector struct {
	// OTLPEndpoint is the host:port of the collector's OTLP/HTTP receiver,
	// ready for an otlptracehttp exporter.
	OTLPEndpoint string

	cmd        *exec.Cmd
	stderrPath string
	done       chan struct{}
	waitErr    error
}

// Start launches a collector that forwards traces to sink and waits until it
// reports healthy. The config is a text/template; pass "" for a pass-through
// pipeline. Available template fields: OTLPHTTPPort, HealthPort, SinkURL.
//
// The caller owns the returned Collector and must call Stop. If no collector
// binary is available, Start returns ErrNoCollector.
func Start(sink *Sink, config string) (*Collector, error) {
	bin, ok := CollectorBinary()
	if !ok {
		return nil, ErrNoCollector
	}
	if config == "" {
		config = passthroughConfig
	}

	otlpPort, err := freePort()
	if err != nil {
		return nil, fmt.Errorf("allocate OTLP port: %w", err)
	}
	healthPort, err := freePort()
	if err != nil {
		return nil, fmt.Errorf("allocate health port: %w", err)
	}

	rendered, err := renderConfig(config, configParams{
		OTLPHTTPPort: otlpPort,
		HealthPort:   healthPort,
		SinkURL:      sink.URL(),
	})
	if err != nil {
		return nil, err
	}

	dir, err := os.MkdirTemp("", "motel-pipelinetest-")
	if err != nil {
		return nil, err
	}
	configPath := filepath.Join(dir, "collector.yaml")
	if err := os.WriteFile(configPath, []byte(rendered), 0o600); err != nil {
		return nil, err
	}
	stderrPath := filepath.Join(dir, "collector.stderr")
	stderr, err := os.Create(stderrPath)
	if err != nil {
		return nil, err
	}

	cmd := exec.Command(bin, "--config", configPath) //nolint:gosec // binary path is operator-controlled
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		_ = stderr.Close()
		return nil, fmt.Errorf("start collector: %w", err)
	}

	c := &Collector{
		OTLPEndpoint: fmt.Sprintf("127.0.0.1:%d", otlpPort),
		cmd:          cmd,
		stderrPath:   stderrPath,
		done:         make(chan struct{}),
	}
	go func() {
		c.waitErr = cmd.Wait()
		_ = stderr.Close()
		close(c.done)
	}()

	if err := c.awaitReady(healthPort); err != nil {
		_ = c.Stop()
		return nil, err
	}
	return c, nil
}

// awaitReady polls the health_check extension until it reports 200, the
// process exits, or the timeout elapses.
func (c *Collector) awaitReady(healthPort int) error {
	healthURL := fmt.Sprintf("http://127.0.0.1:%d/", healthPort)
	client := &http.Client{Timeout: time.Second}
	deadline := time.Now().Add(15 * time.Second)

	for time.Now().Before(deadline) {
		select {
		case <-c.done:
			return fmt.Errorf("collector exited during startup: %w\n%s", c.waitErr, c.stderrTail())
		default:
		}

		resp, err := client.Get(healthURL)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("collector not ready within timeout\n%s", c.stderrTail())
}

// Stop terminates the collector and waits for it to exit.
func (c *Collector) Stop() error {
	if c.cmd.Process != nil {
		_ = c.cmd.Process.Kill()
	}
	<-c.done
	if c.stderrPath != "" {
		_ = os.RemoveAll(filepath.Dir(c.stderrPath))
	}
	return nil
}

// stderrTail returns the collector's captured stderr for diagnostics.
func (c *Collector) stderrTail() string {
	b, err := os.ReadFile(c.stderrPath)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func renderConfig(config string, params configParams) (string, error) {
	tmpl, err := template.New("collector").Parse(config)
	if err != nil {
		return "", fmt.Errorf("parse collector config template: %w", err)
	}
	var sb strings.Builder
	if err := tmpl.Execute(&sb, params); err != nil {
		return "", fmt.Errorf("render collector config: %w", err)
	}
	return sb.String(), nil
}

// freePort returns a loopback TCP port that was free at the time of the call.
// There is an inherent race between closing the probe listener and the
// collector binding the port, but it is small and acceptable for a test
// harness.
func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer func() { _ = l.Close() }()
	return l.Addr().(*net.TCPAddr).Port, nil
}
