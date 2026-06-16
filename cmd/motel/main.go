// Synthetic OpenTelemetry generator
// Reads a YAML topology definition and emits traces, metrics, and logs via OTel SDK
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"math/rand/v2"
	"net"
	"net/http"
	_ "net/http/pprof" //nolint:gosec // pprof endpoint is opt-in via --pprof flag
	"net/url"
	"os"
	"os/signal"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/andrewh/motel/pkg/semconv"
	"github.com/andrewh/motel/pkg/synth"
	"github.com/andrewh/motel/pkg/synth/traceimport"
	"github.com/spf13/cobra"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploggrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/exporters/stdout/stdoutlog"
	"go.opentelemetry.io/otel/exporters/stdout/stdoutmetric"
	"go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	"go.opentelemetry.io/otel/log"
	"go.opentelemetry.io/otel/metric"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	otelsc "go.opentelemetry.io/otel/semconv/v1.39.0"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

var (
	version   = "dev"
	commit    = "unknown"
	buildTime = "unknown"
)

func main() {
	if err := rootCmd().Execute(); err != nil {
		os.Exit(1)
	}
}

func rootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:          "motel",
		Short:        "Synthetic OpenTelemetry generator",
		SilenceUsage: true,
	}

	root.AddCommand(runCmd())
	root.AddCommand(emitCmd())
	root.AddCommand(doctorCmd())
	root.AddCommand(validateCmd())
	root.AddCommand(importCmd())
	root.AddCommand(previewCmd())
	root.AddCommand(checkCmd())
	root.AddCommand(versionCmd())

	return root
}

func runCmd() *cobra.Command {
	var (
		endpoint         string
		stdout           bool
		duration         time.Duration
		protocol         string
		headers          string
		insecure         bool
		exportTimeout    time.Duration
		signals          string
		slowThreshold    time.Duration
		maxSpansPerTrace int
		semconvDir       string
		labelScenarios   bool
		pprofAddr        string
		timeOffset       time.Duration
		realtime         bool
		seed             uint64
	)

	cmd := &cobra.Command{
		Use:   "run <topology.yaml | URL>",
		Short: "Generate synthetic signals from a topology definition",
		Long: "Generate synthetic signals from a topology definition.\n\n" +
			"The topology source can be a local file path or an HTTP/HTTPS URL.\n" +
			"URL fetches have a 10-second timeout and a 10 MB response body limit.",
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return fmt.Errorf("missing topology file or URL\n\nUsage: motel run <topology.yaml | URL>")
			}
			return cobra.ExactArgs(1)(cmd, args)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			if cmd.Flags().Changed("slow-threshold") && !strings.Contains(signals, "logs") {
				_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "Warning: --slow-threshold has no effect without --signals logs")
			}
			if realtime && cmd.Flags().Changed("time-offset") {
				return fmt.Errorf("--realtime and --time-offset cannot be used together")
			}
			return runGenerate(cmd.Context(), args[0], runOptions{
				endpoint:         endpoint,
				endpointSet:      cmd.Flags().Changed("endpoint"),
				stdout:           stdout,
				duration:         duration,
				protocol:         protocol,
				protocolSet:      cmd.Flags().Changed("protocol"),
				headers:          headers,
				headersSet:       cmd.Flags().Changed("headers"),
				insecure:         insecure,
				insecureSet:      cmd.Flags().Changed("insecure"),
				exportTimeout:    exportTimeout,
				timeoutSet:       cmd.Flags().Changed("timeout"),
				signals:          signals,
				slowThreshold:    slowThreshold,
				maxSpansPerTrace: maxSpansPerTrace,
				semconvDir:       semconvDir,
				labelScenarios:   labelScenarios,
				pprofAddr:        pprofAddr,
				timeOffset:       timeOffset,
				realtime:         realtime,
				seed:             seed,
			})
		},
	}

	cmd.Flags().StringVar(&endpoint, "endpoint", "", "OTLP endpoint (overrides OTEL_EXPORTER_OTLP_ENDPOINT)")
	cmd.Flags().BoolVar(&stdout, "stdout", false, "emit signals to stdout as JSON")
	cmd.Flags().DurationVar(&duration, "duration", 0, "simulation duration, e.g. 10s, 5m, 1h (default 1m)")
	cmd.Flags().StringVar(&protocol, "protocol", "http/protobuf", "OTLP protocol: http/protobuf or grpc (overrides OTEL_EXPORTER_OTLP_PROTOCOL)")
	cmd.Flags().StringVar(&headers, "headers", "", "OTLP headers as comma-separated key=value pairs (overrides OTEL_EXPORTER_OTLP_HEADERS)")
	cmd.Flags().BoolVar(&insecure, "insecure", false, "disable TLS for OTLP exporters")
	cmd.Flags().DurationVar(&exportTimeout, "timeout", 0, "OTLP export timeout (overrides OTEL_EXPORTER_OTLP_TIMEOUT)")
	cmd.Flags().StringVar(&signals, "signals", "traces", "comma-separated signals to emit: traces,metrics,logs")
	cmd.Flags().DurationVar(&slowThreshold, "slow-threshold", time.Second, "duration threshold for slow span log emission")
	cmd.Flags().IntVar(&maxSpansPerTrace, "max-spans-per-trace", 0, "maximum spans per trace (0 = default 10000)")
	cmd.Flags().StringVar(&semconvDir, "semconv", "", "directory of additional semantic convention YAML files")
	cmd.Flags().BoolVar(&labelScenarios, "label-scenarios", false, "add synth.scenarios attribute to spans with active scenario names")
	cmd.Flags().StringVar(&pprofAddr, "pprof", "", "start pprof HTTP server on this address (e.g. :6060)")
	cmd.Flags().DurationVar(&timeOffset, "time-offset", 0, "shift span, metric, and log timestamps by this duration (e.g. -1h for past, 1h for future)")
	cmd.Flags().BoolVar(&realtime, "realtime", false, "emit spans at wall-clock times matching simulated timestamps")
	cmd.Flags().Uint64Var(&seed, "seed", 0, "seed for deterministic simulation decisions (0 = random); determinism is best-effort and not guaranteed across motel versions")

	return cmd
}

