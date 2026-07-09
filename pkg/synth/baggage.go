// OTel baggage propagation for synthetic topologies.
//
// Baggage is a set of key/value pairs that travels with the trace context across
// service boundaries (parent -> children), distinct from span attributes. Services
// and operations declare baggage in the topology DSL; the engine sets it on the
// context when a span starts, propagates it to descendant spans, and can surface
// the inherited set as span attributes (mirroring collector processors that copy
// baggage onto spans) so it is observable by a downstream OTLP consumer.
package synth

import (
	"sort"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/baggage"
)

// baggageAttributePrefix is prepended to baggage keys when an operation surfaces
// baggage as span attributes (e.g. baggage.user.id), mirroring the common
// real-world collector processor behaviour.
const baggageAttributePrefix = "baggage."

// mergeDeclaredBaggage returns the baggage an operation declares: service-level
// entries overlaid with operation-level entries, with the operation winning on
// key conflicts. Returns nil when neither level declares baggage.
func mergeDeclaredBaggage(service, operation map[string]string) map[string]string {
	if len(service) == 0 && len(operation) == 0 {
		return nil
	}
	merged := make(map[string]string, len(service)+len(operation))
	for k, v := range service {
		merged[k] = v
	}
	for k, v := range operation {
		merged[k] = v
	}
	return merged
}

// overlayBaggageMap overlays an operation's declared baggage onto the baggage
// inherited from its parent, returning the combined set visible while the span is
// active. Declared entries win on key conflicts. Returns nil when the result is
// empty.
func overlayBaggageMap(inherited, declared map[string]string) map[string]string {
	if len(inherited) == 0 && len(declared) == 0 {
		return nil
	}
	merged := make(map[string]string, len(inherited)+len(declared))
	for k, v := range inherited {
		merged[k] = v
	}
	for k, v := range declared {
		merged[k] = v
	}
	return merged
}

// baggageAttributesFromMap renders a baggage set as span attributes keyed by
// baggageAttributePrefix + baggage key, sorted for deterministic output.
func baggageAttributesFromMap(m map[string]string) []attribute.KeyValue {
	if len(m) == 0 {
		return nil
	}
	attrs := make([]attribute.KeyValue, 0, len(m))
	for _, k := range sortedKeys(m) {
		attrs = append(attrs, attribute.String(baggageAttributePrefix+k, m[k]))
	}
	return attrs
}

// buildBaggage constructs an OTel baggage.Baggage from a map so it can be placed
// on the context and propagated. Entries that fail member construction are
// skipped; config validation rejects malformed baggage before this runs.
func buildBaggage(m map[string]string) baggage.Baggage {
	members := make([]baggage.Member, 0, len(m))
	for _, k := range sortedKeys(m) {
		mem, err := baggage.NewMemberRaw(k, m[k])
		if err != nil {
			continue
		}
		members = append(members, mem)
	}
	bag, _ := baggage.New(members...)
	return bag
}

// baggageToMap converts an OTel baggage set into a plain map. The plan phase has
// no context to carry OTel baggage, so it propagates baggage as a map instead.
func baggageToMap(bag baggage.Baggage) map[string]string {
	members := bag.Members()
	if len(members) == 0 {
		return nil
	}
	m := make(map[string]string, len(members))
	for _, mem := range members {
		m[mem.Key()] = mem.Value()
	}
	return m
}

// sortedKeys returns the keys of m in ascending order.
func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
