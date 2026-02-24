// Per-operation runtime state for cross-trace simulation effects
// Tracks queue depth, circuit breaker status, and backpressure for each operation
package synth

import (
	"math/rand/v2"
	"time"
)

// Rejection reason constants for span attributes.
const (
	ReasonQueueFull   = "queue_full"
	ReasonCircuitOpen = "circuit_open"

	// Backpressure tuning constants.
	backpressureAlpha         = 0.3
	maxBackpressureMultiplier = 10.0
	rejectionDuration         = 1 * time.Millisecond
)

// CircuitState represents the state of a circuit breaker.
type CircuitState int

// Circuit breaker states.
const (
	CircuitClosed   CircuitState = iota // CircuitClosed allows all requests through.
	CircuitOpen                         // CircuitOpen rejects all requests.
	CircuitHalfOpen                     // CircuitHalfOpen allows a probe request to test recovery.
)

// SimulationState tracks cross-trace state for operations during a run.
// Only operations with queue_depth, backpressure, or circuit_breaker config
// get an entry — unconfigured operations are unaffected.
//
// State persists for the entire simulation, including across scenario boundaries.
// After a scenario ends, effects like open circuit breakers and backpressure
// remain until the system naturally recovers (e.g. cooldown expires, latency drops).
// This matches real-world behaviour where removing the cause of degradation
// does not instantly reset the symptoms.
type SimulationState struct {
	operations map[string]*OperationState
}

// OperationState holds runtime state for a single operation across traces.
// Not safe for concurrent use. The engine calls all methods from a single goroutine.
type OperationState struct {
	ActiveRequests int
	MaxQueueDepth  int

	BackpressureThreshold time.Duration
	DurationMultiplier    float64
	ErrorRateAdd          float64
	RecentLatency         time.Duration
	BackpressureActive    bool

	FailureWindow    []failureRecord
	Circuit          CircuitState
	OpenedAt         time.Duration
	Cooldown         time.Duration
	FailureThreshold int
	WindowDuration   time.Duration
}

type failureRecord struct {
	At time.Duration
}

// NewSimulationState builds state from topology operations that have
// queue depth, backpressure, or circuit breaker configuration.
func NewSimulationState(topo *Topology) *SimulationState {
	s := &SimulationState{
		operations: make(map[string]*OperationState),
	}
	for _, svc := range topo.Services {
		for _, op := range svc.Operations {
			if op.QueueDepth == 0 && op.Backpressure == nil && op.CircuitBreaker == nil {
				continue
			}
			ref := svc.Name + "." + op.Name
			os := &OperationState{
				MaxQueueDepth: op.QueueDepth,
			}
			if op.CircuitBreaker != nil {
				os.FailureThreshold = op.CircuitBreaker.FailureThreshold
				os.WindowDuration = op.CircuitBreaker.Window
				os.Cooldown = op.CircuitBreaker.Cooldown
			}
			if op.Backpressure != nil {
				os.BackpressureThreshold = op.Backpressure.LatencyThreshold
				os.DurationMultiplier = op.Backpressure.DurationMultiplier
				os.ErrorRateAdd = op.Backpressure.ErrorRateAdd
			}
			s.operations[ref] = os
		}
	}
	return s
}

// Get returns the state for an operation, or nil if not tracked.
func (s *SimulationState) Get(ref string) *OperationState {
	if s == nil {
		return nil
	}
	return s.operations[ref]
}

// Admit checks operation state and returns adjustments for the current request.
// Mutates circuit breaker state (e.g. Open→HalfOpen transition on cooldown expiry).
// Returns the adjusted duration multiplier, additional error rate, and whether
// the request should be rejected outright.
func (os *OperationState) Admit(elapsed time.Duration, rng *rand.Rand) (durationMult float64, errorRateAdd float64, rejected bool, reason string) {
	durationMult = 1.0

	if os.Circuit == CircuitOpen {
		if elapsed-os.OpenedAt >= os.Cooldown {
			os.Circuit = CircuitHalfOpen
		} else {
			return 0, 0, true, ReasonCircuitOpen
		}
	}

	if os.MaxQueueDepth > 0 && os.ActiveRequests >= os.MaxQueueDepth {
		return 0, 0, true, ReasonQueueFull
	}

	if os.BackpressureActive {
		durationMult = os.DurationMultiplier
		if durationMult <= 0 {
			durationMult = 1.0
		}
		if durationMult > maxBackpressureMultiplier {
			durationMult = maxBackpressureMultiplier
		}
		errorRateAdd = os.ErrorRateAdd
	}

	return durationMult, errorRateAdd, false, ""
}

// Enter increments the active request count.
func (os *OperationState) Enter() {
	os.ActiveRequests++
}

// Exit decrements the active request count and records the outcome.
func (os *OperationState) Exit(elapsed time.Duration, latency time.Duration, failed bool) {
	os.ActiveRequests--
	if os.ActiveRequests < 0 {
		os.ActiveRequests = 0
	}

	if os.BackpressureThreshold > 0 {
		if os.RecentLatency == 0 {
			os.RecentLatency = latency
		} else {
			os.RecentLatency = time.Duration(
				backpressureAlpha*float64(latency) +
					(1-backpressureAlpha)*float64(os.RecentLatency),
			)
		}
		os.BackpressureActive = os.RecentLatency > os.BackpressureThreshold
	}

	if os.WindowDuration > 0 {
		cutoff := elapsed - os.WindowDuration
		pruned := os.FailureWindow[:0]
		for _, r := range os.FailureWindow {
			if r.At >= cutoff {
				pruned = append(pruned, r)
			}
		}
		os.FailureWindow = pruned
	}
	// Append after pruning, and only up to threshold — we only need to know
	// whether the count reaches the threshold, not track every failure.
	if os.FailureThreshold > 0 && failed && len(os.FailureWindow) < os.FailureThreshold {
		os.FailureWindow = append(os.FailureWindow, failureRecord{At: elapsed})
	}

	if os.FailureThreshold > 0 && len(os.FailureWindow) >= os.FailureThreshold && os.Circuit == CircuitClosed {
		os.Circuit = CircuitOpen
		os.OpenedAt = elapsed
	}

	if os.Circuit == CircuitHalfOpen {
		if failed {
			os.Circuit = CircuitOpen
			os.OpenedAt = elapsed
		} else {
			os.Circuit = CircuitClosed
			os.FailureWindow = os.FailureWindow[:0]
		}
	}
}