func emitCmd() *cobra.Command {
	var (
		service       string
		operation     string
		spanDuration  time.Duration
		duration      time.Duration
		errorRate     string
		attrs         []string
		count         int
		rate          string
		endpoint      string
		stdout        bool
		protocol      string
		headers       string
		insecure      bool
		exportTimeout time.Duration
	)

	cmd := &cobra.Command{
		Use:   "emit",
		Short: "Emit traces from inline arguments without a topology file",
		Long: "Emit one or more single-span traces from command-line arguments.\n\n" +
			"For multi-service topologies or call graphs, use 'motel run' with a YAML file.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if service == "" {
				return fmt.Errorf("--service is required")
			}
			if operation == "" {
				return fmt.Errorf("--operation is required")
			}
			if count == 0 && duration == 0 {
				return nil
			}

			// Parse --attr key=value pairs
			opAttrs := make(map[string]synth.AttributeValueConfig, len(attrs))
			for _, a := range attrs {
				k, v, ok := strings.Cut(a, "=")
				if !ok {
					return fmt.Errorf("--attr %q must be in key=value format", a)
				}
				opAttrs[k] = synth.AttributeValueConfig{Value: v}
			}

			// Build a Config programmatically
			cfg := &synth.Config{
				Version: synth.CurrentVersion,
				Services: []synth.ServiceConfig{
					{
						Name: service,
						Operations: []synth.OperationConfig{
							{
								Name:       operation,
								Duration:   spanDuration.String(),
								ErrorRate:  errorRate,
								Attributes: opAttrs,
							},
						},
					},
				},
				Traffic: synth.TrafficConfig{Rate: rate},
			}

			if err := synth.ValidateConfig(cfg); err != nil {
				return err
			}
			topo, err := synth.BuildTopology(cfg, nil)
			if err != nil {
				return err
			}
			traffic, err := synth.NewTrafficPattern(cfg.Traffic)
			if err != nil {
				return err
			}

			opts := runOptions{
				endpoint:      endpoint,
				endpointSet:   cmd.Flags().Changed("endpoint"),
				stdout:        stdout,
				protocol:      protocol,
				protocolSet:   cmd.Flags().Changed("protocol"),
				headers:       headers,
				headersSet:    cmd.Flags().Changed("headers"),
				insecure:      insecure,
				insecureSet:   cmd.Flags().Changed("insecure"),
				exportTimeout: exportTimeout,
				timeoutSet:    cmd.Flags().Changed("timeout"),
			}

			if err := validateProtocol(opts.protocol); err != nil {
				return err
			}

			if !opts.stdout {
				if err := checkEndpointForEmit(opts); err != nil {
					return err
				}
			}

			baseRes, err := resource.Merge(resource.Default(), resource.NewSchemaless(
				attribute.String("motel.version", version),
			))
			if err != nil {
				return fmt.Errorf("creating resource: %w", err)
			}

			serviceResources := map[string]*resource.Resource{}
			svcRes, err := resource.Merge(baseRes, resource.NewSchemaless(
				attribute.String("service.name", service),
			))
			if err != nil {
				return fmt.Errorf("creating resource: %w", err)
			}
			serviceResources[service] = svcRes

			traceProviders, shutdownTraces, err := createTraceProviders(cmd.Context(), opts, true, serviceResources)
			if err != nil {
				return fmt.Errorf("creating trace providers: %w", err)
			}
			defer shutdownTraces()

			tracers, err := tracerSource(topo, traceProviders)
			if err != nil {
				return err
			}

			engineDuration := unlimitedDuration
			maxTraces := count
			if duration > 0 {
				engineDuration = duration
				if !cmd.Flags().Changed("count") {
					maxTraces = 0
				}
			}

			engine := &synth.Engine{
				Topology:  topo,
				Traffic:   traffic,
				Tracers:   tracers,
				Rng:       rand.New(rand.NewPCG(rand.Uint64(), rand.Uint64())), //nolint:gosec // synthetic data, not security-sensitive
				Duration:  engineDuration,
				MaxTraces: maxTraces,
			}

			ctx, stop := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
			defer stop()

			stats, err := engine.Run(ctx)
			if err != nil {
				return err
			}

			return json.NewEncoder(cmd.ErrOrStderr()).Encode(stats)
		},
	}

	cmd.Flags().StringVar(&service, "service", "", "service name (required)")
	cmd.Flags().StringVar(&operation, "operation", "", "operation name (required)")
	cmd.Flags().DurationVar(&spanDuration, "span-duration", 100*time.Millisecond, "span duration")
	cmd.Flags().DurationVar(&duration, "duration", 0, "simulation duration, e.g. 10s, 5m, 1h")
	cmd.Flags().StringVar(&errorRate, "error-rate", "", "error rate (e.g. 5%, 0.05)")
	cmd.Flags().StringArrayVar(&attrs, "attr", nil, "span attribute in key=value format (repeatable)")
	cmd.Flags().IntVar(&count, "count", 1, "number of traces to emit")
	cmd.Flags().StringVar(&rate, "rate", "10/s", "trace rate when count > 1 (e.g. 10/s, 100/m)")
	cmd.Flags().StringVar(&endpoint, "endpoint", "", "OTLP endpoint (overrides OTEL_EXPORTER_OTLP_ENDPOINT)")
	cmd.Flags().BoolVar(&stdout, "stdout", false, "emit signals to stdout as JSON")
	cmd.Flags().StringVar(&protocol, "protocol", "http/protobuf", "OTLP protocol: http/protobuf or grpc (overrides OTEL_EXPORTER_OTLP_PROTOCOL)")
	cmd.Flags().StringVar(&headers, "headers", "", "OTLP headers as comma-separated key=value pairs (overrides OTEL_EXPORTER_OTLP_HEADERS)")
	cmd.Flags().BoolVar(&insecure, "insecure", false, "disable TLS for OTLP exporters")
	cmd.Flags().DurationVar(&exportTimeout, "timeout", 0, "OTLP export timeout (overrides OTEL_EXPORTER_OTLP_TIMEOUT)")

	return cmd
}

func doctorCmd() *cobra.Command {
	var (
		endpoint      string
		protocol      string
		headers       string
		insecure      bool
		exportTimeout time.Duration
	)

	cmd := &cobra.Command{
		Use:     "doctor",
		Aliases: []string{"status"},
		Short:   "Diagnose OTLP exporter configuration",
		RunE: func(cmd *cobra.Command, args []string) error {
			opts := runOptions{
				endpoint:      endpoint,
				endpointSet:   cmd.Flags().Changed("endpoint"),
				protocol:      protocol,
				protocolSet:   cmd.Flags().Changed("protocol"),
				headers:       headers,
				headersSet:    cmd.Flags().Changed("headers"),
				insecure:      insecure,
				insecureSet:   cmd.Flags().Changed("insecure"),
				exportTimeout: exportTimeout,
				timeoutSet:    cmd.Flags().Changed("timeout"),
			}
			return runDoctor(cmd.Context(), cmd.OutOrStdout(), opts)
		},
	}
	cmd.Flags().StringVar(&endpoint, "endpoint", "", "OTLP endpoint (overrides OTEL_EXPORTER_OTLP_ENDPOINT)")
	cmd.Flags().StringVar(&protocol, "protocol", "http/protobuf", "OTLP protocol: http/protobuf or grpc (overrides OTEL_EXPORTER_OTLP_PROTOCOL)")
	cmd.Flags().StringVar(&headers, "headers", "", "OTLP headers as comma-separated key=value pairs (overrides OTEL_EXPORTER_OTLP_HEADERS)")
	cmd.Flags().BoolVar(&insecure, "insecure", false, "disable TLS for OTLP exporters")
	cmd.Flags().DurationVar(&exportTimeout, "timeout", 0, "OTLP export timeout (overrides OTEL_EXPORTER_OTLP_TIMEOUT)")
	return cmd
}

