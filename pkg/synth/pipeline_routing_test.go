package synth

import (
	"errors"
	"testing"

	"github.com/andrewh/motel/pkg/pipelinetest"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"pgregory.net/rapid"
)

// Routing invariants (issue 75). A routing stage directs telemetry to one of
// several backends based on attributes; these tests assert the two properties
// a routing configuration must not violate, using the checks in
// pkg/pipelinetest:
//
//   - Routing consistency: all spans of a trace arrive at the same backend —
//     CheckRoutingConsistency.
//   - Rule completeness: every sent span is delivered to some backend, none
//     fall through the rules into silent discard — CheckRouteCompleteness.
//
// The correct configuration routes on a resource attribute ("tenant"), which
// is constant across a trace, so traces move whole. The violation tests then
// show each invariant catching a realistic misconfiguration: routing on a
// per-span attribute (splits traces) and a rule table with no default
// (drops unmatched spans).
//
// Like the other pipeline tests, they drive a real collector subprocess and
// skip when no binary is available or it lacks the routing connector / filter
// processor.

// tenantRoutedPipeline routes resource attribute tenant=beta to the second
// sink and everything else to the first, via the routing connector.
const tenantRoutedPipeline = `receivers:
  otlp:
    protocols:
      http:
        endpoint: 127.0.0.1:{{.OTLPHTTPPort}}
connectors:
  routing:
    default_pipelines: [traces/primary]
    table:
      - context: resource
        condition: attributes["tenant"] == "beta"
        pipelines: [traces/secondary]
exporters:
  otlphttp/primary:
    endpoint: {{index .SinkURLs 0}}
    compression: none
  otlphttp/secondary:
    endpoint: {{index .SinkURLs 1}}
    compression: none
extensions:
  health_check:
    endpoint: 127.0.0.1:{{.HealthPort}}
service:
  extensions: [health_check]
  pipelines:
    traces/in:
      receivers: [otlp]
      exporters: [routing]
    traces/primary:
      receivers: [routing]
      exporters: [otlphttp/primary]
    traces/secondary:
      receivers: [routing]
      exporters: [otlphttp/secondary]
  telemetry:
    metrics:
      level: none
    logs:
      level: warn
`

// noDefaultRoutedPipeline is tenantRoutedPipeline without default_pipelines:
// spans that match no rule are silently discarded. This is the rule
// completeness violation.
const noDefaultRoutedPipeline = `receivers:
  otlp:
    protocols:
      http:
        endpoint: 127.0.0.1:{{.OTLPHTTPPort}}
connectors:
  routing:
    table:
      - context: resource
        condition: attributes["tenant"] == "beta"
        pipelines: [traces/secondary]
exporters:
  otlphttp/primary:
    endpoint: {{index .SinkURLs 0}}
    compression: none
  otlphttp/secondary:
    endpoint: {{index .SinkURLs 1}}
    compression: none
extensions:
  health_check:
    endpoint: 127.0.0.1:{{.HealthPort}}
service:
  extensions: [health_check]
  pipelines:
    traces/in:
      receivers: [otlp]
      exporters: [routing]
    traces/primary:
      receivers: [routing]
      exporters: [otlphttp/primary]
    traces/secondary:
      receivers: [routing]
      exporters: [otlphttp/secondary]
  telemetry:
    metrics:
      level: none
    logs:
      level: warn
`

// serviceSplitPipeline "routes" by a per-span property — the DIY routing
// pattern of parallel pipelines with complementary filters. gateway spans go
// to the first sink, everything else to the second, so every multi-service
// trace is torn across backends. This is the routing consistency violation.
const serviceSplitPipeline = `receivers:
  otlp:
    protocols:
      http:
        endpoint: 127.0.0.1:{{.OTLPHTTPPort}}
processors:
  filter/only-gateway:
    error_mode: ignore
    traces:
      span:
        - instrumentation_scope.name != "gateway"
  filter/all-but-gateway:
    error_mode: ignore
    traces:
      span:
        - instrumentation_scope.name == "gateway"
exporters:
  otlphttp/primary:
    endpoint: {{index .SinkURLs 0}}
    compression: none
  otlphttp/secondary:
    endpoint: {{index .SinkURLs 1}}
    compression: none
extensions:
  health_check:
    endpoint: 127.0.0.1:{{.HealthPort}}
service:
  extensions: [health_check]
  pipelines:
    traces/gateway:
      receivers: [otlp]
      processors: [filter/only-gateway]
      exporters: [otlphttp/primary]
    traces/rest:
      receivers: [otlp]
      processors: [filter/all-but-gateway]
      exporters: [otlphttp/secondary]
  telemetry:
    metrics:
      level: none
    logs:
      level: warn
`

