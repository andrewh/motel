package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/andrewh/motel/pkg/synth"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPreviewCommand(t *testing.T) {
	t.Parallel()

	t.Run("produces SVG to stdout", func(t *testing.T) {
		t.Parallel()
		path := writeTestConfig(t, validConfig)

		root := rootCmd()
		root.SetArgs([]string{"preview", "--duration", "30s", path})
		var out bytes.Buffer
		root.SetOut(&out)

		err := root.Execute()
		require.NoError(t, err)
		assert.True(t, strings.HasPrefix(out.String(), "<svg"))
		assert.Contains(t, out.String(), "</svg>")
	})

	t.Run("produces SVG to file", func(t *testing.T) {
		t.Parallel()
		path := writeTestConfig(t, validConfig)
		outFile := filepath.Join(t.TempDir(), "preview.svg")

		root := rootCmd()
		root.SetArgs([]string{"preview", "--duration", "30s", "-o", outFile, path})

		err := root.Execute()
		require.NoError(t, err)

		data, err := os.ReadFile(outFile)
		require.NoError(t, err)
		assert.True(t, strings.HasPrefix(string(data), "<svg"))
	})

	t.Run("missing config file", func(t *testing.T) {
		t.Parallel()
		root := rootCmd()
		root.SetArgs([]string{"preview", "/nonexistent.yaml"})

		err := root.Execute()
		require.Error(t, err)
	})

	t.Run("no args shows error", func(t *testing.T) {
		t.Parallel()
		root := rootCmd()
		root.SetArgs([]string{"preview"})

		err := root.Execute()
		require.Error(t, err)
	})

	t.Run("with scenarios", func(t *testing.T) {
		t.Parallel()
		cfg := `
version: 1
services:
  svc:
    operations:
      op:
        duration: 10ms
traffic:
  rate: 100/s
scenarios:
  - name: spike
    at: +30s
    duration: 10s
    traffic:
      rate: 500/s
      pattern: uniform
`
		path := writeTestConfig(t, cfg)
		root := rootCmd()
		root.SetArgs([]string{"preview", "--duration", "1m", path})
		var out bytes.Buffer
		root.SetOut(&out)

		err := root.Execute()
		require.NoError(t, err)
		assert.Contains(t, out.String(), "spike")
		assert.Contains(t, out.String(), "scenario-rect")
	})
}

func TestInferDuration(t *testing.T) {
	t.Parallel()

	t.Run("no scenarios returns default", func(t *testing.T) {
		t.Parallel()
		got := inferDuration(nil)
		assert.Equal(t, defaultPreviewDuration, got)
	})

	t.Run("uses latest scenario end with buffer", func(t *testing.T) {
		t.Parallel()
		scenarios := []synth.Scenario{
			{Name: "a", Start: 10 * time.Second, End: 30 * time.Second},
			{Name: "b", Start: 1 * time.Minute, End: 2 * time.Minute},
		}
		got := inferDuration(scenarios)
		twoMin := 2 * time.Minute
		expected := time.Duration(float64(twoMin) * 1.1)
		assert.Equal(t, expected, got)
	})
}

func TestSampleInterval(t *testing.T) {
	t.Parallel()

	tests := []struct {
		duration time.Duration
		expected time.Duration
	}{
		{5 * time.Minute, time.Second},
		{10 * time.Minute, time.Second},
		{15 * time.Minute, 2 * time.Second},
		{31 * time.Minute, 5 * time.Second},
	}

	for _, tt := range tests {
		t.Run(tt.duration.String(), func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.expected, sampleInterval(tt.duration))
		})
	}
}

func TestFormatRate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		rate     float64
		expected string
	}{
		{0, "0"},
		{100, "100"},
		{999, "999"},
		{1000, "1k"},
		{5500, "6k"},
		{12.5, "12.5"},
	}

	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.expected, formatRate(tt.rate))
		})
	}
}

func TestFormatElapsed(t *testing.T) {
	t.Parallel()

	tests := []struct {
		d        time.Duration
		expected string
	}{
		{0, "0"},
		{30 * time.Second, "30s"},
		{time.Minute, "1m"},
		{90 * time.Second, "1m30s"},
		{5 * time.Minute, "5m"},
	}

	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.expected, formatElapsed(tt.d))
		})
	}
}