func runDoctor(ctx context.Context, out io.Writer, opts runOptions) error {
	cfg, err := resolveOTLPConfig(opts, "traces")
	if err != nil {
		return err
	}
	resolved, err := resolveEndpoint(cfg.endpoint, cfg.protocol)
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(out, "OTLP endpoint: %s\n", resolved.hostPort)
	_, _ = fmt.Fprintf(out, "OTLP protocol: %s\n", cfg.protocol)
	_, _ = fmt.Fprintf(out, "OTLP insecure: %t\n", cfg.insecure)
	if cfg.timeout > 0 {
		_, _ = fmt.Fprintf(out, "OTLP timeout: %s\n", cfg.timeout)
	}
	for _, key := range slices.Sorted(maps.Keys(cfg.headers)) {
		_, _ = fmt.Fprintf(out, "OTLP header: %s=%s\n", key, redactValue(cfg.headers[key]))
	}

	if _, err := dialEndpoint(cfg.endpoint, cfg.protocol); err != nil {
		return fmt.Errorf("OTLP TCP check failed for %s: %w\n\nCheck the endpoint host/port, protocol port (4318 for http/protobuf, 4317 for grpc), TLS mode, and firewall rules", resolved.hostPort, err)
	}
	_, _ = fmt.Fprintln(out, "TCP check: ok")

	res, err := resource.Merge(resource.Default(), resource.NewSchemaless(attribute.String("service.name", "motel-doctor")))
	if err != nil {
		return fmt.Errorf("creating resource: %w", err)
	}
	providers, shutdown, err := createTraceProviders(ctx, opts, true, map[string]*resource.Resource{"motel-doctor": res})
	if err != nil {
		return fmt.Errorf("creating trace exporter: %w", err)
	}
	tr := providers["motel-doctor"].Tracer("motel-doctor")
	spanCtx, span := tr.Start(ctx, "motel.doctor.canary")
	span.End()
	shutdown()
	_, _ = fmt.Fprintf(out, "Canary trace: sent trace_id=%s span_id=%s\n", span.SpanContext().TraceID(), span.SpanContext().SpanID())
	_ = spanCtx
	return nil
}

func redactValue(value string) string {
	if value == "" {
		return "<empty>"
	}
	return "<redacted>"
}

func checkEndpointForEmit(opts runOptions) error {
	cfg, err := resolveOTLPConfig(opts, "traces")
	if err != nil {
		return err
	}
	host, err := dialEndpoint(cfg.endpoint, cfg.protocol)
	if err != nil {
		return fmt.Errorf("cannot reach OTLP collector at %s\n\n"+
			"To emit signals as JSON to the terminal, use --stdout:\n"+
			"  motel emit --service myapp --operation request --stdout\n\n"+
			"To send to a specific collector, use --endpoint:\n"+
			"  motel emit --service myapp --operation request --endpoint collector.example.com:4318", host)
	}
	return nil
}

func validateCmd() *cobra.Command {
	var semconvDir string

	cmd := &cobra.Command{
		Use:   "validate <topology.yaml | URL>",
		Short: "Parse and validate a topology configuration",
		Long: "Parse and validate a topology configuration.\n\n" +
			"The topology source can be a local file path or an HTTP/HTTPS URL.\n" +
			"URL fetches have a 10-second timeout and a 10 MB response body limit.",
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return fmt.Errorf("missing topology file or URL\n\nUsage: motel validate <topology.yaml | URL>")
			}
			return cobra.ExactArgs(1)(cmd, args)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := synth.LoadConfig(args[0])
			if err != nil {
				return err
			}
			if err := synth.ValidateConfig(cfg); err != nil {
				return err
			}
			reg, err := loadRegistry(semconvDir)
			if err != nil {
				return err
			}
			topo, err := synth.BuildTopology(cfg, domainResolver(reg))
			if err != nil {
				return err
			}
			for _, w := range semconvMetricWarnings(cfg, reg) {
				_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "warning: %s\n", w)
			}
			for _, w := range semconvLogWarnings(cfg, reg) {
				_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "warning: %s\n", w)
			}
			svcLabel := "services"
			if len(topo.Services) == 1 {
				svcLabel = "service"
			}
			rootLabel := "operations"
			if len(topo.Roots) == 1 {
				rootLabel = "operation"
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Configuration valid: %d %s, %d root %s\n\n"+
				"To generate signals:\n"+
				"  motel run --stdout %s\n\n"+
				"See https://github.com/andrewh/motel/tree/main/docs/examples for more examples.\n",
				len(topo.Services), svcLabel, len(topo.Roots), rootLabel, args[0])
			return nil
		},
	}

	cmd.Flags().StringVar(&semconvDir, "semconv", "", "directory of additional semantic convention YAML files")

	return cmd
}

func importCmd() *cobra.Command {
	var (
		format           string
		minTraces        int
		metaProfile      string
		metaIncludeEmpty bool
	)

	cmd := &cobra.Command{
		Use:   "import [file]",
		Short: "Import a topology config from trace data",
		Long:  "Reads trace spans or supported summary data and generates a synth YAML topology config.",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var r io.Reader = os.Stdin
			if len(args) == 1 {
				f, err := os.Open(args[0]) //nolint:gosec // user-supplied file path is expected
				if err != nil {
					return fmt.Errorf("opening input: %w", err)
				}
				defer f.Close() //nolint:errcheck // best-effort close on read-only file
				r = f
			}

			result, err := traceimport.Import(r, traceimport.Options{
				Format:           traceimport.Format(format),
				MinTraces:        minTraces,
				Warnings:         cmd.ErrOrStderr(),
				MetaProfile:      metaProfile,
				MetaIncludeEmpty: metaIncludeEmpty,
			})
			if err != nil {
				if strings.Contains(err.Error(), "no spans found") {
					return fmt.Errorf("%w\n\nProvide a file or pipe stdin:\n  motel import traces.json\n  cat traces.json | motel import", err)
				}
				return err
			}

			_, err = cmd.OutOrStdout().Write(result.YAML)
			return err
		},
	}

	cmd.Flags().StringVar(&format, "format", "auto", "input format: auto, stdouttrace, otlp, jaeger, or meta-summary (Meta ATC 2023 parent-data.csv)")
	cmd.Flags().IntVar(&minTraces, "min-traces", 1, "minimum traces for statistical accuracy (warns if fewer)")
	cmd.Flags().StringVar(&metaProfile, "profile", "", "profile filter for --format meta-summary: ads, fetch, or raas")
	cmd.Flags().BoolVar(&metaIncludeEmpty, "include-empty", false, "include empty children_set rows for --format meta-summary")

	return cmd
}

func versionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print version information",
		Run: func(cmd *cobra.Command, args []string) {
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "motel %s (commit: %s, built: %s)\n", version, commit, buildTime)
		},
	}
}

type runOptions struct {
	endpoint         string
	endpointSet      bool
	stdout           bool
	duration         time.Duration
	protocol         string
	protocolSet      bool
	headers          string
	headersSet       bool
	insecure         bool
	insecureSet      bool
	exportTimeout    time.Duration
	timeoutSet       bool
	signals          string
	slowThreshold    time.Duration
	maxSpansPerTrace int
	semconvDir       string
	labelScenarios   bool
	pprofAddr        string
	timeOffset       time.Duration
	realtime         bool
	seed             uint64
}

type otlpConfig struct {
	endpoint string
	protocol string
	headers  map[string]string
	insecure bool
	timeout  time.Duration
}

const (
	envOTLPEndpoint = "OTEL_EXPORTER_OTLP_ENDPOINT"
	envOTLPProtocol = "OTEL_EXPORTER_OTLP_PROTOCOL"
	envOTLPHeaders  = "OTEL_EXPORTER_OTLP_HEADERS"
	envOTLPTimeout  = "OTEL_EXPORTER_OTLP_TIMEOUT"
)

func signalEnv(signal, suffix string) string {
	return "OTEL_EXPORTER_OTLP_" + strings.ToUpper(signal) + "_" + suffix
}

func envFirst(names ...string) string {
	for _, name := range names {
		if value := strings.TrimSpace(os.Getenv(name)); value != "" {
			return value
		}
	}
	return ""
}

func resolveOTLPConfig(opts runOptions, signal string) (otlpConfig, error) {
	cfg := otlpConfig{protocol: "http/protobuf"}
	if opts.protocolSet {
		cfg.protocol = opts.protocol
	} else if protocol := envFirst(signalEnv(signal, "PROTOCOL"), envOTLPProtocol); protocol != "" {
		cfg.protocol = protocol
	} else {
		cfg.protocol = opts.protocol
	}
	if err := validateProtocol(cfg.protocol); err != nil {
		return otlpConfig{}, err
	}

	if opts.endpointSet {
		cfg.endpoint = opts.endpoint
	} else {
		cfg.endpoint = envFirst(signalEnv(signal, "ENDPOINT"), envOTLPEndpoint)
	}

	headerValue := ""
	if opts.headersSet {
		headerValue = opts.headers
	} else {
		headerValue = envFirst(signalEnv(signal, "HEADERS"), envOTLPHeaders)
	}
	headers, err := parseOTLPHeaders(headerValue)
	if err != nil {
		return otlpConfig{}, err
	}
	cfg.headers = headers

	if opts.insecureSet {
		cfg.insecure = opts.insecure
	} else if insecure := envFirst(signalEnv(signal, "INSECURE"), "OTEL_EXPORTER_OTLP_INSECURE"); insecure != "" {
		parsed, parseErr := strconv.ParseBool(insecure)
		if parseErr != nil {
			return otlpConfig{}, fmt.Errorf("invalid OTLP insecure value %q: %w", insecure, parseErr)
		}
		cfg.insecure = parsed
	}

	if opts.timeoutSet {
		cfg.timeout = opts.exportTimeout
	} else if timeoutValue := envFirst(signalEnv(signal, "TIMEOUT"), envOTLPTimeout); timeoutValue != "" {
		timeout, parseErr := parseOTLPTimeout(timeoutValue)
		if parseErr != nil {
			return otlpConfig{}, parseErr
		}
		cfg.timeout = timeout
	}
	return cfg, nil
}

func parseOTLPHeaders(value string) (map[string]string, error) {
	if strings.TrimSpace(value) == "" {
		return nil, nil
	}
	headers := map[string]string{}
	for _, part := range strings.Split(value, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		key, val, ok := strings.Cut(part, "=")
		if !ok || strings.TrimSpace(key) == "" {
			return nil, fmt.Errorf("OTLP header %q must be in key=value format", part)
		}
		headers[strings.TrimSpace(key)] = strings.TrimSpace(val)
	}
	return headers, nil
}

func parseOTLPTimeout(value string) (time.Duration, error) {
	if ms, err := strconv.Atoi(value); err == nil {
		return time.Duration(ms) * time.Millisecond, nil
	}
	d, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("invalid OTLP timeout %q: use a duration like 5s or milliseconds like 5000", value)
	}
	return d, nil
}

// RNG streams for seeded runs. Each consumer of randomness gets a fixed
// stream of the same seed so that enabling or disabling one signal does
// not perturb the sequences of the others.
const (
	rngStreamEngine  = 1
	rngStreamMetrics = 2
	rngStreamLogs    = 3
)

// newRunRng returns the RNG for one consumer of randomness during a run.
// With a non-zero seed the RNG is deterministic on the given stream;
// with seed 0 it is independently random.
func newRunRng(seed, stream uint64) *rand.Rand {
	if seed == 0 {
		return rand.New(rand.NewPCG(rand.Uint64(), rand.Uint64())) //nolint:gosec // synthetic data, not security-sensitive
	}
	return rand.New(rand.NewPCG(seed, stream)) //nolint:gosec // synthetic data, not security-sensitive
}

var validSignals = map[string]bool{
	"traces":  true,
	"metrics": true,
	"logs":    true,
}

var validProtocols = map[string]bool{
	"http/protobuf": true,
	"grpc":          true,
}

func validateProtocol(p string) error {
	if !validProtocols[p] {
		return fmt.Errorf("unsupported protocol %q, supported: http/protobuf, grpc", p)
	}
	return nil
}

func parseSignals(s string) (map[string]bool, error) {
	set := make(map[string]bool)
	for _, sig := range strings.Split(s, ",") {
		sig = strings.TrimSpace(sig)
		if sig == "" {
			continue
		}
		if !validSignals[sig] {
			return nil, fmt.Errorf("unknown signal %q, valid signals: traces, metrics, logs", sig)
		}
		set[sig] = true
	}
	return set, nil
}

const (
	defaultDuration     = 1 * time.Minute
	unlimitedDuration   = 24 * 365 * time.Hour
	shutdownTimeout     = 5 * time.Second
	connectCheckTimeout = 2 * time.Second
	defaultHTTPPort     = "4318"
	defaultGRPCPort     = "4317"
)

