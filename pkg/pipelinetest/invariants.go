package pipelinetest

import (
	"encoding/hex"
	"fmt"
	"strings"

	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
)

// SpanKey is the hex-encoded (trace ID, span ID) identity of a span. It is the
// join key between the spans a test sent into a pipeline and the spans the
// Sink received, so invariant checks can compare the two sets.
func SpanKey(traceID, spanID []byte) string {
	return hex.EncodeToString(traceID) + ":" + hex.EncodeToString(spanID)
}

// ParseSpanKey decodes a SpanKey back into its raw trace and span ID bytes,
// so identities that were reduced to keys can still be recorded via Sent.Add.
// It enforces the OTLP ID sizes — a 16-byte trace ID and an 8-byte span ID —
// so a malformed key fails here rather than surfacing later as an identity
// that matches nothing.
func ParseSpanKey(key string) (traceID, spanID []byte, err error) {
	tid, sid, ok := strings.Cut(key, ":")
	if !ok {
		return nil, nil, fmt.Errorf("span key %q has no separator", key)
	}
	if traceID, err = hex.DecodeString(tid); err != nil {
		return nil, nil, fmt.Errorf("span key %q: %w", key, err)
	}
	if len(traceID) != 16 {
		return nil, nil, fmt.Errorf("span key %q: trace ID is %d bytes, want 16", key, len(traceID))
	}
	if spanID, err = hex.DecodeString(sid); err != nil {
		return nil, nil, fmt.Errorf("span key %q: %w", key, err)
	}
	if len(spanID) != 8 {
		return nil, nil, fmt.Errorf("span key %q: span ID is %d bytes, want 8", key, len(spanID))
	}
	return traceID, spanID, nil
}

// Sent records the span identities a test pushed into a pipeline, grouped so
// the invariant checks can compare them against what the Sink received. The
// harness is agnostic to how a caller generates spans: translate each sent
// span into its raw trace and span ID bytes and call Add.
type Sent struct {
	keys    map[string]struct{}
	byTrace map[string][]string
}

// NewSent returns an empty Sent ready for Add.
func NewSent() *Sent {
	return &Sent{
		keys:    make(map[string]struct{}),
		byTrace: make(map[string][]string),
	}
}

// Add records one sent span. Repeated identities are recorded once.
func (s *Sent) Add(traceID, spanID []byte) {
	key := SpanKey(traceID, spanID)
	if _, ok := s.keys[key]; ok {
		return
	}
	s.keys[key] = struct{}{}
	tid := hex.EncodeToString(traceID)
	s.byTrace[tid] = append(s.byTrace[tid], key)
}

// Has reports whether key was recorded by Add.
func (s *Sent) Has(key string) bool {
	_, ok := s.keys[key]
	return ok
}

// Len returns the number of distinct spans recorded.
func (s *Sent) Len() int { return len(s.keys) }

// ReceivedKeys reduces captured spans to their identity set.
func ReceivedKeys(received []*tracepb.Span) map[string]struct{} {
	keys := make(map[string]struct{}, len(received))
	for _, s := range received {
		keys[SpanKey(s.GetTraceId(), s.GetSpanId())] = struct{}{}
	}
	return keys
}

// CheckNoFabrication reports a received span whose identity was never sent: a
// sampler may drop spans but must never invent new ones. It is deliberately a
// subset check, not an exactly-once check — OTLP delivery is at-least-once, so
// a correct pipeline may redeliver a span on retry, and that is not a
// fabrication.
func CheckNoFabrication(sent *Sent, received []*tracepb.Span) error {
	for _, s := range received {
		key := SpanKey(s.GetTraceId(), s.GetSpanId())
		if !sent.Has(key) {
			return fmt.Errorf("received span %s that was never sent", key)
		}
	}
	return nil
}

// CheckParentsKept reports an orphaned span: a received span whose parent span
// was dropped. Root spans (a zero parent ID) are exempt. This is the
// parent-child preservation invariant.
func CheckParentsKept(received []*tracepb.Span) error {
	kept := ReceivedKeys(received)
	for _, s := range received {
		parent := s.GetParentSpanId()
		if !validParentID(parent) {
			continue
		}
		parentKey := SpanKey(s.GetTraceId(), parent)
		if _, ok := kept[parentKey]; !ok {
			return fmt.Errorf("orphaned span: %s was kept but its parent %s was dropped",
				SpanKey(s.GetTraceId(), s.GetSpanId()), parentKey)
		}
	}
	return nil
}

