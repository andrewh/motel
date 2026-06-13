package traceimport

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestReportConfidenceDiagnostics(t *testing.T) {
	t.Parallel()

	collector := &StatsCollector{
		Services: map[string]*ServiceStats{
			"api": {
				Ops: map[string]*OpStats{
					"handle": {
						ErrorCount: 1,
						TotalCount: 2,
						Calls: map[string]*CallStats{
							"cache.lookup": {Count: 1},
						},
					},
				},
				CallStyles: map[string]*CallStyleVote{
					"handle": {Parallel: 1, Sequential: 1},
				},
			},
		},
	}

	var warnings bytes.Buffer
	reportConfidenceDiagnostics(collector, 5, &warnings)

	out := warnings.String()
	assert.Contains(t, out, "api.handle has 2 samples below requested minimum 5")
	assert.Contains(t, out, "errors=1")
	assert.Contains(t, out, "api.handle -> cache.lookup observed 1 times across 2 parent samples")
	assert.Contains(t, out, "api.handle call_style inferred as parallel from parallel=1 sequential=1 votes")
}

func TestReportConfidenceDiagnostics_SkipsStrongEvidence(t *testing.T) {
	t.Parallel()

	collector := &StatsCollector{
		Services: map[string]*ServiceStats{
			"api": {
				Ops: map[string]*OpStats{
					"handle": {
						TotalCount: 10,
						Calls: map[string]*CallStats{
							"database.query": {Count: 10},
						},
					},
				},
				CallStyles: map[string]*CallStyleVote{
					"handle": {Parallel: 6},
				},
			},
		},
	}

	var warnings bytes.Buffer
	reportConfidenceDiagnostics(collector, 5, &warnings)

	assert.Empty(t, warnings.String())
}
