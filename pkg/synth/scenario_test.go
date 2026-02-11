// Tests for scenario activation windows and override resolution
// Validates time offset parsing, window detection, and last-defined-wins semantics
package synth

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseOffset(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		input   string
		want    time.Duration
		wantErr string
	}{
		{name: "minutes with plus", input: "+5m", want: 5 * time.Minute},
		{name: "seconds with plus", input: "+30s", want: 30 * time.Second},
		{name: "hours with plus", input: "+1h", want: time.Hour},
		{name: "without plus", input: "5m", want: 5 * time.Minute},
		{name: "complex duration", input: "+1h30m", want: 90 * time.Minute},
		{name: "empty", input: "", wantErr: "cannot be empty"},
		{name: "invalid", input: "+xyz", wantErr: "invalid"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := ParseOffset(tt.input)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestBuildScenarios(t *testing.T) {
	t.Parallel()

	t.Run("builds scenario with activation window", func(t *testing.T) {
		t.Parallel()
		cfgs := []ScenarioConfig{{
			Name:     "degradation",
			At:       "+5m",
			Duration: "10m",
			Override: map[string]OverrideConfig{
				"svc.op": {Duration: "500ms +/- 100ms", ErrorRate: "15%"},
			},
		}}

		scenarios, err := BuildScenarios(cfgs)
		require.NoError(t, err)
		require.Len(t, scenarios, 1)

		sc := scenarios[0]
		assert.Equal(t, "degradation", sc.Name)
		assert.Equal(t, 5*time.Minute, sc.Start)
		assert.Equal(t, 15*time.Minute, sc.End)
		require.Contains(t, sc.Overrides, "svc.op")
		assert.InDelta(t, 0.15, sc.Overrides["svc.op"].ErrorRate, 0.001)
		assert.True(t, sc.Overrides["svc.op"].HasErrorRate)
	})

	t.Run("override without error rate", func(t *testing.T) {
		t.Parallel()
		cfgs := []ScenarioConfig{{
			Name:     "slow",
			At:       "+1m",
			Duration: "5m",
			Override: map[string]OverrideConfig{
				"svc.op": {Duration: "200ms"},
			},
		}}

		scenarios, err := BuildScenarios(cfgs)
		require.NoError(t, err)
		assert.False(t, scenarios[0].Overrides["svc.op"].HasErrorRate)
	})
}

func TestActiveScenarios(t *testing.T) {
	t.Parallel()

	scenarios := []Scenario{
		{
			Name:  "early",
			Start: 1 * time.Minute,
			End:   3 * time.Minute,
			Overrides: map[string]Override{
				"svc.op": {Duration: Distribution{Mean: 100 * time.Millisecond}},
			},
		},
		{
			Name:  "late",
			Start: 5 * time.Minute,
			End:   10 * time.Minute,
			Overrides: map[string]Override{
				"svc.op": {Duration: Distribution{Mean: 500 * time.Millisecond}},
			},
		},
	}

	t.Run("before any scenario", func(t *testing.T) {
		t.Parallel()
		active := ActiveScenarios(scenarios, 30*time.Second)
		assert.Empty(t, active)
	})

	t.Run("during first scenario", func(t *testing.T) {
		t.Parallel()
		active := ActiveScenarios(scenarios, 2*time.Minute)
		require.Len(t, active, 1)
		assert.Equal(t, "early", active[0].Name)
	})

	t.Run("between scenarios", func(t *testing.T) {
		t.Parallel()
		active := ActiveScenarios(scenarios, 4*time.Minute)
		assert.Empty(t, active)
	})

	t.Run("during second scenario", func(t *testing.T) {
		t.Parallel()
		active := ActiveScenarios(scenarios, 7*time.Minute)
		require.Len(t, active, 1)
		assert.Equal(t, "late", active[0].Name)
	})
}

func TestResolveOverrides(t *testing.T) {
	t.Parallel()

	t.Run("last defined wins for overlapping scenarios", func(t *testing.T) {
		t.Parallel()
		scenarios := []Scenario{
			{
				Overrides: map[string]Override{
					"svc.op": {
						Duration:     Distribution{Mean: 100 * time.Millisecond},
						ErrorRate:    0.1,
						HasErrorRate: true,
					},
				},
			},
			{
				Overrides: map[string]Override{
					"svc.op": {
						Duration:     Distribution{Mean: 500 * time.Millisecond},
						ErrorRate:    0.5,
						HasErrorRate: true,
					},
				},
			},
		}

		overrides := ResolveOverrides(scenarios)
		require.Contains(t, overrides, "svc.op")
		assert.Equal(t, 500*time.Millisecond, overrides["svc.op"].Duration.Mean)
		assert.InDelta(t, 0.5, overrides["svc.op"].ErrorRate, 0.001)
	})

	t.Run("partial override preserves earlier values", func(t *testing.T) {
		t.Parallel()
		scenarios := []Scenario{
			{
				Overrides: map[string]Override{
					"svc.op": {
						Duration:     Distribution{Mean: 100 * time.Millisecond},
						ErrorRate:    0.1,
						HasErrorRate: true,
					},
				},
			},
			{
				Overrides: map[string]Override{
					"svc.op": {
						Duration: Distribution{Mean: 500 * time.Millisecond},
						// No error rate override
					},
				},
			},
		}

		overrides := ResolveOverrides(scenarios)
		// Duration should be from the last scenario
		assert.Equal(t, 500*time.Millisecond, overrides["svc.op"].Duration.Mean)
		// Error rate should be from the first (since second doesn't override it)
		assert.InDelta(t, 0.1, overrides["svc.op"].ErrorRate, 0.001)
	})
}
