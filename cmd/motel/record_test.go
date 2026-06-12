package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/andrewh/motel/pkg/synth"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testRunHeader() runHeader {
	return runHeader{
		Type:     recordTypeRun,
		Version:  runLogVersion,
		Motel:    "test",
		Topology: "t.yaml",
		Seed:     42,
		StartMs:  1000,
		Scenarios: []scenarioWindow{
			{Name: "spike", StartMs: 5000, EndMs: 15000},
		},
	}
}

func writeTestRunLog(t *testing.T, path string) {
	t.Helper()
	epoch := time.UnixMilli(1000)
	rec, err := newRunRecorder(path, testRunHeader(), epoch)
	require.NoError(t, err)

	rec.Observe(synth.SpanInfo{
		Service:   "gateway",
		Operation: "GET /users",
		Timestamp: epoch.Add(10 * time.Millisecond),
		Duration:  30 * time.Millisecond,
	})
	rec.Observe(synth.SpanInfo{
		Service:         "backend",
		Operation:       "list",
		ParentService:   "gateway",
		ParentOperation: "GET /users",
		Timestamp:       epoch.Add(15 * time.Millisecond),
		Duration:        20 * time.Millisecond,
		IsError:         true,
	})
	rec.ObservePlanEvent(synth.PlanEvent{
		Kind:      synth.PlanEventRetry,
		Service:   "backend",
		Operation: "list",
		Timestamp: epoch.Add(40 * time.Millisecond),
	})

	require.NoError(t, rec.finish(&synth.Stats{Traces: 1, Spans: 2, ElapsedMs: 50}))
}

func TestRunRecorderRoundTrip(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"run.jsonl", "run.jsonl.gz"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), name)
			writeTestRunLog(t, path)

			log, err := loadRunLog(path)
			require.NoError(t, err)

			assert.Equal(t, testRunHeader(), log.Header)

			require.Len(t, log.Spans, 2)
			assert.Equal(t, 10.0, log.Spans[0].T)
			assert.Equal(t, 30.0, log.Spans[0].D)
			assert.Equal(t, "gateway", log.Spans[0].Service)
			assert.Empty(t, log.Spans[0].ParentService)
			assert.Equal(t, "gateway", log.Spans[1].ParentService)
			assert.Equal(t, "GET /users", log.Spans[1].ParentOperation)
			assert.True(t, log.Spans[1].Error)

			require.Len(t, log.Plans, 1)
			assert.Equal(t, synth.PlanEventRetry, log.Plans[0].Kind)
			assert.Equal(t, 40.0, log.Plans[0].T)

			require.NotNil(t, log.Stats)
			assert.Equal(t, int64(2), log.Stats.Stats.Spans)
			assert.Equal(t, 50.0, log.Stats.T)
		})
	}
}

func TestLoadRunLogErrors(t *testing.T) {
	t.Parallel()

	t.Run("missing file", func(t *testing.T) {
		t.Parallel()
		_, err := loadRunLog("/nonexistent.jsonl")
		require.Error(t, err)
	})

	t.Run("missing header", func(t *testing.T) {
		t.Parallel()
		path := filepath.Join(t.TempDir(), "rec.jsonl")
		require.NoError(t, os.WriteFile(path, []byte(`{"type":"span","t":0,"d":1,"svc":"a","op":"b"}`+"\n"), 0o600))
		_, err := loadRunLog(path)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no run header")
	})

	t.Run("old snapshot format is rejected", func(t *testing.T) {
		t.Parallel()
		path := filepath.Join(t.TempDir(), "rec.jsonl")
		old := `{"timestampMs":1000,"services":{"gateway":{"spans":1}}}` + "\n"
		require.NoError(t, os.WriteFile(path, []byte(old), 0o600))
		_, err := loadRunLog(path)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "unknown record type")
	})

	t.Run("newer format version is rejected", func(t *testing.T) {
		t.Parallel()
		path := filepath.Join(t.TempDir(), "rec.jsonl")
		content := `{"type":"run","v":99,"startMs":0}
{"type":"span","t":0,"d":1,"svc":"a","op":"b"}
`
		require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
		_, err := loadRunLog(path)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "newer than this motel supports")
	})

	t.Run("no spans is an error", func(t *testing.T) {
		t.Parallel()
		path := filepath.Join(t.TempDir(), "rec.jsonl")
		require.NoError(t, os.WriteFile(path, []byte(`{"type":"run","v":1,"startMs":0}`+"\n"), 0o600))
		_, err := loadRunLog(path)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no spans")
	})
}

