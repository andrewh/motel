// Topology graph construction from parsed YAML configuration
// Resolves string references to pointers, detects root operations and cycles
package synth

import (
	"fmt"
	"strings"
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

// Operation represents a resolved operation with pointers to downstream calls.
type Operation struct {
	Service    *Service
	Name       string
	Duration   Distribution
	ErrorRate  float64
	Calls      []*Operation
	CallStyle  string
	Attributes map[string]AttributeGenerator
}

// BuildTopology resolves a validated Config into a traversable Topology graph.
func BuildTopology(cfg *Config) (*Topology, error) {
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
			if len(opCfg.Attributes) > 0 {
				attrs = make(map[string]AttributeGenerator, len(opCfg.Attributes))
				for name, acfg := range opCfg.Attributes {
					gen, err := NewAttributeGenerator(acfg)
					if err != nil {
						return nil, fmt.Errorf("service %q operation %q attribute %q: %w", svcCfg.Name, opCfg.Name, name, err)
					}
					attrs[name] = gen
				}
			}
			svc.Operations[opCfg.Name] = &Operation{
				Service:    svc,
				Name:       opCfg.Name,
				Duration:   dist,
				ErrorRate:  errorRate,
				CallStyle:  opCfg.CallStyle,
				Attributes: attrs,
			}
		}
		topo.Services[svcCfg.Name] = svc
	}

	// Second pass: resolve call references
	for _, svcCfg := range cfg.Services {
		for _, opCfg := range svcCfg.Operations {
			op := topo.Services[svcCfg.Name].Operations[opCfg.Name]
			for _, callRef := range opCfg.Calls {
				targetSvc, targetOp, err := resolveRef(topo, callRef)
				if err != nil {
					return nil, fmt.Errorf("service %q operation %q: %w", svcCfg.Name, opCfg.Name, err)
				}
				_ = targetSvc
				op.Calls = append(op.Calls, targetOp)
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
			for _, target := range op.Calls {
				called[target] = true
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
			return fmt.Errorf("cycle detected involving %s.%s", op.Service.Name, op.Name)
		case visited:
			return nil
		}
		state[op] = visiting
		for _, child := range op.Calls {
			if err := visit(child); err != nil {
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