func TestSampleRates(t *testing.T) {
	t.Parallel()

	t.Run("uniform traffic without scenarios", func(t *testing.T) {
		t.Parallel()
		cfg := synth.TrafficConfig{Rate: "100/s", Pattern: "uniform"}
		traffic, err := synth.NewTrafficPattern(cfg)
		require.NoError(t, err)

		samples := sampleRates(traffic, nil, 5*time.Second)
		assert.Equal(t, 6, len(samples)) // 0s, 1s, 2s, 3s, 4s, 5s
		for _, s := range samples {
			assert.Equal(t, 100.0, s.Rate)
		}
	})

	t.Run("scenario overrides traffic", func(t *testing.T) {
		t.Parallel()
		baseCfg := synth.TrafficConfig{Rate: "100/s", Pattern: "uniform"}
		traffic, err := synth.NewTrafficPattern(baseCfg)
		require.NoError(t, err)

		scenarioCfg := synth.TrafficConfig{Rate: "500/s", Pattern: "uniform"}
		scenarioTraffic, err := synth.NewTrafficPattern(scenarioCfg)
		require.NoError(t, err)

		scenarios := []synth.Scenario{
			{
				Name:    "spike",
				Start:   2 * time.Second,
				End:     4 * time.Second,
				Traffic: scenarioTraffic,
			},
		}

		samples := sampleRates(traffic, scenarios, 5*time.Second)
		assert.Equal(t, 100.0, samples[0].Rate) // 0s — base
		assert.Equal(t, 100.0, samples[1].Rate) // 1s — base
		assert.Equal(t, 500.0, samples[2].Rate) // 2s — scenario
		assert.Equal(t, 500.0, samples[3].Rate) // 3s — scenario
		assert.Equal(t, 100.0, samples[4].Rate) // 4s — back to base (end is exclusive)
		assert.Equal(t, 100.0, samples[5].Rate) // 5s — base
	})
}

func TestRenderSVG(t *testing.T) {
	t.Parallel()

	t.Run("empty samples returns error", func(t *testing.T) {
		t.Parallel()
		var buf bytes.Buffer
		err := renderSVG(&buf, nil, nil, "test")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no samples")
	})

	t.Run("renders valid SVG", func(t *testing.T) {
		t.Parallel()
		samples := []rateSample{
			{Elapsed: 0, Rate: 100},
			{Elapsed: time.Second, Rate: 200},
			{Elapsed: 2 * time.Second, Rate: 150},
		}
		var buf bytes.Buffer
		err := renderSVG(&buf, samples, nil, "test.yaml")
		require.NoError(t, err)

		svg := buf.String()
		assert.True(t, strings.HasPrefix(svg, "<svg"))
		assert.Contains(t, svg, "test.yaml")
		assert.Contains(t, svg, "polyline")
		assert.Contains(t, svg, "traces/s")
		assert.Contains(t, svg, "elapsed time")
	})

	t.Run("escapes XML in title", func(t *testing.T) {
		t.Parallel()
		samples := []rateSample{{Elapsed: 0, Rate: 100}}
		var buf bytes.Buffer
		err := renderSVG(&buf, samples, nil, "test<>&.yaml")
		require.NoError(t, err)
		assert.Contains(t, buf.String(), "test&lt;&gt;&amp;.yaml")
	})

	t.Run("scenario labels are staggered vertically", func(t *testing.T) {
		t.Parallel()
		samples := []rateSample{
			{Elapsed: 0, Rate: 100},
			{Elapsed: 10 * time.Second, Rate: 100},
		}
		scenarioCfg := synth.TrafficConfig{Rate: "500/s", Pattern: "uniform"}
		scenarioTraffic, err := synth.NewTrafficPattern(scenarioCfg)
		require.NoError(t, err)

		scenarios := []synth.Scenario{
			{Name: "alpha", Start: 1 * time.Second, End: 5 * time.Second, Traffic: scenarioTraffic},
			{Name: "beta", Start: 2 * time.Second, End: 6 * time.Second, Traffic: scenarioTraffic},
		}
		var buf bytes.Buffer
		err = renderSVG(&buf, samples, scenarios, "test.yaml")
		require.NoError(t, err)

		svg := buf.String()
		assert.Contains(t, svg, "alpha")
		assert.Contains(t, svg, "beta")

		// Labels should have different y positions (staggered by 12px each)
		alphaY := fmt.Sprintf(`y="%d"`, marginTop+12)
		betaY := fmt.Sprintf(`y="%d"`, marginTop+12+12)
		assert.Contains(t, svg, alphaY)
		assert.Contains(t, svg, betaY)
	})

	t.Run("scenario labels render after rate line", func(t *testing.T) {
		t.Parallel()
		samples := []rateSample{
			{Elapsed: 0, Rate: 100},
			{Elapsed: 10 * time.Second, Rate: 100},
		}
		scenarioCfg := synth.TrafficConfig{Rate: "500/s", Pattern: "uniform"}
		scenarioTraffic, err := synth.NewTrafficPattern(scenarioCfg)
		require.NoError(t, err)

		scenarios := []synth.Scenario{
			{Name: "spike", Start: 1 * time.Second, End: 5 * time.Second, Traffic: scenarioTraffic},
		}
		var buf bytes.Buffer
		err = renderSVG(&buf, samples, scenarios, "test.yaml")
		require.NoError(t, err)

		svg := buf.String()
		polylineIdx := strings.Index(svg, "rate-line")
		labelIdx := strings.Index(svg, "spike")
		assert.Greater(t, labelIdx, polylineIdx, "scenario label should appear after polyline in SVG")
	})
}