// TestRouting_WholeTracesPerBackend drives two tenants' traffic through a
// resource-attribute router and asserts the invariants plus exact
// delivery: traces arrive whole, nothing falls through, and each backend
// receives exactly its tenant's spans.
func TestRouting_WholeTracesPerBackend(t *testing.T) {
	requireRouting(t)
	sinks, collector := startMultiPipeline(t, tenantRoutedPipeline, 2)

	topo := loadTopology(t, passthroughTopology)
	alpha := stubKeys(generateTenant(t, topo, collector.OTLPEndpoint, "alpha", 15, 3))
	beta := stubKeys(generateTenant(t, topo, collector.OTLPEndpoint, "beta", 15, 4))

	sent := pipelinetest.NewSent()
	backends := settledBackends(sinks, len(alpha), len(beta))
	for key := range alpha {
		addKey(sent, key)
	}
	for key := range beta {
		addKey(sent, key)
	}

	if err := pipelinetest.CheckRoutingConsistency(backends); err != nil {
		t.Fatal(err)
	}
	if err := pipelinetest.CheckRouteCompleteness(sent, backends); err != nil {
		t.Fatal(err)
	}
	// Exact delivery per backend: routing to a backend is a filter from that
	// backend's point of view — keep my tenant's spans, drop the other's.
	if err := pipelinetest.CheckFilterCorrectness(alpha, beta, backends["primary"]); err != nil {
		t.Fatalf("primary backend: %v", err)
	}
	if err := pipelinetest.CheckFilterCorrectness(beta, alpha, backends["secondary"]); err != nil {
		t.Fatalf("secondary backend: %v", err)
	}
}

// TestRouting_ConsistencyProperty asserts the routing invariants for any
// generated topology: whatever the trace shape, resource-attribute routing
// must deliver every trace whole to exactly one backend. Each draw sends one
// tenant's traffic; sent identities accumulate across draws as in the other
// pipeline properties.
func TestRouting_ConsistencyProperty(t *testing.T) {
	requireRouting(t)
	sinks, collector := startMultiPipeline(t, tenantRoutedPipeline, 2)

	sent := pipelinetest.NewSent()
	expected := map[string]map[string]struct{}{
		"alpha": make(map[string]struct{}),
		"beta":  make(map[string]struct{}),
	}

	rapid.Check(t, func(t *rapid.T) {
		cfg := genSimpleConfig(t)
		topo, err := BuildTopology(cfg)
		if err != nil {
			t.Fatalf("BuildTopology: %v", err)
		}
		if len(topo.Roots) == 0 {
			t.Skip("no root operations")
		}

		tenant := rapid.SampledFrom([]string{"alpha", "beta"}).Draw(t, "tenant")
		seed := rapid.Uint64().Draw(t, "seed")
		keys := stubKeys(generateTenant(t, topo, collector.OTLPEndpoint, tenant, 5, seed))
		if len(keys) == 0 {
			t.Skip("no spans generated")
		}
		merge(expected[tenant], keys)
		for key := range keys {
			addKey(sent, key)
		}
		backends := settledBackends(sinks, len(expected["alpha"]), len(expected["beta"]))

		if err := pipelinetest.CheckRoutingConsistency(backends); err != nil {
			t.Fatal(err)
		}
		if err := pipelinetest.CheckRouteCompleteness(sent, backends); err != nil {
			t.Fatal(err)
		}
		if err := pipelinetest.CheckFilterCorrectness(expected["alpha"], expected["beta"], backends["primary"]); err != nil {
			t.Fatalf("primary backend: %v", err)
		}
		if err := pipelinetest.CheckFilterCorrectness(expected["beta"], expected["alpha"], backends["secondary"]); err != nil {
			t.Fatalf("secondary backend: %v", err)
		}
	})
}

// TestRouting_SplitByServiceDetected demonstrates the routing consistency
// invariant catching the misconfiguration it exists for: routing on a
// per-span property tears every multi-service trace across backends, even
// though no span is lost.
func TestRouting_SplitByServiceDetected(t *testing.T) {
	requireFilter(t)
	sinks, collector := startMultiPipeline(t, serviceSplitPipeline, 2)

	topo := loadTopology(t, passthroughTopology)
	stubs := generateTenant(t, topo, collector.OTLPEndpoint, "alpha", 15, 3)
	gateway, rest := partitionStubs(stubs, func(s tracetest.SpanStub) bool {
		return s.InstrumentationScope.Name != "gateway"
	})

	sent := pipelinetest.NewSent()
	for key := range gateway {
		addKey(sent, key)
	}
	for key := range rest {
		addKey(sent, key)
	}
	backends := settledBackends(sinks, len(gateway), len(rest))

	if err := pipelinetest.CheckRouteCompleteness(sent, backends); err != nil {
		t.Fatalf("per-span routing lost spans, expected only a split: %v", err)
	}
	if err := pipelinetest.CheckRoutingConsistency(backends); err == nil {
		t.Fatal("expected per-span routing to split traces across backends, but every trace arrived whole")
	}
}

