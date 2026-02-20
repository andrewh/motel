// Topology graph construction from parsed YAML configuration
// Resolves string references to pointers, detects root operations and cycles
package synth

import (
	"cmp"
	"fmt"
	"slices"
	"strings"
	"time"
)

// Topology is the resolved service graph ready for simulation.
type Topology struct {
	Services map[string]*Service
	Roots    []*Operation
}

// Service represents a resolved service node in the topology graph.
type Service struct {
	Name       string
	Operations map[string]*Operation
	Attributes map[string]string
}

// ResolvedBackpressure holds parsed backpressure settings for an operation.
type ResolvedBackpressure struct {
	LatencyThreshold   time.Duration
	DurationMultiplier float64
	ErrorRateAdd       float64
}

// ResolvedCircuitBreaker holds parsed circuit breaker settings for an operation.
type ResolvedCircuitBreaker struct {
	FailureThreshold int
	Window           time.Duration
	Cooldown         time.Duration
}

// Operation represents a resolved operation with pointers to downstream calls.
type Operation struct {
	Service        *Service
	Name           string
	Ref            string
	Duration       Distribution
	ErrorRate      float64
	Calls          []Call
	CallStyle      string
	Attributes     map[string]AttributeGenerator
	QueueDepth     int
	Backpressure   *ResolvedBackpressure
	CircuitBreaker *ResolvedCircuitBreaker
}

// Call represents a resolved downstream call with optional modifiers.
type Call struct {
	Operation    *Operation
	Probability  float64
	Condition    string
	Count        int
	Timeout      time.Duration
	Retries      int
	RetryBackoff time.Duration
}

// DomainResolver maps a domain identifier to attribute generators.
// Returns nil if the domain is not recognised.
type DomainResolver func(domain string) map[string]AttributeGenerator

// BuildTopology resolves a validated Config into a traversable Topology graph.
// cfg must have passed ValidateConfig first. Duration strings in backpressure
// and circuit breaker config are parsed without error checks here because
// validation has already rejected malformed values.
// An optional DomainResolver enables the domain field on operations.
func BuildTopology(cfg *Config, resolvers ...DomainResolver) (*Topology, error) {
	var resolve DomainResolver
	if len(resolvers) > 0 {
		resolve = resolvers[0]
	}

	topo := &Topology{
		Services: make(map[string]*Service, len(cfg.Services)),
	}

	// First pass: create all services and operations
	for _, svcCfg := range cfg.Services {
		svc := &Service{
			Name:       svcCfg.Name,
			Operations: make(map[string]*Operation, len(svcCfg.Operations)),
			Attributes: svcCfg.Attributes,
		}
		for _, opCfg := range svcCfg.Operations {
			dist, err := ParseDistribution(opCfg.Duration)
			if err != nil {
				return nil, fmt.Errorf("service %q operation %q: %w", svcCfg.Name, opCfg.Name, err)
			}
			var errorRate float64
			if opCfg.ErrorRate != "" {
				errorRate, err = parseErrorRate(opCfg.ErrorRate)
				if err != nil {
					return nil, fmt.Errorf("service %q operation %q: %w", svcCfg.Name, opCfg.Name, err)
				}
			}
			var attrs map[string]AttributeGenerator
			if opCfg.Domain != "" {
				if resolve == nil {
					return nil, fmt.Errorf("service %q operation %q: domain %q specified but no domain resolver configured", svcCfg.Name, opCfg.Name, opCfg.Domain)
				}
				attrs = resolve(opCfg.Domain)
				if attrs == nil {
					return nil, fmt.Errorf("service %q operation %q: unknown domain %q", svcCfg.Name, opCfg.Name, opCfg.Domain)
				}
			}
			if len(opCfg.Attributes) > 0 {
				if attrs == nil {
					attrs = make(map[string]AttributeGenerator, len(opCfg.Attributes))
				}
				for name, acfg := range opCfg.Attributes {
					gen, err := NewAttributeGenerator(acfg)
					if err != nil {
						return nil, fmt.Errorf("service %q operation %q attribute %q: %w", svcCfg.Name, opCfg.Name, name, err)
					}
					attrs[name] = gen
				}
			}
			op := &Operation{
				Service:    svc,
				Name:       opCfg.Name,
				Ref:        svcCfg.Name + "." + opCfg.Name,
				Duration:   dist,
				ErrorRate:  errorRate,
				CallStyle:  opCfg.CallStyle,
				Attributes: attrs,
				QueueDepth: opCfg.QueueDepth,
			}
			if opCfg.Backpressure != nil {
				lt, _ := time.ParseDuration(opCfg.Backpressure.LatencyThreshold)
				var errAdd float64
				if opCfg.Backpressure.ErrorRateAdd != "" {
					errAdd, _ = parseErrorRate(opCfg.Backpressure.ErrorRateAdd)
				}
				op.Backpressure = &ResolvedBackpressure{
					LatencyThreshold:   lt,
					DurationMultiplier: opCfg.Backpressure.DurationMultiplier,
					ErrorRateAdd:       errAdd,
				}
			}
			if opCfg.CircuitBreaker != nil {
				w, _ := time.ParseDuration(opCfg.CircuitBreaker.Window)
				cd, _ := time.ParseDuration(opCfg.CircuitBreaker.Cooldown)
				op.CircuitBreaker = &ResolvedCircuitBreaker{
					FailureThreshold: opCfg.CircuitBreaker.FailureThreshold,
					Window:           w,
					Cooldown:         cd,
				}
			}
			svc.Operations[opCfg.Name] = op
		}
		topo.Services[svcCfg.Name] = svc
	}

	// Second pass: resolve call references
	for _, svcCfg := range cfg.Services {
		for _, opCfg := range svcCfg.Operations {
			op := topo.Services[svcCfg.Name].Operations[opCfg.Name]
			for _, callCfg := range opCfg.Calls {
				targetSvc, targetOp, err := resolveRef(topo, callCfg.Target)
				if err != nil {
					return nil, fmt.Errorf("service %q operation %q: %w", svcCfg.Name, opCfg.Name, err)
				}
				_ = targetSvc
				call := Call{
					Operation:   targetOp,
					Probability: callCfg.Probability,
					Condition:   callCfg.Condition,
					Count:       callCfg.Count,
					Retries:     callCfg.Retries,
				}
				if callCfg.Timeout != "" {
					call.Timeout, err = time.ParseDuration(callCfg.Timeout)
					if err != nil {
						return nil, fmt.Errorf("service %q operation %q: call %q: invalid timeout: %w", svcCfg.Name, opCfg.Name, callCfg.Target, err)
					}
				}
				if callCfg.RetryBackoff != "" {
					call.RetryBackoff, err = time.ParseDuration(callCfg.RetryBackoff)
					if err != nil {
						return nil, fmt.Errorf("service %q operation %q: call %q: invalid retry_backoff: %w", svcCfg.Name, opCfg.Name, callCfg.Target, err)
					}
				}
				op.Calls = append(op.Calls, call)
			}
		}
	}

	// Detect cycles
	if err := detectCycles(topo); err != nil {
		return nil, err
	}

	// Detect root operations (not called by any other operation)
	topo.Roots = findRoots(topo)

	return topo, nil
}