// CheckWholeTraces reports a partially sampled trace: a trace with at least one
// received span but some sent span missing. This is the trace completeness
// invariant. Traces the pipeline dropped entirely are exempt.
func CheckWholeTraces(sent *Sent, received []*tracepb.Span) error {
	kept := make(map[string]struct{}, len(received))
	keptTraces := make(map[string]struct{})
	for _, s := range received {
		kept[SpanKey(s.GetTraceId(), s.GetSpanId())] = struct{}{}
		keptTraces[hex.EncodeToString(s.GetTraceId())] = struct{}{}
	}
	for tid := range keptTraces {
		for _, key := range sent.byTrace[tid] {
			if _, ok := kept[key]; !ok {
				return fmt.Errorf("partially sampled trace %s: span %s was dropped while other spans of the trace were kept", tid, key)
			}
		}
	}
	return nil
}

// CheckConservation reports any discrepancy between the identities sent and the
// identities received: a pass-through pipeline must deliver every sent span and
// fabricate none. It compares identity sets, not raw counts, so redelivery of a
// span (OTLP is at-least-once) is not a violation; only a missing or fabricated
// identity is. This is the conservation invariant.
func CheckConservation(sent *Sent, received []*tracepb.Span) error {
	got := ReceivedKeys(received)
	if len(got) != sent.Len() {
		return fmt.Errorf("span count mismatch: sent %d, received %d", sent.Len(), len(got))
	}
	for key := range sent.keys {
		if _, ok := got[key]; !ok {
			return fmt.Errorf("span %s sent but not received", key)
		}
	}
	return nil
}

// CheckFilterCorrectness verifies a filter's keep/drop decisions against the
// caller's own partition of the sent spans: keep holds the identities the
// filter must pass through, drop the identities it must remove. It reports a
// false negative (a received span in drop), a false positive (a keep span
// missing from received), or a fabrication (a received span in neither set).
// This is the filter correctness invariant.
//
// The caller computes the partition by applying the filter's predicate to the
// spans it sent — it knows every span's attributes client-side — so the check
// stays a pure set comparison and works for any predicate.
func CheckFilterCorrectness(keep, drop map[string]struct{}, received []*tracepb.Span) error {
	got := ReceivedKeys(received)
	for key := range got {
		if _, ok := drop[key]; ok {
			return fmt.Errorf("span %s was kept but matches the drop predicate", key)
		}
		if _, ok := keep[key]; !ok {
			return fmt.Errorf("received span %s that was never sent", key)
		}
	}
	for key := range keep {
		if _, ok := got[key]; !ok {
			return fmt.Errorf("span %s was dropped but does not match the drop predicate", key)
		}
	}
	return nil
}

// CheckRoutingConsistency reports a trace split across backends: a trace ID
// with spans at more than one destination. backends maps a backend name (used
// only in the error message) to the spans that backend received. This is the
// routing consistency invariant — attribute-based routing must not tear traces
// apart, or each backend sees a fragment no tail-based tool can reassemble.
func CheckRoutingConsistency(backends map[string][]*tracepb.Span) error {
	seen := make(map[string]string) // trace ID -> backend that received it
	for name, spans := range backends {
		for _, s := range spans {
			tid := hex.EncodeToString(s.GetTraceId())
			if prev, ok := seen[tid]; ok && prev != name {
				return fmt.Errorf("trace %s split across backends %q and %q", tid, prev, name)
			}
			seen[tid] = name
		}
	}
	return nil
}

// CheckRouteCompleteness reports a span that fell through the routing rules:
// a sent span that no backend received, silently discarded by a rule set that
// does not cover it. It also reports a fabricated span (received by some
// backend but never sent). This is the rule completeness invariant. A span
// delivered to several backends is not a violation — fan-out duplication is a
// routing policy choice, not a loss.
func CheckRouteCompleteness(sent *Sent, backends map[string][]*tracepb.Span) error {
	routed := make(map[string]struct{})
	for name, spans := range backends {
		for _, s := range spans {
			key := SpanKey(s.GetTraceId(), s.GetSpanId())
			if !sent.Has(key) {
				return fmt.Errorf("backend %q received span %s that was never sent", name, key)
			}
			routed[key] = struct{}{}
		}
	}
	for key := range sent.keys {
		if _, ok := routed[key]; !ok {
			return fmt.Errorf("span %s fell through the routing rules: sent but received by no backend", key)
		}
	}
	return nil
}

// validParentID reports whether a parent span ID is present and non-zero. OTLP
// encodes a root span's missing parent as an empty or all-zero span ID.
func validParentID(id []byte) bool {
	for _, b := range id {
		if b != 0 {
			return true
		}
	}
	return false
}
