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

func TestParseSpanKey(t *testing.T) {
	tid, sid := traceID(7), spanID(9)
	gotTid, gotSid, err := ParseSpanKey(SpanKey(tid, sid))
	if err != nil {
		t.Fatalf("ParseSpanKey round-trip failed: %v", err)
	}
	if string(gotTid) != string(tid) || string(gotSid) != string(sid) {
		t.Fatal("ParseSpanKey round-trip changed the IDs")
	}
	short := SpanKey(spanID(1), spanID(1)) // 8-byte trace ID
	long := SpanKey(tid, tid)              // 16-byte span ID
	for _, bad := range []string{"nocolon", "zz:00", "00:zz", ":", SpanKey(nil, sid), SpanKey(tid, nil), short, long} {
		if _, _, err := ParseSpanKey(bad); err == nil {
			t.Fatalf("ParseSpanKey accepted malformed key %q", bad)
		}
	}
}

func TestCheckFilterCorrectness(t *testing.T) {
	tid := traceID(1)
	keep := map[string]struct{}{SpanKey(tid, spanID(1)): {}}
	drop := map[string]struct{}{SpanKey(tid, spanID(2)): {}}

	if err := CheckFilterCorrectness(keep, drop, []*tracepb.Span{span(tid, 1, 0)}); err != nil {
		t.Fatalf("CheckFilterCorrectness rejected a correct partition: %v", err)
	}
	if err := CheckFilterCorrectness(keep, drop, []*tracepb.Span{span(tid, 1, 0), span(tid, 2, 1)}); err == nil {
		t.Fatal("CheckFilterCorrectness accepted a span that matches the drop predicate")
	}
	if err := CheckFilterCorrectness(keep, drop, nil); err == nil {
		t.Fatal("CheckFilterCorrectness accepted the drop of a span it should keep")
	}
	if err := CheckFilterCorrectness(keep, drop, []*tracepb.Span{span(tid, 1, 0), span(tid, 3, 0)}); err == nil {
		t.Fatal("CheckFilterCorrectness accepted a fabricated span")
	}
}

func TestCheckRoutingConsistency(t *testing.T) {
	tidA, tidB := traceID(1), traceID(2)

	whole := map[string][]*tracepb.Span{
		"primary":   {span(tidA, 1, 0), span(tidA, 2, 1)},
		"secondary": {span(tidB, 1, 0)},
	}
	if err := CheckRoutingConsistency(whole); err != nil {
		t.Fatalf("CheckRoutingConsistency rejected whole-trace routing: %v", err)
	}

	split := map[string][]*tracepb.Span{
		"primary":   {span(tidA, 1, 0)},
		"secondary": {span(tidA, 2, 1)},
	}
	if err := CheckRoutingConsistency(split); err == nil {
		t.Fatal("CheckRoutingConsistency accepted a trace split across backends")
	}
}

func TestCheckRouteCompleteness(t *testing.T) {
	tid := traceID(1)
	sent := NewSent()
	sent.Add(tid, spanID(1))
	sent.Add(tid, spanID(2))

	complete := map[string][]*tracepb.Span{
		"primary":   {span(tid, 1, 0)},
		"secondary": {span(tid, 2, 1)},
	}
	if err := CheckRouteCompleteness(sent, complete); err != nil {
		t.Fatalf("CheckRouteCompleteness rejected full coverage: %v", err)
	}

	// Fan-out duplication is a policy choice, not a loss.
	duplicated := map[string][]*tracepb.Span{
		"primary":   {span(tid, 1, 0), span(tid, 2, 1)},
		"secondary": {span(tid, 2, 1)},
	}
	if err := CheckRouteCompleteness(sent, duplicated); err != nil {
		t.Fatalf("CheckRouteCompleteness rejected fan-out duplication: %v", err)
	}

	fallthru := map[string][]*tracepb.Span{
		"primary": {span(tid, 1, 0)},
	}
	if err := CheckRouteCompleteness(sent, fallthru); err == nil {
		t.Fatal("CheckRouteCompleteness accepted a span that fell through the rules")
	}

	fabricated := map[string][]*tracepb.Span{
		"primary":   {span(tid, 1, 0), span(tid, 2, 1)},
		"secondary": {span(tid, 3, 0)},
	}
	if err := CheckRouteCompleteness(sent, fabricated); err == nil {
		t.Fatal("CheckRouteCompleteness accepted a fabricated span")
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
