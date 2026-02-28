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
	"os"
	"os/signal"
	"slices"
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
	"go.opentelemetry.io/otel/trace"
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
		signals          string
		slowThreshold    time.Duration
		maxSpansPerTrace int
		semconvDir       string
		labelScenarios   bool
		pprofAddr        string
		timeOffset       time.Duration
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
			return runGenerate(cmd.Context(), args[0], runOptions{
				endpoint:         endpoint,
				stdout:           stdout,
				duration:         duration,
				protocol:         protocol,
				signals:          signals,
				slowThreshold:    slowThreshold,
				maxSpansPerTrace: maxSpansPerTrace,
				semconvDir:       semconvDir,
				labelScenarios:   labelScenarios,
				pprofAddr:        pprofAddr,
				timeOffset:       timeOffset,
			})
		},
	}

	cmd.Flags().StringVar(&endpoint, "endpoint", "", "OTLP endpoint (e.g. localhost:4318)")
	cmd.Flags().BoolVar(&stdout, "stdout", false, "emit signals to stdout as JSON")
	cmd.Flags().DurationVar(&duration, "duration", 0, "simulation duration, e.g. 10s, 5m, 1h (default 1m)")
	cmd.Flags().StringVar(&protocol, "protocol", "http/protobuf", "OTLP protocol (http/protobuf or grpc)")
	cmd.Flags().StringVar(&signals, "signals", "traces", "comma-separated signals to emit: traces,metrics,logs")
	cmd.Flags().DurationVar(&slowThreshold, "slow-threshold", time.Second, "duration threshold for slow span log emission")
	cmd.Flags().IntVar(&maxSpansPerTrace, "max-spans-per-trace", 0, "maximum spans per trace (0 = default 10000)")
	cmd.Flags().StringVar(&semconvDir, "semconv", "", "directory of additional semantic convention YAML files")
	cmd.Flags().BoolVar(&labelScenarios, "label-scenarios", false, "add synth.scenarios attribute to spans with active scenario names")
	cmd.Flags().StringVar(&pprofAddr, "pprof", "", "start pprof HTTP server on this address (e.g. :6060)")
	cmd.Flags().DurationVar(&timeOffset, "time-offset", 0, "shift span timestamps by this duration (e.g. -1h for past, 1h for future)")

	return cmd
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
			topo, err := buildTopology(cfg, semconvDir)
			if err != nil {
				return err
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
		format    string
		minTraces int
	)

	cmd := &cobra.Command{
		Use:   "import [file]",
		Short: "Import a topology config from trace data",
		Long:  "Reads trace spans (stdouttrace or OTLP JSON) and generates a synth YAML topology config.",
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

			yamlBytes, err := traceimport.Import(r, traceimport.Options{
				Format:    traceimport.Format(format),
				MinTraces: minTraces,
				Warnings:  cmd.ErrOrStderr(),
			})
			if err != nil {
				if strings.Contains(err.Error(), "no spans found") {
					return fmt.Errorf("%w\n\nProvide a file or pipe stdin:\n  motel import traces.json\n  cat traces.json | motel import", err)
				}
				return err
			}

			_, err = cmd.OutOrStdout().Write(yamlBytes)
			return err
		},
	}

	cmd.Flags().StringVar(&format, "format", "auto", "input format: auto, stdouttrace, or otlp")
	cmd.Flags().IntVar(&minTraces, "min-traces", 1, "minimum traces for statistical accuracy (warns if fewer)")

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
	stdout           bool
	duration         time.Duration
	protocol         string
	signals          string
	slowThreshold    time.Duration
	maxSpansPerTrace int
	semconvDir       string
	labelScenarios   bool
	pprofAddr        string
	timeOffset       time.Duration
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
	shutdownTimeout     = 5 * time.Second
	connectCheckTimeout = 2 * time.Second
	defaultHTTPPort     = "4318"
	defaultGRPCPort     = "4317"
)

func checkEndpoint(endpoint, protocol, configPath string) error {
	host := endpoint
	if host == "" {
		port := defaultHTTPPort
		if protocol == "grpc" {
			port = defaultGRPCPort
		}
		host = "localhost:" + port
	} else if _, _, err := net.SplitHostPort(host); err != nil {
		port := defaultHTTPPort
		if protocol == "grpc" {
			port = defaultGRPCPort
		}
		host = net.JoinHostPort(host, port)
	}

	conn, err := net.DialTimeout("tcp", host, connectCheckTimeout)
	if err != nil {
		return fmt.Errorf("cannot reach OTLP collector at %s\n\n"+
			"To emit signals as JSON to the terminal, use --stdout:\n"+
			"  motel run --stdout --duration 10s %s\n\n"+
			"To send to a specific collector, use --endpoint:\n"+
			"  motel run --endpoint collector.example.com:4318 %s\n\n"+
			"Without --duration, motel runs for 1 minute", host, configPath, configPath)
	}
	_ = conn.Close()
	return nil
}