type resolvedEndpoint struct {
	hostPort    string
	endpointURL string
}

func defaultPort(protocol string) string {
	if protocol == "grpc" {
		return defaultGRPCPort
	}
	return defaultHTTPPort
}

func isEndpointURL(endpoint string) bool {
	return strings.Contains(endpoint, "://")
}

func resolveEndpoint(endpoint, protocol string) (resolvedEndpoint, error) {
	if endpoint == "" {
		return resolvedEndpoint{hostPort: net.JoinHostPort("localhost", defaultPort(protocol))}, nil
	}

	if isEndpointURL(endpoint) {
		u, err := url.Parse(endpoint)
		if err != nil {
			return resolvedEndpoint{}, fmt.Errorf("invalid endpoint URL %q: %w", endpoint, err)
		}
		if u.Scheme != "http" && u.Scheme != "https" {
			return resolvedEndpoint{}, fmt.Errorf("endpoint URL %q: scheme must be http or https", endpoint)
		}
		if u.Host == "" {
			return resolvedEndpoint{}, fmt.Errorf("endpoint URL %q: host is required", endpoint)
		}
		hostPort := u.Host
		if _, _, err := net.SplitHostPort(hostPort); err != nil {
			host := u.Hostname()
			if host == "" {
				return resolvedEndpoint{}, fmt.Errorf("endpoint URL %q: host is required", endpoint)
			}
			hostPort = net.JoinHostPort(host, defaultPort(protocol))
			u.Host = hostPort
		}
		return resolvedEndpoint{hostPort: hostPort, endpointURL: u.String()}, nil
	}

	if _, _, err := net.SplitHostPort(endpoint); err == nil {
		return resolvedEndpoint{hostPort: endpoint}, nil
	}
	return resolvedEndpoint{hostPort: net.JoinHostPort(endpoint, defaultPort(protocol))}, nil
}

func dialEndpoint(endpoint, protocol string) (string, error) {
	resolved, err := resolveEndpoint(endpoint, protocol)
	if err != nil {
		return endpoint, err
	}
	conn, err := net.DialTimeout("tcp", resolved.hostPort, connectCheckTimeout)
	if err != nil {
		return resolved.hostPort, err
	}
	_ = conn.Close()
	return resolved.hostPort, nil
}

func checkEndpoint(opts runOptions, configPath string) error {
	cfg, err := resolveOTLPConfig(opts, "traces")
	if err != nil {
		return err
	}
	host, err := dialEndpoint(cfg.endpoint, cfg.protocol)
	if err != nil {
		return fmt.Errorf("cannot reach OTLP collector at %s\n\n"+
			"To emit signals as JSON to the terminal, use --stdout:\n"+
			"  motel run --stdout --duration 10s %s\n\n"+
			"To send to a specific collector, use --endpoint:\n"+
			"  motel run --endpoint collector.example.com:4318 %s\n\n"+
			"Without --duration, motel runs for 1 minute", host, configPath, configPath)
	}
	return nil
}

func runGenerate(ctx context.Context, configPath string, opts runOptions) error {
	if opts.pprofAddr != "" {
		pprofListener, listenErr := net.Listen("tcp", opts.pprofAddr)
		if listenErr != nil {
			return fmt.Errorf("starting pprof server: %w", listenErr)
		}

		pprofServer := &http.Server{Addr: opts.pprofAddr, Handler: http.DefaultServeMux}
		go func() {
			fmt.Fprintf(os.Stderr, "pprof server listening on %s\n", pprofListener.Addr())
			if err := pprofServer.Serve(pprofListener); err != nil && err != http.ErrServerClosed { //nolint:gosec // pprof server is opt-in via flag
				fmt.Fprintf(os.Stderr, "pprof server error: %v\n", err)
			}
		}()
		defer func() {
			shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
			defer cancel()
			if err := pprofServer.Shutdown(shutdownCtx); err != nil {
				fmt.Fprintf(os.Stderr, "pprof server shutdown error: %v\n", err)
			}
		}()
	}

	cfg, err := synth.LoadConfig(configPath)
	if err != nil {
		return err
	}
	if err := synth.ValidateConfig(cfg); err != nil {
		return err
	}
	topo, err := buildTopology(cfg, opts.semconvDir)
	if err != nil {
		return err
	}
	traffic, err := synth.NewTrafficPattern(cfg.Traffic)
	if err != nil {
		return err
	}
	scenarios, err := synth.BuildScenarios(cfg.Scenarios, topo)
	if err != nil {
		return err
	}

	if opts.slowThreshold < 0 {
		return fmt.Errorf("--slow-threshold must not be negative, got %s", opts.slowThreshold)
	}

	enabledSignals, err := parseSignals(opts.signals)
	if err != nil {
		return err
	}

	if err := validateProtocol(opts.protocol); err != nil {
		return err
	}

	if !opts.stdout {
		if err := checkEndpoint(opts, configPath); err != nil {
			return err
		}
	}

	baseRes, err := resource.Merge(resource.Default(), resource.NewSchemaless(
		attribute.String("motel.version", version),
	))
	if err != nil {
		return fmt.Errorf("creating resource: %w", err)
	}

	// Build per-service resources and create signal providers.
	// Each service gets its own providers with the correct service.name resource.
	// Providers within each signal share a single exporter and processor.
	serviceResources := make(map[string]*resource.Resource, len(topo.Services))
	for name, svc := range topo.Services {
		attrs := make([]attribute.KeyValue, 0, 1+len(svc.ResourceAttributes))
		attrs = append(attrs, attribute.String("service.name", name))
		for k, v := range svc.ResourceAttributes {
			attrs = append(attrs, attribute.String(k, v))
		}
		svcRes, resErr := resource.Merge(baseRes, resource.NewSchemaless(attrs...))
		if resErr != nil {
			return fmt.Errorf("creating resource for service %s: %w", name, resErr)
		}
		serviceResources[name] = svcRes
	}

	traceProviders, shutdownTraces, err := createTraceProviders(ctx, opts, enabledSignals["traces"], serviceResources)
	if err != nil {
		return fmt.Errorf("creating trace providers: %w", err)
	}
	defer shutdownTraces()

	tracers, err := tracerSource(topo, traceProviders)
	if err != nil {
		return err
	}

	var observers []synth.SpanObserver

	if enabledSignals["metrics"] {
		if !topoHasMetrics(topo) {
			fmt.Fprintln(os.Stderr, "warning: --signals includes metrics but the topology defines no metric instruments; no metric data will be emitted. Add a metrics: section to at least one service or operation.")
		}
		meters, shutdownMetrics, mErr := createMetricProviders(ctx, opts, serviceResources)
		if mErr != nil {
			return fmt.Errorf("creating metric providers: %w", mErr)
		}
		defer shutdownMetrics()
		obs, mErr := synth.NewMetricObserver(meters, topo, newRunRng(opts.seed, rngStreamMetrics))
		if mErr != nil {
			return fmt.Errorf("creating metric observer: %w", mErr)
		}
		stopIntervals := obs.Start()
		defer stopIntervals()
		observers = append(observers, obs)
	}

	if enabledSignals["logs"] {
		loggers, shutdownLogs, lErr := createLogProviders(ctx, opts, serviceResources)
		if lErr != nil {
			return fmt.Errorf("creating log providers: %w", lErr)
		}
		defer shutdownLogs()
		obs, lErr := synth.NewLogObserver(loggers, topo, opts.slowThreshold, newRunRng(opts.seed, rngStreamLogs))
		if lErr != nil {
			return fmt.Errorf("creating log observer: %w", lErr)
		}
		observers = append(observers, obs)
	}

	duration := opts.duration
	if duration == 0 {
		duration = defaultDuration
	}

	engine := &synth.Engine{
		Topology:         topo,
		Traffic:          traffic,
		Scenarios:        scenarios,
		Tracers:          tracers,
		Rng:              newRunRng(opts.seed, rngStreamEngine),
		Duration:         duration,
		Observers:        observers,
		MaxSpansPerTrace: opts.maxSpansPerTrace,
		State:            synth.NewSimulationState(topo),
		LabelScenarios:   opts.labelScenarios,
		TimeOffset:       opts.timeOffset,
		Realtime:         opts.realtime,
	}

	// Handle OS signals for graceful shutdown
	ctx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	stats, err := engine.Run(ctx)
	if err != nil {
		return err
	}

	return json.NewEncoder(os.Stderr).Encode(stats)
}

