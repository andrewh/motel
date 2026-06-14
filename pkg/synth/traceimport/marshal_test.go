// Unit tests for YAML serialisation of inferred configs
// Verifies output format and round-trip through synth.LoadConfig + ValidateConfig
package traceimport

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMarshalConfig_Basic(t *testing.T) {
	collector := NewStatsCollector()
	svc := collector.getService("api")
	op := collector.getOp(svc, "GET /users")
	recordTestDuration(op, 30*time.Millisecond, 1)
	recordTestDuration(op, 40*time.Millisecond, 1)

	attrs := map[string]map[string]string{
		"api": {"env": "prod"},
	}

	data, err := MarshalConfig(collector, attrs, 2, 2, 1.0)
	require.NoError(t, err)

	yaml := string(data)
	assert.Contains(t, yaml, "version: 1")
	assert.Contains(t, yaml, "api:")
	assert.Contains(t, yaml, "GET /users:")
	assert.Contains(t, yaml, "duration:")
	assert.Contains(t, yaml, "rate:")
	assert.Contains(t, yaml, "env: prod")
}

func TestMarshalConfig_Header(t *testing.T) {
	collector := NewStatsCollector()
	svc := collector.getService("api")
	op := collector.getOp(svc, "handle")
	recordTestDuration(op, 10*time.Millisecond, 1)

	data, err := MarshalConfig(collector, nil, 4, 10, 1.42)
	require.NoError(t, err)

	// Window is rendered with one decimal place, not rounded to a whole number.
	assert.Contains(t, string(data), "# Inferred from 4 traces (10 spans) observed over 1.4 seconds")
}

func TestMarshalConfig_RoundTrip(t *testing.T) {
	collector := NewStatsCollector()

	// Build a small topology: gateway -> backend
	gw := collector.getService("gateway")
	gwOp := collector.getOp(gw, "handle")
	recordTestDuration(gwOp, 50*time.Millisecond, 1)
	gwOp.Calls = map[string]*CallStats{
		"backend.process": {Count: 1},
	}

	be := collector.getService("backend")
	beOp := collector.getOp(be, "process")
	recordTestDuration(beOp, 20*time.Millisecond, 1)

	data, err := MarshalConfig(collector, nil, 1, 2, 0)
	require.NoError(t, err)

	// Validate it round-trips
	err = validateRoundTrip(data)
	require.NoError(t, err)
}

func TestMarshalConfig_WithProbability(t *testing.T) {
	collector := NewStatsCollector()
	svc := collector.getService("api")
	op := collector.getOp(svc, "handle")
	recordTestDuration(op, 10*time.Millisecond, 10)
	op.Calls = map[string]*CallStats{
		"cache.get": {Count: 5},
	}

	cache := collector.getService("cache")
	cacheOp := collector.getOp(cache, "get")
	recordTestDuration(cacheOp, time.Millisecond, 5)

	data, err := MarshalConfig(collector, nil, 10, 15, 1.0)
	require.NoError(t, err)

	yaml := string(data)
	assert.Contains(t, yaml, "probability:")
	assert.Contains(t, yaml, "0.5")
}

func TestMarshalConfig_SequentialCallStyle(t *testing.T) {
	collector := NewStatsCollector()
	svc := collector.getService("api")
	op := collector.getOp(svc, "handle")
	recordTestDuration(op, 10*time.Millisecond, 1)
	svc.CallStyles["handle"] = &CallStyleVote{Sequential: 5, Parallel: 1}

	data, err := MarshalConfig(collector, nil, 1, 1, 0)
	require.NoError(t, err)

	yaml := string(data)
	assert.Contains(t, yaml, "call_style: sequential")
}

func recordTestDuration(op *OpStats, d time.Duration, count int) {
	op.RecordDuration(d, count)
	op.TotalCount += count
}
