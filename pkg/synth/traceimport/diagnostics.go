package traceimport

import (
	"fmt"
	"io"
	"sort"
)

const (
	lowConfidenceCallObservations = 3
	lowConfidenceCallStyleVotes   = 3
)

func reportConfidenceDiagnostics(collector *StatsCollector, minSamples int, w io.Writer) {
	if collector == nil || w == nil {
		return
	}
	if minSamples < 1 {
		minSamples = 1
	}

	for _, svcName := range sortedServiceNames(collector.Services) {
		svc := collector.Services[svcName]
		for _, opName := range sortedOpNames(svc.Ops) {
			op := svc.Ops[opName]
			ref := svcName + "." + opName
			if op.TotalCount < minSamples {
				_, _ = fmt.Fprintf(w, "warning: import confidence: %s has %d samples below requested minimum %d; duration and error_rate estimates may be noisy (errors=%d)\n",
					ref, op.TotalCount, minSamples, op.ErrorCount)
			}
			for _, target := range sortedCallTargets(op.Calls) {
				cs := op.Calls[target]
				if cs.Count <= lowConfidenceCallObservations {
					_, _ = fmt.Fprintf(w, "warning: import confidence: %s -> %s observed %d times across %d parent samples; inferred call probability needs review\n",
						ref, target, cs.Count, op.TotalCount)
				}
			}
			vote, ok := svc.CallStyles[opName]
			if !ok {
				continue
			}
			totalVotes := vote.Parallel + vote.Sequential
			if totalVotes <= lowConfidenceCallStyleVotes || vote.Parallel > 0 && vote.Sequential > 0 {
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

func sortedServiceNames(services map[string]*ServiceStats) []string {
	names := make([]string, 0, len(services))
	for name := range services {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func sortedOpNames(ops map[string]*OpStats) []string {
	names := make([]string, 0, len(ops))
	for name := range ops {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func sortedCallTargets(calls map[string]*CallStats) []string {
	targets := make([]string, 0, len(calls))
	for target := range calls {
		targets = append(targets, target)
	}
	sort.Strings(targets)
	return targets
}