func tracerSource(topo *synth.Topology, providers map[string]*sdktrace.TracerProvider) (synth.TracerSource, error) {
	for name := range topo.Services {
		if providers[name] == nil {
			return nil, fmt.Errorf("missing tracer provider for service %q", name)
		}
	}

	missingProvider := noop.NewTracerProvider()
	return func(name string) trace.Tracer {
		provider := providers[name]
		if provider == nil {
			return missingProvider.Tracer("github.com/andrewh/motel")
		}

		return provider.Tracer("github.com/andrewh/motel",
			trace.WithInstrumentationVersion(version),
			trace.WithSchemaURL(otelsc.SchemaURL),
			trace.WithInstrumentationAttributes(
				attribute.Bool("motel.synthetic", true),
			),
		)
	}, nil
}

// createTraceProviders creates one TracerProvider per service sharing a single exporter
// and processor. Returns a map of service name → provider and a shutdown function.
func createTraceProviders(ctx context.Context, opts runOptions, enabled bool, resources map[string]*resource.Resource) (map[string]*sdktrace.TracerProvider, func(), error) {
	providers := make(map[string]*sdktrace.TracerProvider, len(resources))
	noopShutdown := func() {}

	if !enabled {
		noopTP := sdktrace.NewTracerProvider()
		for name := range resources {
			providers[name] = noopTP
		}
		return providers, func() {
			_ = noopTP.Shutdown(context.Background())
		}, nil
	}

	exporter, err := createTraceExporter(ctx, opts)
	if err != nil {
		return nil, noopShutdown, err
	}

	var sp sdktrace.SpanProcessor
	if opts.stdout {
		sp = sdktrace.NewSimpleSpanProcessor(exporter)
	} else {
		sp = sdktrace.NewBatchSpanProcessor(exporter)
	}

	for name, res := range resources {
		providers[name] = sdktrace.NewTracerProvider(
			sdktrace.WithSpanProcessor(sp),
			sdktrace.WithResource(res),
		)
	}

	shutdown := func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		shutdownAll(shutdownCtx, slices.Collect(maps.Values(providers)), "tracer provider")
	}
	return providers, shutdown, nil
}

func createTraceExporter(ctx context.Context, opts runOptions) (sdktrace.SpanExporter, error) {
	if opts.stdout {
		return stdouttrace.New(stdouttrace.WithWriter(os.Stdout))
	}
	cfg, err := resolveOTLPConfig(opts, "traces")
	if err != nil {
		return nil, err
	}
	resolved, err := resolveEndpoint(cfg.endpoint, cfg.protocol)
	if err != nil {
		return nil, err
	}
	switch cfg.protocol {
	case "grpc":
		var grpcOpts []otlptracegrpc.Option
		if cfg.endpoint != "" {
			if resolved.endpointURL != "" {
				grpcOpts = append(grpcOpts, otlptracegrpc.WithEndpointURL(resolved.endpointURL))
			} else {
				grpcOpts = append(grpcOpts, otlptracegrpc.WithEndpoint(resolved.hostPort))
			}
		}
		if cfg.insecure || (cfg.endpoint != "" && resolved.endpointURL == "") {
			grpcOpts = append(grpcOpts, otlptracegrpc.WithInsecure())
		}
		if len(cfg.headers) > 0 {
			grpcOpts = append(grpcOpts, otlptracegrpc.WithHeaders(cfg.headers))
		}
		if cfg.timeout > 0 {
			grpcOpts = append(grpcOpts, otlptracegrpc.WithTimeout(cfg.timeout))
		}
		return otlptracegrpc.New(ctx, grpcOpts...)
	case "http/protobuf", "":
		var httpOpts []otlptracehttp.Option
		if cfg.endpoint != "" {
			if resolved.endpointURL != "" {
				httpOpts = append(httpOpts, otlptracehttp.WithEndpointURL(resolved.endpointURL))
			} else {
				httpOpts = append(httpOpts, otlptracehttp.WithEndpoint(resolved.hostPort))
			}
		}
		if cfg.insecure || (cfg.endpoint != "" && resolved.endpointURL == "") {
			httpOpts = append(httpOpts, otlptracehttp.WithInsecure())
		}
		if len(cfg.headers) > 0 {
			httpOpts = append(httpOpts, otlptracehttp.WithHeaders(cfg.headers))
		}
		if cfg.timeout > 0 {
			httpOpts = append(httpOpts, otlptracehttp.WithTimeout(cfg.timeout))
		}
		return otlptracehttp.New(ctx, httpOpts...)
	default:
		return nil, fmt.Errorf("unsupported protocol %q, supported: http/protobuf, grpc", cfg.protocol)
	}
}

// noopShutdownMetricExporter wraps a metric exporter to ignore Shutdown calls.
// This allows multiple PeriodicReaders to share an exporter without the first
// reader's shutdown closing it for the rest. The real exporter is shut down
// separately after all providers are drained.
type noopShutdownMetricExporter struct {
	sdkmetric.Exporter
}

