// Tests for scenario activation windows and override resolution
// Validates time offset parsing, window detection, and last-defined-wins semantics
package synth

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// minimalTopo returns a topology with a single "svc.op" operation for scenario tests.
func minimalTopo() *Topology {
	svc := &Service{Name: "svc", Operations: make(map[string]*Operation)}
	op := &Operation{Service: svc, Name: "op", Duration: Distribution{Mean: 10 * time.Millisecond}}
	svc.Operations["op"] = op
	return &Topology{
		Services: map[string]*Service{"svc": svc},
		Roots:    []*Operation{op},
	}
}

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
		{name: "empty", input: "", wantErr: "offset is required"},
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

		scenarios, err := BuildScenarios(cfgs, minimalTopo())
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

		scenarios, err := BuildScenarios(cfgs, minimalTopo())
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

	scenarios, err := BuildScenarios(cfgs, minimalTopo())
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
					"http.status": {Values: map[any]int{"503": 80, "200": 20}},
				},
			},
		},
	}}

	scenarios, err := BuildScenarios(cfgs, minimalTopo())
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

	_, err := BuildScenarios(cfgs, minimalTopo())
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

func TestResolveOverridesDoesNotMutateOriginal(t *testing.T) {
	t.Parallel()

	original := &StaticValue{Value: "original"}
	override := &StaticValue{Value: "override"}

	scenarios := []Scenario{
		{
			Overrides: map[string]Override{
				"svc.op": {Attributes: map[string]AttributeGenerator{"a": original}},
			},
		},
		{
			Overrides: map[string]Override{
				"svc.op": {Attributes: map[string]AttributeGenerator{"b": override}},
			},
		},
	}

	_ = ResolveOverrides(scenarios)

	// Original scenario's attribute map must not be modified
	assert.Len(t, scenarios[0].Overrides["svc.op"].Attributes, 1,
		"original scenario attributes should not be mutated")
	assert.NotContains(t, scenarios[0].Overrides["svc.op"].Attributes, "b",
		"original scenario should not contain merged attributes")
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

	scenarios, err := BuildScenarios(cfgs, minimalTopo())
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

	scenarios, err := BuildScenarios(cfgs, minimalTopo())
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

func callTopoForTests() *Topology {
	svcA := &Service{Name: "a", Operations: make(map[string]*Operation)}
	svcB := &Service{Name: "b", Operations: make(map[string]*Operation)}
	svcC := &Service{Name: "c", Operations: make(map[string]*Operation)}

	opA := &Operation{Service: svcA, Name: "op", Ref: "a.op", Duration: Distribution{Mean: 10 * time.Millisecond}}
	opB := &Operation{Service: svcB, Name: "op", Ref: "b.op", Duration: Distribution{Mean: 10 * time.Millisecond}}
	opC := &Operation{Service: svcC, Name: "op", Ref: "c.op", Duration: Distribution{Mean: 10 * time.Millisecond}}

	opA.Calls = []Call{{Operation: opB}}
	svcA.Operations["op"] = opA
	svcB.Operations["op"] = opB
	svcC.Operations["op"] = opC

	return &Topology{
		Services: map[string]*Service{"a": svcA, "b": svcB, "c": svcC},
		Roots:    []*Operation{opA, opC},
	}
}

func TestBuildScenariosWithAddCalls(t *testing.T) {
	t.Parallel()

	topo := callTopoForTests()
	cfgs := []ScenarioConfig{{
		Name:     "add-cache",
		At:       "+1m",
		Duration: "5m",
		Override: map[string]OverrideConfig{
			"a.op": {
				AddCalls: []CallConfig{{Target: "c.op", Condition: "on-error"}},
			},
		},
	}}

	scenarios, err := BuildScenarios(cfgs, topo)
	require.NoError(t, err)
	require.Len(t, scenarios, 1)

	ov := scenarios[0].Overrides["a.op"]
	require.Len(t, ov.AddCalls, 1)
	assert.Equal(t, topo.Services["c"].Operations["op"], ov.AddCalls[0].Operation)
	assert.Equal(t, "on-error", ov.AddCalls[0].Condition)
}

func TestBuildScenariosWithRemoveCalls(t *testing.T) {
	t.Parallel()

	topo := callTopoForTests()
	cfgs := []ScenarioConfig{{
		Name:     "circuit-break",
		At:       "+1m",
		Duration: "5m",
		Override: map[string]OverrideConfig{
			"a.op": {
				RemoveCalls: []RemoveCallConfig{{Target: "b.op"}},
			},
		},
	}}

	scenarios, err := BuildScenarios(cfgs, topo)
	require.NoError(t, err)
	require.Len(t, scenarios, 1)

	ov := scenarios[0].Overrides["a.op"]
	require.Len(t, ov.RemoveCalls, 1)
	assert.True(t, ov.RemoveCalls["b.op"])
}

func TestBuildScenariosUnknownAddCallTarget(t *testing.T) {
	t.Parallel()

	topo := callTopoForTests()
	cfgs := []ScenarioConfig{{
		Name:     "bad",
		At:       "+1m",
		Duration: "5m",
		Override: map[string]OverrideConfig{
			"a.op": {
				AddCalls: []CallConfig{{Target: "nonexistent.op"}},
			},
		},
	}}

	_, err := BuildScenarios(cfgs, topo)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nonexistent")
}

func TestBuildScenariosCycleDetection(t *testing.T) {
	t.Parallel()

	t.Run("add_calls creating cycle rejected", func(t *testing.T) {
		t.Parallel()
		topo := callTopoForTests() // a.op -> b.op
		cfgs := []ScenarioConfig{{
			Name:     "cyclic",
			At:       "+1m",
			Duration: "5m",
			Override: map[string]OverrideConfig{
				"b.op": {
					AddCalls: []CallConfig{{Target: "a.op"}},
				},
			},
		}}

		_, err := BuildScenarios(cfgs, topo)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "cycle")
	})

	t.Run("acyclic add_calls accepted", func(t *testing.T) {
		t.Parallel()
		topo := callTopoForTests() // a.op -> b.op, c.op standalone
		cfgs := []ScenarioConfig{{
			Name:     "ok",
			At:       "+1m",
			Duration: "5m",
			Override: map[string]OverrideConfig{
				"b.op": {
					AddCalls: []CallConfig{{Target: "c.op"}},
				},
			},
		}}

		_, err := BuildScenarios(cfgs, topo)
		require.NoError(t, err)
	})

	t.Run("remove breaks existing path so add is safe", func(t *testing.T) {
		t.Parallel()
		topo := callTopoForTests() // a.op -> b.op
		cfgs := []ScenarioConfig{{
			Name:     "swap",
			At:       "+1m",
			Duration: "5m",
			Override: map[string]OverrideConfig{
				"a.op": {
					RemoveCalls: []RemoveCallConfig{{Target: "b.op"}},
					AddCalls:    []CallConfig{{Target: "c.op"}},
				},
				"c.op": {
					AddCalls: []CallConfig{{Target: "b.op"}},
				},
			},
		}}

		_, err := BuildScenarios(cfgs, topo)
		require.NoError(t, err)
	})
}

