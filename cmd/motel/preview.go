package main

import (
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/andrewh/motel/pkg/synth"
	"github.com/spf13/cobra"
)

func previewCmd() *cobra.Command {
	var (
		duration time.Duration
		output   string
	)

	cmd := &cobra.Command{
		Use:   "preview <topology.yaml | URL>",
		Short: "Render the traffic rate over time as an SVG chart",
		Long: "Render the traffic rate over time as an SVG chart.\n\n" +
			"The topology source can be a local file path or an HTTP/HTTPS URL.\n" +
			"URL fetches have a 10-second timeout and a 10 MB response body limit.",
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return fmt.Errorf("missing topology file or URL\n\nUsage: motel preview <topology.yaml | URL>")
			}
			return cobra.ExactArgs(1)(cmd, args)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			return runPreview(cmd, args[0], duration, output)
		},
	}

	cmd.Flags().DurationVar(&duration, "duration", 0, "preview duration (default: inferred from topology)")
	cmd.Flags().StringVarP(&output, "output", "o", "", "output file path (default: stdout)")

	return cmd
}

func runPreview(cmd *cobra.Command, configPath string, duration time.Duration, output string) error {
	cfg, err := synth.LoadConfig(configPath)
	if err != nil {
		return err
	}
	if err := synth.ValidateConfig(cfg); err != nil {
		return err
	}
	topo, err := buildTopology(cfg, "")
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

	if duration == 0 {
		duration = inferDuration(scenarios)
	}

	samples := sampleRates(traffic, scenarios, duration)

	var w io.Writer = cmd.OutOrStdout()
	if output != "" {
		f, err := os.Create(output) //nolint:gosec // user-supplied output path is expected
		if err != nil {
			return fmt.Errorf("creating output file: %w", err)
		}
		defer f.Close() //nolint:errcheck // best-effort close on write
		w = f
	}

	title := filepath.Base(configPath)
	return renderSVG(w, samples, scenarios, title)
}

const defaultPreviewDuration = 5 * time.Minute

func inferDuration(scenarios []synth.Scenario) time.Duration {
	if len(scenarios) == 0 {
		return defaultPreviewDuration
	}
	var latest time.Duration
	for _, sc := range scenarios {
		if sc.End > latest {
			latest = sc.End
		}
	}
	return time.Duration(float64(latest) * 1.1)
}

type rateSample struct {
	Elapsed time.Duration
	Rate    float64
}

func sampleInterval(duration time.Duration) time.Duration {
	switch {
	case duration > 30*time.Minute:
		return 5 * time.Second
	case duration > 10*time.Minute:
		return 2 * time.Second
	default:
		return time.Second
	}
}

func sampleRates(traffic synth.TrafficPattern, scenarios []synth.Scenario, duration time.Duration) []rateSample {
	interval := sampleInterval(duration)
	n := int(duration/interval) + 1
	samples := make([]rateSample, 0, n)

	for elapsed := time.Duration(0); elapsed <= duration; elapsed += interval {
		rate := traffic.Rate(elapsed)
		active := synth.ActiveScenarios(scenarios, elapsed)
		if override := synth.ResolveTraffic(active); override != nil {
			rate = override.Rate(elapsed)
		}
		samples = append(samples, rateSample{Elapsed: elapsed, Rate: rate})
	}
	return samples
}

// SVG chart dimensions
const (
	svgWidth      = 800
	svgHeight     = 400
	marginTop     = 40
	marginRight   = 20
	marginBottom  = 50
	marginLeft    = 70
	plotWidth     = svgWidth - marginLeft - marginRight
	plotHeight    = svgHeight - marginTop - marginBottom
	gridLines     = 5
	maxTickLabels = 10
)

