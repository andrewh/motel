package pipelinetest

import (
	"testing"

	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
)

// These tests feed hand-crafted violations to each invariant check and confirm
// it trips, then confirm clean data passes. A correct collector never
// exercises the failure paths, so this is the only always-on coverage the
// checks get; it needs no collector binary.

// traceID builds a 16-byte trace ID whose last byte is b.
func traceID(b byte) []byte {
	id := make([]byte, 16)
	id[15] = b
	return id
}

// spanID builds an 8-byte span ID whose last byte is b.
func spanID(b byte) []byte {
	id := make([]byte, 8)
	id[7] = b
	return id
}

// span builds an OTLP span; a parent of 0 means a root span.
func span(tid []byte, sid, parent byte) *tracepb.Span {
	s := &tracepb.Span{TraceId: tid, SpanId: spanID(sid)}
	if parent != 0 {
		s.ParentSpanId = spanID(parent)
	}
	return s
}

func TestCheckParentsKept(t *testing.T) {
	tid := traceID(1)
	root := span(tid, 1, 0)
	child := span(tid, 2, 1)
	grandchild := span(tid, 3, 2)

	if err := CheckParentsKept([]*tracepb.Span{root, grandchild}); err == nil {
		t.Fatal("CheckParentsKept accepted a span whose parent was dropped")
	}
	if err := CheckParentsKept([]*tracepb.Span{root, child, grandchild}); err != nil {
		t.Fatalf("CheckParentsKept rejected complete lineage: %v", err)
	}
}

func TestCheckWholeTraces(t *testing.T) {
	tid := traceID(1)
	root := span(tid, 1, 0)
	child := span(tid, 2, 1)

	sent := NewSent()
	sent.Add(tid, spanID(1))
	sent.Add(tid, spanID(2))

	if err := CheckWholeTraces(sent, []*tracepb.Span{root}); err == nil {
		t.Fatal("CheckWholeTraces accepted a partially kept trace")
	}
	if err := CheckWholeTraces(sent, []*tracepb.Span{root, child}); err != nil {
		t.Fatalf("CheckWholeTraces rejected a whole trace: %v", err)
	}
	if err := CheckWholeTraces(sent, nil); err != nil {
		t.Fatalf("CheckWholeTraces rejected a fully dropped trace: %v", err)
	}
}

func TestCheckNoFabrication(t *testing.T) {
	tid := traceID(1)
	sent := NewSent()
	sent.Add(tid, spanID(1))

	if err := CheckNoFabrication(sent, []*tracepb.Span{span(tid, 1, 0)}); err != nil {
		t.Fatalf("CheckNoFabrication rejected a sent span: %v", err)
	}
	if err := CheckNoFabrication(sent, []*tracepb.Span{span(tid, 2, 1)}); err == nil {
		t.Fatal("CheckNoFabrication accepted a span that was never sent")
	}
}

func TestCheckConservation(t *testing.T) {
	tid := traceID(1)
	root := span(tid, 1, 0)
	child := span(tid, 2, 1)

	sent := NewSent()
	sent.Add(tid, spanID(1))
	sent.Add(tid, spanID(2))

	if err := CheckConservation(sent, []*tracepb.Span{root, child}); err != nil {
		t.Fatalf("CheckConservation rejected an exact round-trip: %v", err)
	}
	if err := CheckConservation(sent, []*tracepb.Span{root}); err == nil {
		t.Fatal("CheckConservation accepted a dropped span")
	}
	if err := CheckConservation(sent, []*tracepb.Span{root, child, span(tid, 3, 1)}); err == nil {
		t.Fatal("CheckConservation accepted a fabricated span")
	}
}