func (e *noopShutdownMetricExporter) Shutdown(context.Context) error { return nil }

// createMetricProviders creates per-service meters sharing a single exporter.
// Returns a map of service name → Meter and a shutdown function.
func createMetricProviders(ctx context.Context, opts runOptions, resources map[string]*resource.Resource) (map[string]metric.Meter, func(), error) {
	exporter, err := createMetricExporter(ctx, opts)
	if err != nil {
		return nil, func() {}, err
	}

	wrapper := &noopShutdownMetricExporter{synth.NewTimeOffsetMetricExporter(exporter, opts.timeOffset)}
	providers := make([]*sdkmetric.MeterProvider, 0, len(resources))
	meters := make(map[string]metric.Meter, len(resources))

	for name, res := range resources {
		mp := sdkmetric.NewMeterProvider(
			sdkmetric.WithReader(sdkmetric.NewPeriodicReader(wrapper)),
			sdkmetric.WithResource(res),
		)
		providers = append(providers, mp)
		meters[name] = mp.Meter("motel")
	}

	shutdown := func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		shutdownAll(shutdownCtx, providers, "meter provider")
		if err := exporter.Shutdown(shutdownCtx); err != nil {
			fmt.Fprintf(os.Stderr, "error shutting down metric exporter: %v\n", err)
		}
	}
	return meters, shutdown, nil
}

func createMetricExporter(ctx context.Context, opts runOptions) (sdkmetric.Exporter, error) {
	if opts.stdout {
		return stdoutmetric.New(stdoutmetric.WithWriter(os.Stdout))
	}
	cfg, err := resolveOTLPConfig(opts, "metrics")
	if err != nil {
		return nil, err
	}
	resolved, err := resolveEndpoint(cfg.endpoint, cfg.protocol)
	if err != nil {
		return nil, err
	}
	switch cfg.protocol {
	case "grpc":
		var grpcOpts []otlpmetricgrpc.Option
		if cfg.endpoint != "" {
			if resolved.endpointURL != "" {
				grpcOpts = append(grpcOpts, otlpmetricgrpc.WithEndpointURL(resolved.endpointURL))
			} else {
				grpcOpts = append(grpcOpts, otlpmetricgrpc.WithEndpoint(resolved.hostPort))
			}
		}
		if cfg.insecure || (cfg.endpoint != "" && resolved.endpointURL == "") {
			grpcOpts = append(grpcOpts, otlpmetricgrpc.WithInsecure())
		}
		if len(cfg.headers) > 0 {
			grpcOpts = append(grpcOpts, otlpmetricgrpc.WithHeaders(cfg.headers))
		}
		if cfg.timeout > 0 {
			grpcOpts = append(grpcOpts, otlpmetricgrpc.WithTimeout(cfg.timeout))
		}
		return otlpmetricgrpc.New(ctx, grpcOpts...)
	case "http/protobuf", "":
		var httpOpts []otlpmetrichttp.Option
		if cfg.endpoint != "" {
			if resolved.endpointURL != "" {
				httpOpts = append(httpOpts, otlpmetrichttp.WithEndpointURL(resolved.endpointURL))
			} else {
				httpOpts = append(httpOpts, otlpmetrichttp.WithEndpoint(resolved.hostPort))
			}
		}
		if cfg.insecure || (cfg.endpoint != "" && resolved.endpointURL == "") {
			httpOpts = append(httpOpts, otlpmetrichttp.WithInsecure())
		}
		if len(cfg.headers) > 0 {
			httpOpts = append(httpOpts, otlpmetrichttp.WithHeaders(cfg.headers))
		}
		if cfg.timeout > 0 {
			httpOpts = append(httpOpts, otlpmetrichttp.WithTimeout(cfg.timeout))
		}
		return otlpmetrichttp.New(ctx, httpOpts...)
	default:
		return nil, fmt.Errorf("unsupported protocol %q for metrics", cfg.protocol)
	}
}

// createLogProviders creates per-service loggers sharing a single exporter and processor.
// Returns a map of service name → Logger and a shutdown function.
func createLogProviders(ctx context.Context, opts runOptions, resources map[string]*resource.Resource) (map[string]log.Logger, func(), error) {
	exporter, err := createLogExporter(ctx, opts)
	if err != nil {
		return nil, func() {}, err
	}

	var processor sdklog.Processor
	if opts.stdout {
		processor = sdklog.NewSimpleProcessor(exporter)
	} else {
		processor = sdklog.NewBatchProcessor(exporter)
	}

	loggers := make(map[string]log.Logger, len(resources))
	providers := make([]*sdklog.LoggerProvider, 0, len(resources))

	for name, res := range resources {
		lp := sdklog.NewLoggerProvider(
			sdklog.WithProcessor(processor),
			sdklog.WithResource(res),
		)
		providers = append(providers, lp)
		loggers[name] = lp.Logger("motel")
	}

	shutdown := func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		shutdownAll(shutdownCtx, providers, "logger provider")
	}
	return loggers, shutdown, nil
}

func createLogExporter(ctx context.Context, opts runOptions) (sdklog.Exporter, error) {
	if opts.stdout {
		return stdoutlog.New(stdoutlog.WithWriter(os.Stdout))
	}
	cfg, err := resolveOTLPConfig(opts, "logs")
	if err != nil {
		return nil, err
	}
	resolved, err := resolveEndpoint(cfg.endpoint, cfg.protocol)
	if err != nil {
		return nil, err
	}
	switch cfg.protocol {
	case "grpc":
		var grpcOpts []otlploggrpc.Option
		if cfg.endpoint != "" {
			if resolved.endpointURL != "" {
				grpcOpts = append(grpcOpts, otlploggrpc.WithEndpointURL(resolved.endpointURL))
			} else {
				grpcOpts = append(grpcOpts, otlploggrpc.WithEndpoint(resolved.hostPort))
			}
		}
		if cfg.insecure || (cfg.endpoint != "" && resolved.endpointURL == "") {
			grpcOpts = append(grpcOpts, otlploggrpc.WithInsecure())
		}
		if len(cfg.headers) > 0 {
			grpcOpts = append(grpcOpts, otlploggrpc.WithHeaders(cfg.headers))
		}
		if cfg.timeout > 0 {
			grpcOpts = append(grpcOpts, otlploggrpc.WithTimeout(cfg.timeout))
		}
		return otlploggrpc.New(ctx, grpcOpts...)
	case "http/protobuf", "":
		var httpOpts []otlploghttp.Option
		if cfg.endpoint != "" {
			if resolved.endpointURL != "" {
				httpOpts = append(httpOpts, otlploghttp.WithEndpointURL(resolved.endpointURL))
			} else {
				httpOpts = append(httpOpts, otlploghttp.WithEndpoint(resolved.hostPort))
			}
		}
		if cfg.insecure || (cfg.endpoint != "" && resolved.endpointURL == "") {
			httpOpts = append(httpOpts, otlploghttp.WithInsecure())
		}
		if len(cfg.headers) > 0 {
			httpOpts = append(httpOpts, otlploghttp.WithHeaders(cfg.headers))
		}
		if cfg.timeout > 0 {
			httpOpts = append(httpOpts, otlploghttp.WithTimeout(cfg.timeout))
		}
		return otlploghttp.New(ctx, httpOpts...)
	default:
		return nil, fmt.Errorf("unsupported protocol %q for logs", cfg.protocol)
	}
}

