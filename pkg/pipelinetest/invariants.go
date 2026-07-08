package pipelinetest

import (
	"encoding/hex"
	"fmt"

	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
)

// SpanKey is the hex-encoded (trace ID, span ID) identity of a span. It is the
// join key between the spans a test sent into a pipeline and the spans the
// Sink received, so invariant checks can compare the two sets.
func SpanKey(traceID, spanID []byte) string {
	return hex.EncodeToString(traceID) + ":" + hex.EncodeToString(spanID)
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