func TestBuildScenariosCycleDetectionDeterministic(t *testing.T) {
	t.Parallel()

	// Build a topology with many nodes to make map iteration order visible
	services := make(map[string]*Service)
	ops := make(map[string]*Operation)
	for _, name := range []string{"a", "b", "c", "d", "e"} {
		svc := &Service{Name: name, Operations: make(map[string]*Operation)}
		op := &Operation{Service: svc, Name: "op", Duration: Distribution{Mean: 10 * time.Millisecond}}
		svc.Operations["op"] = op
		services[name] = svc
		ops[name] = op
	}
	// Chain: a -> b -> c -> d -> e
	ops["a"].Calls = []Call{{Operation: ops["b"]}}
	ops["b"].Calls = []Call{{Operation: ops["c"]}}
	ops["c"].Calls = []Call{{Operation: ops["d"]}}
	ops["d"].Calls = []Call{{Operation: ops["e"]}}

	topo := &Topology{Services: services, Roots: []*Operation{ops["a"]}}

	// Scenario adds e -> a, creating a cycle
	cfgs := []ScenarioConfig{{
		Name:     "cyclic",
		At:       "+1m",
		Duration: "5m",
		Override: map[string]OverrideConfig{
			"e.op": {AddCalls: []CallConfig{{Target: "a.op"}}},
		},
	}}

	// Run multiple times — error message must be identical each time
	var firstErr string
	for i := range 10 {
		_, err := BuildScenarios(cfgs, topo)
		require.Error(t, err, "iteration %d", i)
		if i == 0 {
			firstErr = err.Error()
		} else {
			assert.Equal(t, firstErr, err.Error(), "cycle error message should be deterministic (iteration %d)", i)
		}
	}
}

func TestResolveOverridesMergesCallChanges(t *testing.T) {
	t.Parallel()

	topo := callTopoForTests()
	opB := topo.Services["b"].Operations["op"]
	opC := topo.Services["c"].Operations["op"]

	t.Run("AddCalls accumulate across scenarios", func(t *testing.T) {
		t.Parallel()
		scenarios := []Scenario{
			{
				Overrides: map[string]Override{
					"a.op": {AddCalls: []Call{{Operation: opB}}},
				},
			},
			{
				Overrides: map[string]Override{
					"a.op": {AddCalls: []Call{{Operation: opC}}},
				},
			},
		}

		overrides := ResolveOverrides(scenarios)
		require.Len(t, overrides["a.op"].AddCalls, 2)
	})

	t.Run("RemoveCalls union across scenarios", func(t *testing.T) {
		t.Parallel()
		scenarios := []Scenario{
			{
				Overrides: map[string]Override{
					"a.op": {RemoveCalls: map[string]bool{"b.op": true}},
				},
			},
			{
				Overrides: map[string]Override{
					"a.op": {RemoveCalls: map[string]bool{"c.op": true}},
				},
			},
		}

		overrides := ResolveOverrides(scenarios)
		assert.True(t, overrides["a.op"].RemoveCalls["b.op"])
		assert.True(t, overrides["a.op"].RemoveCalls["c.op"])
	})

	t.Run("does not mutate original scenario AddCalls", func(t *testing.T) {
		t.Parallel()
		scenarios := []Scenario{
			{
				Overrides: map[string]Override{
					"a.op": {AddCalls: []Call{{Operation: opB}}},
				},
			},
			{
				Overrides: map[string]Override{
					"a.op": {AddCalls: []Call{{Operation: opC}}},
				},
			},
		}

		_ = ResolveOverrides(scenarios)
		assert.Len(t, scenarios[0].Overrides["a.op"].AddCalls, 1,
			"original scenario should not be mutated")
	})
}
