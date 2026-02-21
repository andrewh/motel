// Synthetic OpenTelemetry generator
// Reads a YAML topology definition and emits traces, metrics, and logs via OTel SDK
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"os"
	"os/signal"
	"strings"
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
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
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
	)

	cmd := &cobra.Command{
		Use:   "run <topology.yaml>",
		Short: "Generate synthetic signals from a topology definition",
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return fmt.Errorf("missing topology file\n\nUsage: motel run <topology.yaml>")
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

	return cmd
}

func validateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "validate <topology.yaml>",
		Short: "Parse and validate a topology configuration",
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return fmt.Errorf("missing topology file\n\nUsage: motel validate <topology.yaml>")
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
			topo, err := buildTopology(cfg)
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
	cfg, err := synth.LoadConfig(configPath)
	if err != nil {
		return err
	}
	if err := synth.ValidateConfig(cfg); err != nil {
		return err
	}
	topo, err := buildTopology(cfg)
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

	res, err := resource.Merge(resource.Default(), resource.NewSchemaless(
		attribute.String("motel.version", version),
	))
	if err != nil {
		return fmt.Errorf("creating resource: %w", err)
	}

	// Create tracer provider (no-op if traces not requested)
	var tp *sdktrace.TracerProvider
	if enabledSignals["traces"] {
		tp, err = createTracerProvider(ctx, opts, res)
		if err != nil {
			return fmt.Errorf("creating tracer provider: %w", err)
		}
	} else {
		tp = sdktrace.NewTracerProvider()
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		if err := tp.Shutdown(shutdownCtx); err != nil {
			fmt.Fprintf(os.Stderr, "error shutting down tracer provider: %v\n", err)
		}
	}()

	var observers []synth.SpanObserver

	if enabledSignals["metrics"] {
		mp, shutdownMP, mErr := createMeterProvider(ctx, opts, res)
		if mErr != nil {
			return fmt.Errorf("creating meter provider: %w", mErr)
		}
		defer func() {
			shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
			defer cancel()
			if err := shutdownMP(shutdownCtx); err != nil {
				fmt.Fprintf(os.Stderr, "error shutting down meter provider: %v\n", err)
			}
		}()
		obs, mErr := synth.NewMetricObserver(mp)
		if mErr != nil {
			return fmt.Errorf("creating metric observer: %w", mErr)
		}
		observers = append(observers, obs)
	}

	if enabledSignals["logs"] {
		lp, shutdownLP, lErr := createLoggerProvider(ctx, opts, res)
		if lErr != nil {
			return fmt.Errorf("creating logger provider: %w", lErr)
		}
		defer func() {
			shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
			defer cancel()
			if err := shutdownLP(shutdownCtx); err != nil {
				fmt.Fprintf(os.Stderr, "error shutting down logger provider: %v\n", err)
			}
		}()
		observers = append(observers, synth.NewLogObserver(lp, opts.slowThreshold))
	}

	duration := opts.duration
	if duration == 0 {
		duration = defaultDuration
	}

	engine := &synth.Engine{
		Topology:         topo,
		Traffic:          traffic,
		Scenarios:        scenarios,
		Provider:         tp,
		Rng:              rand.New(rand.NewPCG(rand.Uint64(), rand.Uint64())), //nolint:gosec // synthetic data, not security-sensitive
		Duration:         duration,
		Observers:        observers,
		MaxSpansPerTrace: opts.maxSpansPerTrace,
		State:            synth.NewSimulationState(topo),
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

func createTracerProvider(ctx context.Context, opts runOptions, res *resource.Resource) (*sdktrace.TracerProvider, error) {
	if opts.stdout {
		exporter, err := stdouttrace.New(stdouttrace.WithWriter(os.Stdout))
		if err != nil {
			return nil, err
		}
		return sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter), sdktrace.WithResource(res)), nil
	}

	switch opts.protocol {
	case "grpc":
		return createGRPCProvider(ctx, opts.endpoint, res)
	case "http/protobuf", "":
		return createHTTPProvider(ctx, opts.endpoint, res)
	default:
		return nil, fmt.Errorf("unsupported protocol %q, supported: http/protobuf, grpc", opts.protocol)
	}
}

func createHTTPProvider(ctx context.Context, endpoint string, res *resource.Resource) (*sdktrace.TracerProvider, error) {
	var httpOpts []otlptracehttp.Option
	if endpoint != "" {
		httpOpts = append(httpOpts, otlptracehttp.WithEndpoint(endpoint), otlptracehttp.WithInsecure())
	}
	exporter, err := otlptracehttp.New(ctx, httpOpts...)
	if err != nil {
		return nil, err
	}
	return sdktrace.NewTracerProvider(sdktrace.WithBatcher(exporter), sdktrace.WithResource(res)), nil
}