// shutdownable is anything with a Shutdown method (TracerProvider, MeterProvider, LoggerProvider).
type shutdownable interface {
	Shutdown(context.Context) error
}

// shutdownAll shuts down all items concurrently within the given context.
// Errors are logged to stderr individually; a slow item does not block others.
func shutdownAll[S shutdownable](ctx context.Context, items []S, label string) {
	var wg sync.WaitGroup
	for _, item := range items {
		wg.Go(func() {
			if err := item.Shutdown(ctx); err != nil {
				fmt.Fprintf(os.Stderr, "error shutting down %s: %v\n", label, err)
			}
		})
	}
	wg.Wait()
}

func buildTopology(cfg *synth.Config, semconvDir string) (*synth.Topology, error) {
	reg, err := loadRegistry(semconvDir)
	if err != nil {
		return nil, err
	}
	return synth.BuildTopology(cfg, domainResolver(reg))
}

// loadRegistry loads the embedded semantic convention registry, merged with
// any additional YAML files from semconvDir.
func loadRegistry(semconvDir string) (*semconv.Registry, error) {
	reg, err := semconv.LoadEmbedded()
	if err != nil {
		return nil, fmt.Errorf("loading semantic conventions: %w", err)
	}
	if semconvDir != "" {
		info, statErr := os.Stat(semconvDir)
		if statErr != nil {
			return nil, fmt.Errorf("--semconv directory %q does not exist", semconvDir)
		}
		if !info.IsDir() {
			return nil, fmt.Errorf("--semconv path %q is not a directory", semconvDir)
		}
		userReg, loadErr := semconv.Load(os.DirFS(semconvDir))
		if loadErr != nil {
			return nil, fmt.Errorf("loading semantic conventions from %s: %w", semconvDir, loadErr)
		}
		reg = reg.Merge(userReg)
	}
	return reg, nil
}

// semconvMetricWarnings checks topology metric definitions whose names match
// known semantic convention metrics, returning warnings for instrument type
// and unit mismatches. Unknown metric names are not warned about — users may
// define custom metrics freely.
func semconvMetricWarnings(cfg *synth.Config, reg *semconv.Registry) []string {
	var warnings []string
	check := func(scope string, mc synth.MetricConfig) {
		g := reg.MetricByName(mc.Name)
		if g == nil {
			return
		}
		if g.Instrument != "" && mc.Type != g.Instrument {
			warnings = append(warnings, fmt.Sprintf("%s: metric %q: type %q does not match semantic convention instrument %q",
				scope, mc.Name, mc.Type, g.Instrument))
		}
		if g.Unit != "" && mc.Unit != g.Unit {
			if mc.Unit == "" {
				warnings = append(warnings, fmt.Sprintf("%s: metric %q: unit is not set; semantic convention specifies %q",
					scope, mc.Name, g.Unit))
			} else {
				warnings = append(warnings, fmt.Sprintf("%s: metric %q: unit %q does not match semantic convention unit %q",
					scope, mc.Name, mc.Unit, g.Unit))
			}
		}
	}
	for _, svc := range cfg.Services {
		for _, mc := range svc.Metrics {
			check(fmt.Sprintf("service %q", svc.Name), mc)
		}
		for _, op := range svc.Operations {
			for _, mc := range op.Metrics {
				check(fmt.Sprintf("service %q operation %q", svc.Name, op.Name), mc)
			}
		}
	}
	return warnings
}

// semconvLogWarnings checks log attribute names in the topology against the
// semantic convention registry, returning warnings for deprecated attributes
// and static values outside a known enum's members. Unknown attribute names
// are not warned about — users may define custom attributes freely.
func semconvLogWarnings(cfg *synth.Config, reg *semconv.Registry) []string {
	var warnings []string
	check := func(scope string, attrs map[string]synth.AttributeValueConfig) {
		for _, name := range slices.Sorted(maps.Keys(attrs)) {
			def := reg.Attribute(name)
			if def == nil {
				continue
			}
			if def.Deprecated != nil {
				warnings = append(warnings, fmt.Sprintf("%s: attribute %q is deprecated in the semantic conventions",
					scope, name))
			}
			if v := attrs[name].Value; v != nil && def.Type.Value == "enum" && !enumAllows(def, v) {
				warnings = append(warnings, fmt.Sprintf("%s: attribute %q: value %v is not a member of the semantic convention enum",
					scope, name, v))
			}
		}
	}
	for _, svc := range cfg.Services {
		for i, lc := range svc.Logs {
			check(fmt.Sprintf("service %q log[%d]", svc.Name, i), lc.Attributes)
		}
		for _, op := range svc.Operations {
			for i, lc := range op.Logs {
				check(fmt.Sprintf("service %q operation %q log[%d]", svc.Name, op.Name, i), lc.Attributes)
			}
		}
	}
	return warnings
}

// enumAllows reports whether v matches one of the enum members of def,
// comparing string representations to tolerate YAML scalar typing.
func enumAllows(def *semconv.Attribute, v any) bool {
	for _, m := range def.Type.Members {
		if fmt.Sprint(m.Value) == fmt.Sprint(v) {
			return true
		}
	}
	return false
}

// topoHasMetrics reports whether any service or operation in the topology defines at least one metric.
func topoHasMetrics(topo *synth.Topology) bool {
	for _, svc := range topo.Services {
		if len(svc.Metrics) > 0 {
			return true
		}
		for _, op := range svc.Operations {
			if len(op.Metrics) > 0 {
				return true
			}
		}
	}
	return false
}

func domainResolver(reg *semconv.Registry) synth.DomainResolver {
	return func(domain string) map[string]synth.AttributeGenerator {
		g := reg.Group(domain)
		if g == nil {
			// Semconv registry groups use a "registry." prefix (e.g. "registry.http")
			// but configs use the short domain name (e.g. "http") for convenience.
			g = reg.Group("registry." + domain)
		}
		if g == nil {
			return nil
		}
		return semconv.GeneratorsFor(g)
	}
}
