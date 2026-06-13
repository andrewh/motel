package synth

import (
	"errors"
	"fmt"
	"maps"
	"math/rand/v2"
	"slices"
	"strings"
)

// SampleStrategy controls how sampled traces explore probabilistic choices.
type SampleStrategy string

const (
	SampleStrategyRandom SampleStrategy = "random"
	SampleStrategySwarm  SampleStrategy = "swarm"
)

const (
	defaultSwarmFixProbability = 0.35
	swarmAllEnabledRun         = 0
	swarmSuccessEnabledRun     = 1
	swarmAllDisabledRun        = 2
	swarmDirectedRunOffset     = 3
	swarmDecisionStream        = 1
)

var ErrInvalidSampleStrategy = errors.New("invalid sample strategy")

// ParseSampleStrategy parses a user-facing sampling strategy value.
func ParseSampleStrategy(value string) (SampleStrategy, error) {
	switch SampleStrategy(strings.ToLower(strings.TrimSpace(value))) {
	case "", SampleStrategyRandom:
		return SampleStrategyRandom, nil
	case SampleStrategySwarm:
		return SampleStrategySwarm, nil
	default:
		return "", fmt.Errorf("%w %q (expected %q or %q)", ErrInvalidSampleStrategy, value, SampleStrategyRandom, SampleStrategySwarm)
	}
}

func normalizeSampleStrategy(strategy SampleStrategy) SampleStrategy {
	switch strategy {
	case SampleStrategySwarm:
		return SampleStrategySwarm
	default:
		return SampleStrategyRandom
	}
}

type choiceKind string

const (
	choiceKindCallProbability choiceKind = "call-probability"
	choiceKindOperationError  choiceKind = "operation-error"
	choiceKindRetryActivation choiceKind = "retry-activation"
)

type choiceKey struct {
	kind         choiceKind
	operationRef string
	targetRef    string
	callIndex    int
}

type choicePoint struct {
	key choiceKey
}

type choiceDecisions map[choiceKey]bool

func (d choiceDecisions) lookup(kind choiceKind, operationRef, targetRef string, callIndex int) (bool, bool) {
	if len(d) == 0 {
		return false, false
	}
	v, ok := d[choiceKey{
		kind:         kind,
		operationRef: operationRef,
		targetRef:    targetRef,
		callIndex:    callIndex,
	}]
	return v, ok
}

func (e *Engine) forcedChoice(kind choiceKind, operationRef, targetRef string, callIndex int) (bool, bool) {
	if e.choiceDecisions == nil {
		return false, false
	}
	return e.choiceDecisions.lookup(kind, operationRef, targetRef, callIndex)
}

func effectiveErrorRate(op *Operation, overrides map[string]Override) float64 {
	errorRate := op.ErrorRate
	if ov, ok := overrides[op.Ref]; ok && ov.HasErrorRate {
		errorRate = ov.ErrorRate
	}
	return errorRate
}

func isChoiceRate(rate float64) bool {
	return rate > 0 && rate < 1
}

func enumerateChoicePoints(topo *Topology, overrides map[string]Override) []choicePoint {
	var points []choicePoint
	for _, svcName := range slices.Sorted(maps.Keys(topo.Services)) {
		svc := topo.Services[svcName]
		for _, opName := range slices.Sorted(maps.Keys(svc.Operations)) {
			op := svc.Operations[opName]
			if isChoiceRate(effectiveErrorRate(op, overrides)) {
				points = append(points, choicePoint{key: choiceKey{
					kind:         choiceKindOperationError,
					operationRef: op.Ref,
					callIndex:    -1,
				}})
			}
			for i, call := range effectiveCalls(op, overrides) {
				if isChoiceRate(call.Probability) {
					points = append(points, choicePoint{key: choiceKey{
						kind:         choiceKindCallProbability,
						operationRef: op.Ref,
						targetRef:    call.Operation.Ref,
						callIndex:    i,
					}})
				}
				if call.Retries > 0 {
					points = append(points, choicePoint{key: choiceKey{
						kind:         choiceKindRetryActivation,
						operationRef: op.Ref,
						targetRef:    call.Operation.Ref,
						callIndex:    i,
					}})
				}
			}
		}
	}
	return points
}

func swarmChoices(points []choicePoint, run int, rng *rand.Rand) choiceDecisions {
	if len(points) == 0 {
		return nil
	}

	decisions := make(choiceDecisions, len(points))
	switch run {
	case swarmAllEnabledRun:
		for _, point := range points {
			decisions[point.key] = true
		}
		return decisions
	case swarmSuccessEnabledRun:
		for _, point := range points {
			decisions[point.key] = point.key.kind != choiceKindOperationError
		}
		return decisions
	case swarmAllDisabledRun:
		for _, point := range points {
			decisions[point.key] = false
		}
		return decisions
	}

	for _, point := range points {
		if rng.Float64() < defaultSwarmFixProbability {
			decisions[point.key] = rng.IntN(2) == 0
		}
	}

	directedRun := run - swarmDirectedRunOffset
	if directedRun >= 0 && directedRun < len(points)*2 {
		point := points[directedRun/2]
		decisions[point.key] = directedRun%2 == 0
	}

	return decisions
}
