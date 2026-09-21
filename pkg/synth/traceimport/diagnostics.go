package traceimport

import (
	"fmt"
	"io"
)

const (
	mixedCallStyleVoteRatio = 0.2
	inferredParallel        = "parallel"
	inferredSequential      = "sequential"
)

func reportConfidenceDiagnostics(collector *StatsCollector, minSamples int, w io.Writer) {
	if collector == nil || w == nil {
		return
	}
	if minSamples < 1 {
		minSamples = 1
	}

	for _, svcName := range sortedStringKeys(collector.Services) {
		svc := collector.Services[svcName]
		for _, opName := range sortedStringKeys(svc.Ops) {
			op := svc.Ops[opName]
			evidence := assessInference(op, svc.CallStyles[opName], minSamples)
			ref := svcName + "." + opName
			if len(evidence.Reasons) > 0 {
				_, _ = fmt.Fprintf(w, "warning: import confidence: %s has %d operation samples below requested target %d from --min-traces; duration and error_rate estimates may be noisy (errors=%d)\n",
					ref, op.TotalCount, minSamples, evidence.ErrorCount)
			}
			for _, target := range sortedStringKeys(evidence.Calls) {
				call := evidence.Calls[target]
				if len(call.Reasons) > 0 {
					_, _ = fmt.Fprintf(w, "warning: import confidence: %s -> %s observed %d times across %d parent samples; inferred call probability needs review\n", ref, target, call.PresentCount, op.TotalCount)
				}
			}
			style := evidence.CallStyle
			if len(style.Reasons) > 0 {
				_, _ = fmt.Fprintf(w, "warning: import confidence: %s call_style inferred as %s from parallel=%d sequential=%d votes; verify weak or mixed evidence\n", ref, style.InferredStyle, style.ParallelVotes, style.SequentialVotes)
			}
		}
	}
}

func hasMixedCallStyleEvidence(vote *CallStyleVote) bool {
	total := vote.Parallel + vote.Sequential
	if total == 0 || vote.Parallel == 0 || vote.Sequential == 0 {
		return false
	}
	minority := vote.Parallel
	if vote.Sequential < minority {
		minority = vote.Sequential
	}
	return float64(minority)/float64(total) >= mixedCallStyleVoteRatio
}

type InferenceEvidence struct {
	ErrorCount int                     `yaml:"error_count"`
	Calls      map[string]CallEvidence `yaml:"calls"`
	CallStyle  CallStyleEvidence       `yaml:"call_style"`
	Reasons    []string                `yaml:"reasons"`
}

type CallEvidence struct {
	PresentCount    int      `yaml:"present_count"`
	OccurrenceCount int      `yaml:"occurrence_count"`
	Reasons         []string `yaml:"reasons"`
}

type CallStyleEvidence struct {
	ParallelVotes   int      `yaml:"parallel_votes"`
	SequentialVotes int      `yaml:"sequential_votes"`
	InferredStyle   string   `yaml:"inferred_style"`
	Basis           string   `yaml:"basis"`
	Reasons         []string `yaml:"reasons"`
}

func assessInference(op *OpStats, vote *CallStyleVote, minSamples int) InferenceEvidence {
	minSamples = max(1, minSamples)
	evidence := InferenceEvidence{ErrorCount: op.ErrorCount, Calls: map[string]CallEvidence{}, Reasons: []string{}, CallStyle: CallStyleEvidence{InferredStyle: inferredParallel, Basis: "default", Reasons: []string{}}}
	if op.TotalCount < minSamples {
		evidence.Reasons = append(evidence.Reasons, "below_requested_operation_samples")
	}
	for target, call := range op.Calls {
		assessed := CallEvidence{PresentCount: call.Count, OccurrenceCount: call.Occurrences, Reasons: []string{}}
		if minSamples > 1 && call.Count < minSamples && call.Count < op.TotalCount {
			assessed.Reasons = append(assessed.Reasons, "below_requested_call_samples")
		}
		evidence.Calls[target] = assessed
	}
	if vote != nil {
		evidence.CallStyle.ParallelVotes = vote.Parallel
		evidence.CallStyle.SequentialVotes = vote.Sequential
		evidence.CallStyle.Basis = "observed_votes"
		if vote.Sequential > vote.Parallel {
			evidence.CallStyle.InferredStyle = inferredSequential
		}
		if minSamples > 1 && (vote.Parallel+vote.Sequential < minSamples || hasMixedCallStyleEvidence(vote)) {
			evidence.CallStyle.Reasons = append(evidence.CallStyle.Reasons, "weak_or_mixed_call_style")
		}
	}
	return evidence
}
