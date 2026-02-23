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
	)

	cmd := &cobra.Command{
		Use:   "check <topology.yaml>",
		Short: "Run structural checks on a topology",
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return fmt.Errorf("missing topology file\n\nUsage: motel check <topology.yaml>")
			}
			return cobra.ExactArgs(1)(cmd, args)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			if maxDepth < 0 || maxFanOut < 0 || maxSpans < 0 || maxSpansPerTrace < 0 || samples < 0 {
				return fmt.Errorf("limit and sample flags must be non-negative")
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

			opts := synth.CheckOptions{
				MaxDepth:         maxDepth,
				MaxFanOut:        maxFanOut,
				MaxSpans:         maxSpans,
				MaxSpansPerTrace: maxSpansPerTrace,
				Samples:          samples,
				Seed:             seed,
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
				case "max-depth":
					_, _ = fmt.Fprintf(w, "%s  %s: %d (limit: %d)\n", status, r.Name, r.Actual, r.Limit)
					if len(r.Path) > 0 {
						_, _ = fmt.Fprintf(w, "      path: %s\n", strings.Join(r.Path, " \u2192 "))
					}
				case "max-fan-out":
					_, _ = fmt.Fprintf(w, "%s  %s: %d (limit: %d)\n", status, r.Name, r.Actual, r.Limit)
					if r.Ref != "" {
						_, _ = fmt.Fprintf(w, "      worst: %s\n", r.Ref)
					}
				case "max-spans":
					line := fmt.Sprintf("%s  %s: %d static worst-case", status, r.Name, r.Actual)
					if r.Sampled != nil {
						line += fmt.Sprintf(", %d observed/%d samples", *r.Sampled, r.SamplesRun)
					}
					line += fmt.Sprintf(" (limit: %d)", r.Limit)
					_, _ = fmt.Fprintln(w, line)
				default:
					_, _ = fmt.Fprintf(w, "%s  %s: %d (limit: %d)\n", status, r.Name, r.Actual, r.Limit)
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

	return cmd
}
