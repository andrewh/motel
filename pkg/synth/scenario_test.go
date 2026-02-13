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

func TestActiveScenariosOrderedByPriority(t *testing.T) {
	t.Parallel()

	scenarios := []Scenario{
		{
			Name:     "low",
			Start:    0,
			End:      10 * time.Minute,
			Priority: 1,
			Overrides: map[string]Override{
				"svc.op": {Duration: Distribution{Mean: 100 * time.Millisecond}},
			},
		},
		{
			Name:     "high",
			Start:    0,
			End:      10 * time.Minute,
			Priority: 10,
			Overrides: map[string]Override{
				"svc.op": {Duration: Distribution{Mean: 500 * time.Millisecond}},
			},
		},
		{
			Name:     "medium",
			Start:    0,
			End:      10 * time.Minute,
			Priority: 5,
			Overrides: map[string]Override{
				"svc.op": {Duration: Distribution{Mean: 200 * time.Millisecond}},
			},
		},
	}

	active := ActiveScenarios(scenarios, 5*time.Minute)
	require.Len(t, active, 3)
	assert.Equal(t, "low", active[0].Name)
	assert.Equal(t, "medium", active[1].Name)
	assert.Equal(t, "high", active[2].Name)
}

func TestActiveScenariosEqualPriorityPreservesOrder(t *testing.T) {
	t.Parallel()

	scenarios := []Scenario{
		{Name: "first", Start: 0, End: 10 * time.Minute, Priority: 0},
		{Name: "second", Start: 0, End: 10 * time.Minute, Priority: 0},
		{Name: "third", Start: 0, End: 10 * time.Minute, Priority: 0},
	}

	active := ActiveScenarios(scenarios, 5*time.Minute)
	require.Len(t, active, 3)
	assert.Equal(t, "first", active[0].Name)
	assert.Equal(t, "second", active[1].Name)
	assert.Equal(t, "third", active[2].Name)
}

func TestBuildScenariosPreservesPriority(t *testing.T) {
	t.Parallel()

	cfgs := []ScenarioConfig{{
		Name:     "important",
		At:       "+1m",
		Duration: "5m",
		Priority: 42,
		Override: map[string]OverrideConfig{
			"svc.op": {Duration: "100ms"},
		},
	}}

	scenarios, err := BuildScenarios(cfgs)
	require.NoError(t, err)
	require.Len(t, scenarios, 1)
	assert.Equal(t, 42, scenarios[0].Priority)
}

func TestResolveOverridesWithPriority(t *testing.T) {
	t.Parallel()

	// Scenarios are already sorted by priority (as ActiveScenarios would return)
	scenarios := []Scenario{
		{
			Priority: 1,
			Overrides: map[string]Override{
				"svc.op": {
					Duration:     Distribution{Mean: 100 * time.Millisecond},
					ErrorRate:    0.1,
					HasErrorRate: true,
				},
			},
		},
		{
			Priority: 10,
			Overrides: map[string]Override{
				"svc.op": {
					Duration: Distribution{Mean: 999 * time.Millisecond},
				},
			},
		},
	}

	overrides := ResolveOverrides(scenarios)
	require.Contains(t, overrides, "svc.op")
	// Higher priority scenario's duration wins
	assert.Equal(t, 999*time.Millisecond, overrides["svc.op"].Duration.Mean)
	// Lower priority scenario's error rate preserved (higher didn't set it)
	assert.InDelta(t, 0.1, overrides["svc.op"].ErrorRate, 0.001)
}

func TestBuildScenariosWithAttributes(t *testing.T) {
	t.Parallel()

	cfgs := []ScenarioConfig{{
		Name:     "error-spike",
		At:       "+1m",
		Duration: "5m",
		Override: map[string]OverrideConfig{
			"svc.op": {
				Attributes: map[string]AttributeValueConfig{
					"http.status": {Values: map[string]int{"503": 80, "200": 20}},
				},
			},
		},
	}}

	scenarios, err := BuildScenarios(cfgs)
	require.NoError(t, err)
	require.Len(t, scenarios, 1)
	require.Contains(t, scenarios[0].Overrides, "svc.op")
	require.NotNil(t, scenarios[0].Overrides["svc.op"].Attributes)
	assert.Contains(t, scenarios[0].Overrides["svc.op"].Attributes, "http.status")
}