// TestRouting_FallthroughDetected demonstrates the rule completeness
// invariant catching a rule table with no default: traffic matching no rule
// is silently discarded, while matched traffic still arrives whole.
func TestRouting_FallthroughDetected(t *testing.T) {
	requireRouting(t)
	sinks, collector := startMultiPipeline(t, noDefaultRoutedPipeline, 2)

	topo := loadTopology(t, passthroughTopology)
	alpha := stubKeys(generateTenant(t, topo, collector.OTLPEndpoint, "alpha", 10, 3))
	beta := stubKeys(generateTenant(t, topo, collector.OTLPEndpoint, "beta", 10, 4))

	sent := pipelinetest.NewSent()
	for key := range alpha {
		addKey(sent, key)
	}
	for key := range beta {
		addKey(sent, key)
	}
	backends := settledBackends(sinks, 0, len(beta))

	if err := pipelinetest.CheckRoutingConsistency(backends); err != nil {
		t.Fatal(err)
	}
	if err := pipelinetest.CheckFilterCorrectness(beta, alpha, backends["secondary"]); err != nil {
		t.Fatalf("secondary backend: %v", err)
	}
	if err := pipelinetest.CheckRouteCompleteness(sent, backends); err == nil {
		t.Fatal("expected unmatched tenant's spans to fall through the rules, but every span was delivered")
	}
}

// requireRouting skips the test when the collector build lacks the routing
// connector.
func requireRouting(t *testing.T) {
	t.Helper()
	if !pipelinetest.SupportsComponent("routing") {
		t.Skip("no collector with the routing connector (set MOTEL_COLLECTOR_BIN to a build that includes it)")
	}
}

// startMultiPipeline starts n sinks and a collector fanning out to them,
// registering cleanup. It skips the test when no collector binary is
// available.
func startMultiPipeline(t testing.TB, config string, n int) ([]*pipelinetest.Sink, *pipelinetest.Collector) {
	t.Helper()
	if _, ok := pipelinetest.CollectorBinary(); !ok {
		t.Skipf("no collector binary (set %s or install otelcol)", pipelinetest.BinaryEnv)
	}

	sinks := make([]*pipelinetest.Sink, n)
	for i := range sinks {
		sinks[i] = pipelinetest.NewSink()
		t.Cleanup(sinks[i].Close)
	}

	collector, err := pipelinetest.StartMulti(config, sinks...)
	if errors.Is(err, pipelinetest.ErrNoCollector) {
		t.Skip("collector binary disappeared")
	}
	if err != nil {
		t.Fatalf("start collector: %v", err)
	}
	t.Cleanup(func() { _ = collector.Stop() })

	return sinks, collector
}

// generateTenant emits n traces from topo whose resource carries the tenant
// attribute the routing pipelines decide on, returning the captured stubs.
func generateTenant(t testingT, topo *Topology, endpoint, tenant string, n int, seed uint64) []tracetest.SpanStub {
	t.Helper()
	res := resource.NewSchemaless(attribute.String("tenant", tenant))
	return generateAndCapture(t, topo, endpoint, n, seed, nil, sdktrace.WithResource(res))
}

// settledBackends waits for each sink to hold its expected span count (zero
// means no exact count is known), lets both settle so a misrouted straggler
// cannot slip in after the assertion, and returns the received spans keyed by
// backend name.
func settledBackends(sinks []*pipelinetest.Sink, wantPrimary, wantSecondary int) map[string][]*tracepb.Span {
	sinks[0].WaitFor(wantPrimary, settleMax)
	sinks[1].WaitFor(wantSecondary, settleMax)
	return map[string][]*tracepb.Span{
		"primary":   sinks[0].WaitSettled(propertySettleIdle, settleMax),
		"secondary": sinks[1].WaitSettled(propertySettleIdle, settleMax),
	}
}

// stubKeys reduces captured stubs to their identity set.
func stubKeys(stubs []tracetest.SpanStub) map[string]struct{} {
	keys := make(map[string]struct{}, len(stubs))
	for _, s := range stubs {
		tid := s.SpanContext.TraceID()
		sid := s.SpanContext.SpanID()
		keys[pipelinetest.SpanKey(tid[:], sid[:])] = struct{}{}
	}
	return keys
}

// addKey records a SpanKey-encoded identity into sent. Sent.Add takes raw ID
// bytes, so decode the hex key back into its parts.
func addKey(sent *pipelinetest.Sent, key string) {
	tid, sid, err := pipelinetest.ParseSpanKey(key)
	if err != nil {
		panic(err)
	}
	sent.Add(tid, sid)
}
