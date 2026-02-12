// Standalone CLI for topology-driven synthetic OTLP trace generation
// Reads a YAML topology definition and emits realistic traces via OTel SDK
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/andrewh/motel/pkg/semconv"
	"github.com/andrewh/motel/pkg/synth"
	"github.com/spf13/cobra"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
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
		Use:   "motel-synth",
		Short: "Topology-driven synthetic OTLP trace generator",
	}

	root.AddCommand(runCmd())
	root.AddCommand(validateCmd())
	root.AddCommand(versionCmd())

	return root
}

func runCmd() *cobra.Command {
	var (
		endpoint string
		stdout   bool
		duration time.Duration
		protocol string
	)

	cmd := &cobra.Command{
		Use:   "run <config.yaml>",
		Short: "Generate synthetic traces from a topology definition",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runGenerate(cmd.Context(), args[0], runOptions{
				endpoint: endpoint,
				stdout:   stdout,
				duration: duration,
				protocol: protocol,
			})
		},
	}

	cmd.Flags().StringVar(&endpoint, "endpoint", "", "OTLP endpoint (e.g. localhost:4318)")
	cmd.Flags().BoolVar(&stdout, "stdout", false, "emit spans to stdout as JSON")
	cmd.Flags().DurationVar(&duration, "duration", 0, "override simulation duration (0 = from config or 1m default)")
	cmd.Flags().StringVar(&protocol, "protocol", "http/protobuf", "OTLP protocol (http/protobuf or grpc)")

	return cmd
}

func validateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "validate <config.yaml>",
		Short: "Parse and validate a topology configuration",
		Args:  cobra.ExactArgs(1),
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
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Configuration valid: %d services, %d root operations\n",
				len(topo.Services), len(topo.Roots))
			return nil
		},
	}
}

func versionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print version information",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Fprintf(cmd.OutOrStdout(), "motel-synth %s (commit: %s, built: %s)\n", version, commit, buildTime)
		},
	}
}

type runOptions struct {
	endpoint string
	stdout   bool
	duration time.Duration
	protocol string
}

const defaultDuration = 1 * time.Minute

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
	scenarios, err := synth.BuildScenarios(cfg.Scenarios)
	if err != nil {
		return err
	}

	// Create tracer provider
	tp, err := createTracerProvider(ctx, opts)
	if err != nil {
		return fmt.Errorf("creating tracer provider: %w", err)
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := tp.Shutdown(shutdownCtx); err != nil {
			fmt.Fprintf(os.Stderr, "error shutting down tracer provider: %v\n", err)
		}
	}()

	duration := opts.duration
	if duration == 0 {
		duration = defaultDuration
	}

	engine := &synth.Engine{
		Topology:  topo,
		Traffic:   traffic,
		Scenarios: scenarios,
		Provider:  tp,
		Rng:       rand.New(rand.NewPCG(rand.Uint64(), rand.Uint64())), //nolint:gosec // synthetic data, not security-sensitive
		Duration:  duration,
	}

	// Handle signals for graceful shutdown
	ctx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	stats, err := engine.Run(ctx)
	if err != nil {
		return err
	}

	return json.NewEncoder(os.Stderr).Encode(stats)
}

func createTracerProvider(ctx context.Context, opts runOptions) (*sdktrace.TracerProvider, error) {
	if opts.stdout {
		exporter, err := stdouttrace.New(stdouttrace.WithWriter(os.Stdout))
		if err != nil {
			return nil, err
		}
		return sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter)), nil
	}

	switch opts.protocol {
	case "grpc":
		return createGRPCProvider(ctx, opts.endpoint)
	case "http/protobuf", "":
		return createHTTPProvider(ctx, opts.endpoint)
	default:
		return nil, fmt.Errorf("unsupported protocol %q, supported: http/protobuf, grpc", opts.protocol)
	}
}

func createHTTPProvider(ctx context.Context, endpoint string) (*sdktrace.TracerProvider, error) {
	var httpOpts []otlptracehttp.Option
	if endpoint != "" {
		httpOpts = append(httpOpts, otlptracehttp.WithEndpoint(endpoint), otlptracehttp.WithInsecure())
	}
	exporter, err := otlptracehttp.New(ctx, httpOpts...)
	if err != nil {
		return nil, err
	}
	return sdktrace.NewTracerProvider(sdktrace.WithBatcher(exporter)), nil
}

func createGRPCProvider(ctx context.Context, endpoint string) (*sdktrace.TracerProvider, error) {
	var grpcOpts []otlptracegrpc.Option
	if endpoint != "" {
		grpcOpts = append(grpcOpts, otlptracegrpc.WithEndpoint(endpoint), otlptracegrpc.WithInsecure())
	}
	exporter, err := otlptracegrpc.New(ctx, grpcOpts...)
	if err != nil {
		return nil, err
	}
	return sdktrace.NewTracerProvider(sdktrace.WithBatcher(exporter)), nil
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