func runGenerate(ctx context.Context, configPath string, opts runOptions) error {
	if opts.pprofAddr != "" {
		go func() {
			fmt.Fprintf(os.Stderr, "pprof server listening on %s\n", opts.pprofAddr)
			if err := http.ListenAndServe(opts.pprofAddr, nil); err != nil { //nolint:gosec // pprof server is opt-in via flag
				fmt.Fprintf(os.Stderr, "pprof server error: %v\n", err)
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
		if err := checkEndpoint(opts.endpoint, opts.protocol, configPath); err != nil {
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
		attrs := []attribute.KeyValue{
			attribute.String("service.name", name),
		}
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

	var observers []synth.SpanObserver

	if enabledSignals["metrics"] {
		meters, shutdownMetrics, mErr := createMetricProviders(ctx, opts, serviceResources)
		if mErr != nil {
			return fmt.Errorf("creating metric providers: %w", mErr)
		}
		defer shutdownMetrics()
		obs, mErr := synth.NewMetricObserver(meters)
		if mErr != nil {
			return fmt.Errorf("creating metric observer: %w", mErr)
		}
		observers = append(observers, obs)
	}

	if enabledSignals["logs"] {
		loggers, shutdownLogs, lErr := createLogProviders(ctx, opts, serviceResources)
		if lErr != nil {
			return fmt.Errorf("creating log providers: %w", lErr)
		}
		defer shutdownLogs()
		observers = append(observers, synth.NewLogObserver(loggers, opts.slowThreshold))
	}

	duration := opts.duration
	if duration == 0 {
		duration = defaultDuration
	}

	engine := &synth.Engine{
		Topology:  topo,
		Traffic:   traffic,
		Scenarios: scenarios,
		Tracers: func(name string) trace.Tracer {
			tp := traceProviders[name]
			if tp == nil {
				panic(fmt.Sprintf("BUG: no tracer provider for service %q", name))
			}
			return tp.Tracer(name)
		},
		Rng:              rand.New(rand.NewPCG(rand.Uint64(), rand.Uint64())), //nolint:gosec // synthetic data, not security-sensitive
		Duration:         duration,
		Observers:        observers,
		MaxSpansPerTrace: opts.maxSpansPerTrace,
		State:            synth.NewSimulationState(topo),
		LabelScenarios:   opts.labelScenarios,
		TimeOffset:       opts.timeOffset,
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
	switch opts.protocol {
	case "grpc":
		var grpcOpts []otlptracegrpc.Option
		if opts.endpoint != "" {
			grpcOpts = append(grpcOpts, otlptracegrpc.WithEndpoint(opts.endpoint), otlptracegrpc.WithInsecure())
		}
		return otlptracegrpc.New(ctx, grpcOpts...)
	case "http/protobuf", "":
		var httpOpts []otlptracehttp.Option
		if opts.endpoint != "" {
			httpOpts = append(httpOpts, otlptracehttp.WithEndpoint(opts.endpoint), otlptracehttp.WithInsecure())
		}
		return otlptracehttp.New(ctx, httpOpts...)
	default:
		return nil, fmt.Errorf("unsupported protocol %q, supported: http/protobuf, grpc", opts.protocol)
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

	wrapper := &noopShutdownMetricExporter{exporter}
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
	switch opts.protocol {
	case "grpc":
		var grpcOpts []otlpmetricgrpc.Option
		if opts.endpoint != "" {
			grpcOpts = append(grpcOpts, otlpmetricgrpc.WithEndpoint(opts.endpoint), otlpmetricgrpc.WithInsecure())
		}
		return otlpmetricgrpc.New(ctx, grpcOpts...)
	case "http/protobuf", "":
		var httpOpts []otlpmetrichttp.Option
		if opts.endpoint != "" {
			httpOpts = append(httpOpts, otlpmetrichttp.WithEndpoint(opts.endpoint), otlpmetrichttp.WithInsecure())
		}
		return otlpmetrichttp.New(ctx, httpOpts...)
	default:
		return nil, fmt.Errorf("unsupported protocol %q for metrics", opts.protocol)
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
	switch opts.protocol {
	case "grpc":
		var grpcOpts []otlploggrpc.Option
		if opts.endpoint != "" {
			grpcOpts = append(grpcOpts, otlploggrpc.WithEndpoint(opts.endpoint), otlploggrpc.WithInsecure())
		}
		return otlploggrpc.New(ctx, grpcOpts...)
	case "http/protobuf", "":
		var httpOpts []otlploghttp.Option
		if opts.endpoint != "" {
			httpOpts = append(httpOpts, otlploghttp.WithEndpoint(opts.endpoint), otlploghttp.WithInsecure())
		}
		return otlploghttp.New(ctx, httpOpts...)
	default:
		return nil, fmt.Errorf("unsupported protocol %q for logs", opts.protocol)
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
	return synth.BuildTopology(cfg, domainResolver(reg))
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