func TestBucketSnapshots(t *testing.T) {
	t.Parallel()

	log := &runLog{
		Header: runHeader{StartMs: 1000},
		Spans: []spanRecord{
			{T: 0, D: 30, Service: "gateway"},
			{T: 5, D: 20, Service: "backend", Error: true},
			{T: 600, D: 10, Service: "gateway"},
		},
	}

	snaps := bucketSnapshots(log, 500*time.Millisecond)
	require.Len(t, snaps, 3)

	baseline := snaps[0]
	assert.Equal(t, int64(1000), baseline.TimestampMs, "baseline at run start")
	assert.Empty(t, baseline.Services)

	first := snaps[1]
	assert.Equal(t, int64(1500), first.TimestampMs)
	assert.Equal(t, int64(1), first.Services["gateway"].Spans)
	assert.Equal(t, int64(1), first.Services["backend"].Spans)
	assert.Equal(t, int64(1), first.Services["backend"].Errors)

	second := snaps[2]
	assert.Equal(t, int64(2000), second.TimestampMs)
	assert.Equal(t, int64(2), second.Services["gateway"].Spans, "counters are cumulative")
	assert.Equal(t, int64(1), second.Services["backend"].Spans)
}

func TestBucketSnapshotsCountsSpansAtEnd(t *testing.T) {
	t.Parallel()

	// A span starting in bucket 1 but ending in bucket 2 counts in bucket 2.
	log := &runLog{
		Spans: []spanRecord{
			{T: 400, D: 300, Service: "svc"},
		},
	}
	snaps := bucketSnapshots(log, 500*time.Millisecond)
	require.Len(t, snaps, 3)
	assert.Empty(t, snaps[1].Services, "span has not ended by the first boundary")
	assert.Equal(t, int64(1), snaps[2].Services["svc"].Spans)
}

func TestRunLogDuration(t *testing.T) {
	t.Parallel()

	t.Run("uses stats trailer when present", func(t *testing.T) {
		t.Parallel()
		log := &runLog{Stats: &statsRecord{T: 60000}}
		assert.Equal(t, time.Minute, runLogDuration(log))
	})

	t.Run("falls back to latest span end", func(t *testing.T) {
		t.Parallel()
		log := &runLog{Spans: []spanRecord{{T: 100, D: 50}, {T: 400, D: 30}}}
		assert.Equal(t, 430*time.Millisecond, runLogDuration(log))
	})
}

func TestReplayInterval(t *testing.T) {
	t.Parallel()

	tests := []struct {
		total    time.Duration
		expected time.Duration
	}{
		{30 * time.Second, defaultReplayInterval},
		{5 * time.Minute, time.Second},
		{15 * time.Minute, 2 * time.Second},
		{time.Hour, 5 * time.Second},
	}
	for _, tt := range tests {
		t.Run(tt.total.String(), func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.expected, replayInterval(tt.total))
		})
	}
}

func TestGraphSessionHeaderConsistentWithTimeOffset(t *testing.T) {
	t.Parallel()

	topoPath := writeTestConfig(t, validConfig)
	cfg, err := synth.LoadConfig(topoPath)
	require.NoError(t, err)
	require.NoError(t, synth.ValidateConfig(cfg))
	topo, err := synth.BuildTopology(cfg, nil)
	require.NoError(t, err)

	recPath := filepath.Join(t.TempDir(), "rec.jsonl")
	offset := -time.Hour
	before := time.Now().Add(offset)
	sess, err := startGraphSession("", recPath, topo, topoPath, nil, runOptions{timeOffset: offset, seed: 7})
	require.NoError(t, err)
	after := time.Now().Add(offset)
	require.NoError(t, sess.close(nil))

	data, err := os.ReadFile(recPath)
	require.NoError(t, err)
	var header runHeader
	require.NoError(t, json.Unmarshal(bytesBeforeNewline(data), &header))

	assert.Equal(t, offset.Milliseconds(), header.TimeOffsetMs)
	assert.GreaterOrEqual(t, header.StartMs, before.UnixMilli(), "StartMs is the simulated epoch, not wall start")
	assert.LessOrEqual(t, header.StartMs, after.UnixMilli())
	assert.Equal(t, uint64(7), header.Seed)
}

func bytesBeforeNewline(b []byte) []byte {
	for i, c := range b {
		if c == '\n' {
			return b[:i]
		}
	}
	return b
}
