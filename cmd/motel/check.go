package main

import (
	"fmt"
	"strings"

	"github.com/andrewh/motel/pkg/synth"
	"github.com/spf13/cobra"
)

func checkCmd() *cobra.Command {
	var (
		maxDepth         int
		maxFanOut        int
		maxSpans         int
		maxSpansPerTrace int
		samples          int
		seed             uint64
		semconvDir       string
		checksPath       string
		skipScenarios    bool
	)

	cmd := &cobra.Command{
		Use:   "check <topology.yaml | URL>",
		Short: "Run structural checks on a topology",
		Long: "Run structural checks on a topology.\n\n" +
			"The topology source can be a local file path or an HTTP/HTTPS URL.\n" +
			"URL fetches have a 10-second timeout and a 10 MB response body limit.\n\n" +
			"Scenarios defined in the topology are explored automatically: every\n" +
			"distinct combination of co-active scenarios is checked alongside the\n" +
			"baseline, and each check reports the combination that produces the\n" +
			"worst case. Use --skip-scenarios to check the baseline topology only.\n\n" +
			"Use --checks to load thresholds from a separate YAML checks file or URL.\n" +
			"Explicit command-line limit flags override values from that file.",
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return fmt.Errorf("missing topology file or URL\n\nUsage: motel check <topology.yaml | URL>")
			}
			return cobra.ExactArgs(1)(cmd, args)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			if maxDepth < 0 || maxFanOut < 0 || maxSpans < 0 || maxSpansPerTrace < 0 || samples < 0 {
				return fmt.Errorf("limit and sample flags must be non-negative")
			}

			var assertions synth.CheckAssertions
			if checksPath != "" {
				loaded, err := synth.LoadCheckAssertions(checksPath)
				if err != nil {
					return err
				}
				assertions = *loaded
				if assertions.Checks.HasPercentile() && samples == 0 {
					return fmt.Errorf("percentile checks require --samples greater than 0")
				}

				flags := cmd.Flags()
				if flags.Changed("max-depth") {
					assertions.Checks.MaxDepth = checkLimitPtr(maxDepth)
				}
				if flags.Changed("max-fan-out") {
					assertions.Checks.MaxFanOut = checkLimitPtr(maxFanOut)
				}
				if flags.Changed("max-spans") {
					assertions.Checks.MaxSpans = checkLimitPtr(maxSpans)
				}
			}

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

			var scenarios []synth.Scenario
			if !skipScenarios {
				scenarios, err = synth.BuildScenarios(cfg.Scenarios, topo)
				if err != nil {
					return err
				}
			}

			opts := synth.CheckOptions{
				MaxDepth:         maxDepth,
				MaxFanOut:        maxFanOut,
				MaxSpans:         maxSpans,
				MaxSpansPerTrace: maxSpansPerTrace,
				Samples:          samples,
				Seed:             seed,
				Scenarios:        scenarios,
				Assertions:       assertions.Checks,
			}

			results := synth.Check(topo, opts)

			anyFailed := false
			w := cmd.OutOrStdout()
			for _, r := range results {
				status := "PASS"
				if !r.Pass {
					status = "FAIL"
					anyFailed = true
				}

				switch r.Name {
				case synth.CheckNameMaxDepth:
					_, _ = fmt.Fprintf(w, "%s  %s: %d (limit: %d)\n", status, r.Name, r.Actual, r.Limit)
					if len(r.Path) > 0 {
						_, _ = fmt.Fprintf(w, "      path: %s\n", strings.Join(r.Path, " \u2192 "))
					}
				case synth.CheckNameMaxFanOut:
					_, _ = fmt.Fprintf(w, "%s  %s: %d (limit: %d)\n", status, r.Name, r.Actual, r.Limit)
					if r.Ref != "" {
						_, _ = fmt.Fprintf(w, "      worst: %s\n", r.Ref)
					}
				case synth.CheckNameMaxSpans:
					line := fmt.Sprintf("%s  %s: %d static worst-case", status, r.Name, r.Actual)
					if r.Sampled != nil {
						line += fmt.Sprintf(", %d observed/%d samples", *r.Sampled, r.SamplesRun)
					}
					line += fmt.Sprintf(" (limit: %d)", r.Limit)
					_, _ = fmt.Fprintln(w, line)
				default:
					_, _ = fmt.Fprintf(w, "%s  %s: %d (limit: %d)\n", status, r.Name, r.Actual, r.Limit)
				}

				if len(r.Scenarios) > 0 {
					_, _ = fmt.Fprintf(w, "      scenarios: %s\n", strings.Join(r.Scenarios, " + "))
				}

				if r.Distribution != nil {
					d := r.Distribution
					_, _ = fmt.Fprintf(w, "      p50: %d  p95: %d  p99: %d  max: %d  (%d samples)\n",
						d.P50, d.P95, d.P99, d.Max, r.SamplesRun)
				}
			}

			if anyFailed {
				return fmt.Errorf("one or more checks failed")
			}
			return nil
		},
	}

	cmd.Flags().IntVar(&maxDepth, "max-depth", 10, "fail if worst-case trace depth exceeds this")
	cmd.Flags().IntVar(&maxFanOut, "max-fan-out", 100, "fail if worst-case children per span exceeds this")
	cmd.Flags().IntVar(&maxSpans, "max-spans", 10000, "fail if worst-case spans per trace exceeds this")
	cmd.Flags().IntVar(&samples, "samples", 1000, "sampled traces for empirical measurement")
	cmd.Flags().Uint64Var(&seed, "seed", 0, "random seed for reproducibility (0 = random)")
	cmd.Flags().IntVar(&maxSpansPerTrace, "max-spans-per-trace", 0, fmt.Sprintf("maximum spans per sampled trace (0 = default %d)", synth.DefaultMaxSpansPerTrace))
	cmd.Flags().StringVar(&semconvDir, "semconv", "", "directory of additional semantic convention YAML files")
	cmd.Flags().StringVar(&checksPath, "checks", "", "YAML checks file or URL with structural thresholds")
	cmd.Flags().BoolVar(&skipScenarios, "skip-scenarios", false, "check the baseline topology only, ignoring scenarios")

	return cmd
}

func checkLimitPtr(v int) *int { return &v }