func TestBuildScenariosInvalidAttribute(t *testing.T) {
	t.Parallel()

	cfgs := []ScenarioConfig{{
		Name:     "bad",
		At:       "+1m",
		Duration: "5m",
		Override: map[string]OverrideConfig{
			"svc.op": {
				Attributes: map[string]AttributeValueConfig{
					"bad": {Range: []int64{5, 3, 1}}, // invalid: range needs exactly 2 elements
				},
			},
		},
	}}

	_, err := BuildScenarios(cfgs)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "attribute")
}

func TestResolveOverridesMergesAttributes(t *testing.T) {
	t.Parallel()

	gen1 := &StaticValue{Value: "original"}
	gen2 := &StaticValue{Value: "override"}
	gen3 := &StaticValue{Value: "extra"}

	scenarios := []Scenario{
		{
			Overrides: map[string]Override{
				"svc.op": {
					Attributes: map[string]AttributeGenerator{
						"keep":    gen1,
						"replace": gen1,
					},
				},
			},
		},
		{
			Overrides: map[string]Override{
				"svc.op": {
					Attributes: map[string]AttributeGenerator{
						"replace": gen2,
						"new":     gen3,
					},
				},
			},
		},
	}

	overrides := ResolveOverrides(scenarios)
	require.Contains(t, overrides, "svc.op")
	attrs := overrides["svc.op"].Attributes
	require.Len(t, attrs, 3)
	assert.Equal(t, gen1, attrs["keep"], "untouched attribute preserved")
	assert.Equal(t, gen2, attrs["replace"], "overridden attribute replaced")
	assert.Equal(t, gen3, attrs["new"], "new attribute added")
}

func TestResolveOverridesNoAttributesIsNoop(t *testing.T) {
	t.Parallel()

	gen := &StaticValue{Value: "v"}
	scenarios := []Scenario{
		{
			Overrides: map[string]Override{
				"svc.op": {Attributes: map[string]AttributeGenerator{"a": gen}},
			},
		},
		{
			Overrides: map[string]Override{
				"svc.op": {Duration: Distribution{Mean: 100 * time.Millisecond}},
			},
		},
	}

	overrides := ResolveOverrides(scenarios)
	attrs := overrides["svc.op"].Attributes
	require.Len(t, attrs, 1)
	assert.Equal(t, gen, attrs["a"], "earlier attributes preserved when later has none")
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

func TestBuildScenariosWithTraffic(t *testing.T) {
	t.Parallel()

	cfgs := []ScenarioConfig{{
		Name:     "spike",
		At:       "+1m",
		Duration: "5m",
		Traffic:  &TrafficConfig{Rate: "500/s"},
	}}

	scenarios, err := BuildScenarios(cfgs)
	require.NoError(t, err)
	require.Len(t, scenarios, 1)
	require.NotNil(t, scenarios[0].Traffic)
	assert.InDelta(t, 500.0, scenarios[0].Traffic.Rate(0), 0.1)
}

func TestBuildScenariosWithoutTraffic(t *testing.T) {
	t.Parallel()

	cfgs := []ScenarioConfig{{
		Name:     "slow",
		At:       "+1m",
		Duration: "5m",
		Override: map[string]OverrideConfig{
			"svc.op": {Duration: "100ms"},
		},
	}}

	scenarios, err := BuildScenarios(cfgs)
	require.NoError(t, err)
	require.Len(t, scenarios, 1)
	assert.Nil(t, scenarios[0].Traffic)
}

func TestResolveTraffic(t *testing.T) {
	t.Parallel()

	lowPattern, err := NewTrafficPattern(TrafficConfig{Rate: "100/s"})
	require.NoError(t, err)
	highPattern, err := NewTrafficPattern(TrafficConfig{Rate: "500/s"})
	require.NoError(t, err)

	t.Run("highest priority with traffic wins", func(t *testing.T) {
		t.Parallel()
		active := []Scenario{
			{Priority: 1, Traffic: lowPattern},
			{Priority: 10, Traffic: highPattern},
		}
		tp := ResolveTraffic(active)
		require.NotNil(t, tp)
		assert.InDelta(t, 500.0, tp.Rate(0), 0.1)
	})

	t.Run("nil when no scenarios have traffic", func(t *testing.T) {
		t.Parallel()
		active := []Scenario{
			{Priority: 1},
			{Priority: 10},
		}
		tp := ResolveTraffic(active)
		assert.Nil(t, tp)
	})

	t.Run("skips scenarios without traffic", func(t *testing.T) {
		t.Parallel()
		active := []Scenario{
			{Priority: 1, Traffic: lowPattern},
			{Priority: 10}, // no traffic
		}
		tp := ResolveTraffic(active)
		require.NotNil(t, tp)
		assert.InDelta(t, 100.0, tp.Rate(0), 0.1)
	})
}