// resolveRef resolves a "service.operation" reference string to pointers.
func resolveRef(topo *Topology, ref string) (*Service, *Operation, error) {
	// Split on first dot only to allow dots in operation names
	svcName, opName, ok := strings.Cut(ref, ".")
	if !ok {
		return nil, nil, fmt.Errorf("reference %q must be in service.operation format", ref)
	}

	svc, ok := topo.Services[svcName]
	if !ok {
		return nil, nil, fmt.Errorf("reference %q: service %q not found", ref, svcName)
	}
	op, ok := svc.Operations[opName]
	if !ok {
		return nil, nil, fmt.Errorf("reference %q: operation %q not found in service %q", ref, opName, svcName)
	}
	return svc, op, nil
}

// findRoots returns operations that are not called by any other operation.
func findRoots(topo *Topology) []*Operation {
	called := make(map[*Operation]bool)
	for _, svc := range topo.Services {
		for _, op := range svc.Operations {
			for _, call := range op.Calls {
				called[call.Operation] = true
			}
		}
	}

	var roots []*Operation
	for _, svc := range topo.Services {
		for _, op := range svc.Operations {
			if !called[op] {
				roots = append(roots, op)
			}
		}
	}
	slices.SortFunc(roots, func(a, b *Operation) int {
		if c := cmp.Compare(a.Service.Name, b.Service.Name); c != 0 {
			return c
		}
		return cmp.Compare(a.Name, b.Name)
	})
	return roots
}

// detectCycles performs DFS cycle detection across all operations.
func detectCycles(topo *Topology) error {
	const (
		unvisited = iota
		visiting
		visited
	)
	state := make(map[*Operation]int)

	var visit func(op *Operation) error
	visit = func(op *Operation) error {
		switch state[op] {
		case visiting:
			return fmt.Errorf("cycle detected involving %s", op.Ref)
		case visited:
			return nil
		}
		state[op] = visiting
		for _, call := range op.Calls {
			if err := visit(call.Operation); err != nil {
				return err
			}
		}
		state[op] = visited
		return nil
	}

	for _, svc := range topo.Services {
		for _, op := range svc.Operations {
			if state[op] == unvisited {
				if err := visit(op); err != nil {
					return err
				}
			}
		}
	}
	return nil
}