func createGRPCProvider(ctx context.Context, endpoint string, res *resource.Resource) (*sdktrace.TracerProvider, error) {
	var grpcOpts []otlptracegrpc.Option
	if endpoint != "" {
		grpcOpts = append(grpcOpts, otlptracegrpc.WithEndpoint(endpoint), otlptracegrpc.WithInsecure())
	}
	exporter, err := otlptracegrpc.New(ctx, grpcOpts...)
	if err != nil {
		return nil, err
	}
	return sdktrace.NewTracerProvider(sdktrace.WithBatcher(exporter), sdktrace.WithResource(res)), nil
}

type shutdownFunc func(ctx context.Context) error

func createMeterProvider(ctx context.Context, opts runOptions, res *resource.Resource) (*sdkmetric.MeterProvider, shutdownFunc, error) {
	if opts.stdout {
		exporter, err := stdoutmetric.New(stdoutmetric.WithWriter(os.Stdout))
		if err != nil {
			return nil, nil, err
		}
		mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(sdkmetric.NewPeriodicReader(exporter)), sdkmetric.WithResource(res))
		return mp, mp.Shutdown, nil
	}

	switch opts.protocol {
	case "grpc":
		return createGRPCMeterProvider(ctx, opts.endpoint, res)
	case "http/protobuf", "":
		return createHTTPMeterProvider(ctx, opts.endpoint, res)
	default:
		return nil, nil, fmt.Errorf("unsupported protocol %q for metrics", opts.protocol)
	}
}

func createHTTPMeterProvider(ctx context.Context, endpoint string, res *resource.Resource) (*sdkmetric.MeterProvider, shutdownFunc, error) {
	var httpOpts []otlpmetrichttp.Option
	if endpoint != "" {
		httpOpts = append(httpOpts, otlpmetrichttp.WithEndpoint(endpoint), otlpmetrichttp.WithInsecure())
	}
	exporter, err := otlpmetrichttp.New(ctx, httpOpts...)
	if err != nil {
		return nil, nil, err
	}
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(sdkmetric.NewPeriodicReader(exporter)), sdkmetric.WithResource(res))
	return mp, mp.Shutdown, nil
}

func createGRPCMeterProvider(ctx context.Context, endpoint string, res *resource.Resource) (*sdkmetric.MeterProvider, shutdownFunc, error) {
	var grpcOpts []otlpmetricgrpc.Option
	if endpoint != "" {
		grpcOpts = append(grpcOpts, otlpmetricgrpc.WithEndpoint(endpoint), otlpmetricgrpc.WithInsecure())
	}
	exporter, err := otlpmetricgrpc.New(ctx, grpcOpts...)
	if err != nil {
		return nil, nil, err
	}
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(sdkmetric.NewPeriodicReader(exporter)), sdkmetric.WithResource(res))
	return mp, mp.Shutdown, nil
}

func createLoggerProvider(ctx context.Context, opts runOptions, res *resource.Resource) (*sdklog.LoggerProvider, shutdownFunc, error) {
	if opts.stdout {
		exporter, err := stdoutlog.New(stdoutlog.WithWriter(os.Stdout))
		if err != nil {
			return nil, nil, err
		}
		lp := sdklog.NewLoggerProvider(sdklog.WithProcessor(sdklog.NewSimpleProcessor(exporter)), sdklog.WithResource(res))
		return lp, lp.Shutdown, nil
	}

	switch opts.protocol {
	case "grpc":
		return createGRPCLoggerProvider(ctx, opts.endpoint, res)
	case "http/protobuf", "":
		return createHTTPLoggerProvider(ctx, opts.endpoint, res)
	default:
		return nil, nil, fmt.Errorf("unsupported protocol %q for logs", opts.protocol)
	}
}

func createHTTPLoggerProvider(ctx context.Context, endpoint string, res *resource.Resource) (*sdklog.LoggerProvider, shutdownFunc, error) {
	var httpOpts []otlploghttp.Option
	if endpoint != "" {
		httpOpts = append(httpOpts, otlploghttp.WithEndpoint(endpoint), otlploghttp.WithInsecure())
	}
	exporter, err := otlploghttp.New(ctx, httpOpts...)
	if err != nil {
		return nil, nil, err
	}
	lp := sdklog.NewLoggerProvider(sdklog.WithProcessor(sdklog.NewBatchProcessor(exporter)), sdklog.WithResource(res))
	return lp, lp.Shutdown, nil
}

func createGRPCLoggerProvider(ctx context.Context, endpoint string, res *resource.Resource) (*sdklog.LoggerProvider, shutdownFunc, error) {
	var grpcOpts []otlploggrpc.Option
	if endpoint != "" {
		grpcOpts = append(grpcOpts, otlploggrpc.WithEndpoint(endpoint), otlploggrpc.WithInsecure())
	}
	exporter, err := otlploggrpc.New(ctx, grpcOpts...)
	if err != nil {
		return nil, nil, err
	}
	lp := sdklog.NewLoggerProvider(sdklog.WithProcessor(sdklog.NewBatchProcessor(exporter)), sdklog.WithResource(res))
	return lp, lp.Shutdown, nil
}

func buildTopology(cfg *synth.Config) (*synth.Topology, error) {
	reg, err := semconv.LoadEmbedded()
	if err != nil {
		return nil, fmt.Errorf("loading semantic conventions: %w", err)
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
