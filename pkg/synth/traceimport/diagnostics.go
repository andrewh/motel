package traceimport

import (
	"fmt"
	"io"
)

const mixedCallStyleVoteRatio = 0.2

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
			ref := svcName + "." + opName
			if op.TotalCount < minSamples {
				_, _ = fmt.Fprintf(w, "warning: import confidence: %s has %d operation samples below requested target %d from --min-traces; duration and error_rate estimates may be noisy (errors=%d)\n",
					ref, op.TotalCount, minSamples, op.ErrorCount)
			}
			for _, target := range sortedStringKeys(op.Calls) {
				cs := op.Calls[target]
				if minSamples > 1 && cs.Count < minSamples && cs.Count < op.TotalCount {
					_, _ = fmt.Fprintf(w, "warning: import confidence: %s -> %s observed %d times across %d parent samples; inferred call probability needs review\n",
						ref, target, cs.Count, op.TotalCount)
				}
			}
			vote, ok := svc.CallStyles[opName]
			if !ok {
				continue
			}
			totalVotes := vote.Parallel + vote.Sequential
			if minSamples > 1 && (totalVotes < minSamples || hasMixedCallStyleEvidence(vote)) {
				style := "parallel"
				if vote.Sequential > vote.Parallel {
					style = "sequential"
				}
				_, _ = fmt.Fprintf(w, "warning: import confidence: %s call_style inferred as %s from parallel=%d sequential=%d votes; verify weak or mixed evidence\n",
					ref, style, vote.Parallel, vote.Sequential)
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