func renderSVG(w io.Writer, samples []rateSample, scenarios []synth.Scenario, title string) error {
	if len(samples) == 0 {
		return fmt.Errorf("no samples to render")
	}

	maxRate := 0.0
	totalDuration := samples[len(samples)-1].Elapsed
	for _, s := range samples {
		if s.Rate > maxRate {
			maxRate = s.Rate
		}
	}
	if maxRate == 0 {
		maxRate = 1
	}
	maxRate *= 1.1 // headroom

	var b strings.Builder
	b.WriteString(fmt.Sprintf(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 %d %d" width="%d" height="%d">`, svgWidth, svgHeight, svgWidth, svgHeight))
	b.WriteString("\n<style>\n")
	b.WriteString("  text { font-family: -apple-system, 'Segoe UI', Roboto, sans-serif; fill: #333; }\n")
	b.WriteString("  .title { font-size: 14px; font-weight: 600; }\n")
	b.WriteString("  .axis-label { font-size: 11px; }\n")
	b.WriteString("  .tick-label { font-size: 10px; fill: #666; }\n")
	b.WriteString("  .grid { stroke: #e0e0e0; stroke-width: 1; }\n")
	b.WriteString("  .rate-line { fill: none; stroke: #2563eb; stroke-width: 1.5; stroke-linejoin: round; }\n")
	b.WriteString("  .scenario-rect { fill: #f59e0b; fill-opacity: 0.15; stroke: #f59e0b; stroke-width: 1; stroke-opacity: 0.4; }\n")
	b.WriteString("  .scenario-label { font-size: 9px; fill: #92400e; }\n")
	b.WriteString("</style>\n")

	// Background
	b.WriteString(fmt.Sprintf(`<rect width="%d" height="%d" fill="white"/>`, svgWidth, svgHeight))
	b.WriteString("\n")

	// Title
	b.WriteString(fmt.Sprintf(`<text x="%d" y="24" class="title">%s</text>`, marginLeft, xmlEscape(title)))
	b.WriteString("\n")

	// Scenario shading (labels rendered later, after the rate line)
	for _, sc := range scenarios {
		x := marginLeft + int(float64(plotWidth)*float64(sc.Start)/float64(totalDuration))
		w := int(float64(plotWidth) * float64(sc.End-sc.Start) / float64(totalDuration))
		if w < 1 {
			w = 1
		}
		b.WriteString(fmt.Sprintf(`<rect x="%d" y="%d" width="%d" height="%d" class="scenario-rect"/>`, x, marginTop, w, plotHeight))
		b.WriteString("\n")
	}

	// Grid lines and Y-axis tick labels
	for i := 0; i <= gridLines; i++ {
		y := marginTop + plotHeight - int(float64(i)*float64(plotHeight)/float64(gridLines))
		b.WriteString(fmt.Sprintf(`<line x1="%d" y1="%d" x2="%d" y2="%d" class="grid"/>`, marginLeft, y, marginLeft+plotWidth, y))
		b.WriteString("\n")
		rate := maxRate * float64(i) / float64(gridLines)
		b.WriteString(fmt.Sprintf(`<text x="%d" y="%d" text-anchor="end" class="tick-label">%s</text>`, marginLeft-6, y+4, formatRate(rate)))
		b.WriteString("\n")
	}

	// X-axis tick labels
	tickCount := maxTickLabels
	if len(samples) < tickCount {
		tickCount = len(samples)
	}
	for i := 0; i <= tickCount; i++ {
		x := marginLeft + int(float64(i)*float64(plotWidth)/float64(tickCount))
		elapsed := time.Duration(float64(i) * float64(totalDuration) / float64(tickCount))
		b.WriteString(fmt.Sprintf(`<text x="%d" y="%d" text-anchor="middle" class="tick-label">%s</text>`, x, svgHeight-marginBottom+20, formatElapsed(elapsed)))
		b.WriteString("\n")
	}

	// Axis labels
	b.WriteString(fmt.Sprintf(`<text x="%d" y="%d" text-anchor="middle" class="axis-label">elapsed time</text>`, marginLeft+plotWidth/2, svgHeight-8))
	b.WriteString("\n")
	b.WriteString(fmt.Sprintf(`<text x="16" y="%d" text-anchor="middle" transform="rotate(-90,16,%d)" class="axis-label">traces/s</text>`, marginTop+plotHeight/2, marginTop+plotHeight/2))
	b.WriteString("\n")

	// Rate polyline
	var points strings.Builder
	for i, s := range samples {
		x := float64(marginLeft) + float64(plotWidth)*float64(s.Elapsed)/float64(totalDuration)
		y := float64(marginTop+plotHeight) - float64(plotHeight)*s.Rate/maxRate
		if i > 0 {
			points.WriteString(" ")
		}
		points.WriteString(fmt.Sprintf("%.1f,%.1f", x, y))
	}
	b.WriteString(fmt.Sprintf(`<polyline points="%s" class="rate-line"/>`, points.String()))
	b.WriteString("\n")

	// Scenario labels (rendered after rate line so text is not obscured)
	const scenarioLabelHeight = 12
	for i, sc := range scenarios {
		x := marginLeft + int(float64(plotWidth)*float64(sc.Start)/float64(totalDuration))
		y := marginTop + 12 + i*scenarioLabelHeight
		b.WriteString(fmt.Sprintf(`<text x="%d" y="%d" class="scenario-label">%s</text>`, x+2, y, xmlEscape(sc.Name)))
		b.WriteString("\n")
	}

	// Plot area border
	b.WriteString(fmt.Sprintf(`<rect x="%d" y="%d" width="%d" height="%d" fill="none" stroke="#ccc" stroke-width="1"/>`, marginLeft, marginTop, plotWidth, plotHeight))
	b.WriteString("\n")

	b.WriteString("</svg>\n")

	_, err := io.WriteString(w, b.String())
	return err
}

func formatRate(r float64) string {
	if r >= 1000 {
		return fmt.Sprintf("%.0fk", r/1000)
	}
	if r == math.Trunc(r) {
		return fmt.Sprintf("%.0f", r)
	}
	return fmt.Sprintf("%.1f", r)
}

func formatElapsed(d time.Duration) string {
	if d == 0 {
		return "0"
	}
	totalSec := int(d.Seconds())
	if totalSec < 60 {
		return fmt.Sprintf("%ds", totalSec)
	}
	m := totalSec / 60
	s := totalSec % 60
	if s == 0 {
		return fmt.Sprintf("%dm", m)
	}
	return fmt.Sprintf("%dm%ds", m, s)
}

func xmlEscape(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	s = strings.ReplaceAll(s, "'", "&apos;")
	s = strings.ReplaceAll(s, "\"", "&quot;")
	return s
}
