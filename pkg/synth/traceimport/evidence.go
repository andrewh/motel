package traceimport

import (
	"bytes"
	"context"
	"fmt"
	"math"
	"math/rand/v2"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/andrewh/motel/pkg/synth"
	"go.opentelemetry.io/otel/trace/noop"
	"gopkg.in/yaml.v3"
)

const (
	medianProbability      = 0.5
	p95Probability         = 0.95
	statusFit              = "fit"
	statusPoorFit          = "poor_fit"
	statusInsufficient     = "insufficient_evidence"
	minimumFitObservations = 100
	modelSamples           = 2048
	fitRelativeTolerance   = 0.20
	fitAbsoluteTolerance   = time.Millisecond
	assessmentSeed         = 253
)

type ImportEvidence struct {
	RequestedMinSamples int                           `yaml:"requested_min_samples"`
	ResourceOmissions   map[string]int                `yaml:"resource_omissions"`
	SourceTraceCount    int                           `yaml:"source_trace_count"`
	SourceSpanCount     int                           `yaml:"source_span_count"`
	MissingParentCount  int                           `yaml:"missing_parent_count"`
	WindowSeconds       float64                       `yaml:"window_seconds"`
	TrafficRateBasis    string                        `yaml:"traffic_rate_basis"`
	MinimumObservations int                           `yaml:"minimum_observations"`
	RelativeTolerance   float64                       `yaml:"relative_tolerance"`
	AbsoluteToleranceNS int64                         `yaml:"absolute_tolerance_ns"`
	ModelSamples        int                           `yaml:"model_samples"`
	ModelSeed           uint64                        `yaml:"model_seed"`
	Reasons             []string                      `yaml:"reasons"`
	Operations          map[string]*OperationEvidence `yaml:"-"`
}

type OperationEvidence struct {
	Inference          InferenceEvidence                     `yaml:"inference"`
	Attributes         AttributeEvidence                     `yaml:"attributes"`
	RetainedAttributes map[string]synth.AttributeValueConfig `yaml:"-"`
	Latency            LatencyEvidence                       `yaml:"latency"`
}

type LatencyEvidence struct {
	ImportStatus       string      `yaml:"import_status"`
	ObservationCount   int         `yaml:"observation_count"`
	TimingAnomalyCount int         `yaml:"timing_anomaly_count"`
	OwnTime            FitEvidence `yaml:"own_time"`
	TotalTime          FitEvidence `yaml:"total_time"`
	Reasons            []string    `yaml:"reasons"`
}

type Quantiles struct {
	MedianNS float64 `yaml:"median_ns"`
	P95NS    float64 `yaml:"p95_ns"`
}

type FitEvidence struct {
	ImportStatus     string    `yaml:"import_status"`
	ObservationCount int       `yaml:"observation_count"`
	ModeledCount     int       `yaml:"modeled_count"`
	Observed         Quantiles `yaml:"observed"`
	Modeled          Quantiles `yaml:"modeled"`
	Reasons          []string  `yaml:"reasons"`
}

type modelObserver map[string][]float64

func (o modelObserver) Observe(s synth.SpanInfo) {
	ref := s.Service + "." + s.Operation
	o[ref] = append(o[ref], float64(s.Duration))
}

func assessImport(collector *StatsCollector, trees []*TraceTree, cfg *synth.Config, minSamples int) (*ImportEvidence, error) {
	evidence := &ImportEvidence{
		RequestedMinSamples: max(1, minSamples),
		SourceTraceCount:    len(trees), WindowSeconds: computeWindow(trees),
		MinimumObservations: minimumFitObservations, RelativeTolerance: fitRelativeTolerance,
		AbsoluteToleranceNS: int64(fitAbsoluteTolerance), ModelSamples: modelSamples, ModelSeed: assessmentSeed,
		Reasons:    []string{"capture_completeness_unknown", "independent_operation_and_call_sampling", "original_import_only"},
		Operations: make(map[string]*OperationEvidence), TrafficRateBasis: "observed_root_window",
	}
	if evidence.SourceTraceCount < evidence.RequestedMinSamples {
		evidence.Reasons = append(evidence.Reasons, "below_requested_trace_count")
	}
	if evidence.SourceTraceCount == 1 {
		evidence.Reasons = append(evidence.Reasons, "single_source_trace")
	}
	if evidence.WindowSeconds <= 0 || evidence.SourceTraceCount <= 1 {
		evidence.TrafficRateBasis = "default_1_per_second"
	}
	for _, tree := range trees {
		evidence.SourceSpanCount += len(tree.AllNodes)
		for _, root := range tree.Roots {
			if root.Span.ParentID != "" {
				evidence.MissingParentCount++
			}
		}
	}
	topo, err := synth.BuildTopology(cfg, nil)
	if err != nil {
		return nil, fmt.Errorf("building assessment topology: %w", err)
	}
	totals := modelObserver{}
	stats, err := synth.GenerateTraces(context.Background(), topo, synth.TracerProviderSource(noop.NewTracerProvider()), synth.GenerateOptions{Traces: modelSamples, Seed: assessmentSeed, Observers: []synth.SpanObserver{totals}})
	if err != nil {
		return nil, fmt.Errorf("generating assessment traces: %w", err)
	}
	for _, svcName := range sortedStringKeys(collector.Services) {
		svc := collector.Services[svcName]
		for _, opName := range sortedStringKeys(svc.Ops) {
			opStats := svc.Ops[opName]
			op := topo.Services[svcName].Operations[opName]
			rng := rand.New(rand.NewPCG(assessmentSeed, 0))
			own := make([]float64, modelSamples)
			for i := range own {
				own[i] = float64(op.Duration.Sample(rng))
			}
			latency := LatencyEvidence{ObservationCount: opStats.TotalCount, TimingAnomalyCount: opStats.TimingAnomalies,
				OwnTime: assessFit(opStats.OwnObservations, own), TotalTime: assessFit(opStats.TotalObservations, totals[op.Ref]),
				Reasons: []string{"own_time_subtracts_clipped_child_interval_union", "unobserved_children_remain_in_own_time"},
			}
			if opStats.TimingAnomalies > 0 {
				latency.Reasons = append(latency.Reasons, "timestamp_anomaly")
			}
			if evidence.MissingParentCount > 0 {
				latency.Reasons = append(latency.Reasons, "incomplete_capture")
			}
			if stats.SpansBounded > 0 {
				latency.Reasons = append(latency.Reasons, "regeneration_span_limit")
			}
			if opStats.TimingAnomalies > 0 || evidence.MissingParentCount > 0 || stats.SpansBounded > 0 {
				latency.OwnTime.ImportStatus = statusInsufficient
				latency.TotalTime.ImportStatus = statusInsufficient
			}
			latency.ImportStatus = statusFit
			for _, status := range []string{latency.OwnTime.ImportStatus, latency.TotalTime.ImportStatus} {
				if status == statusInsufficient {
					latency.ImportStatus = statusInsufficient
					break
				}
				if status == statusPoorFit {
					latency.ImportStatus = statusPoorFit
				}
			}
			evidence.Operations[op.Ref] = &OperationEvidence{Latency: latency, Inference: assessInference(opStats, svc.CallStyles[opName], evidence.RequestedMinSamples)}
		}
	}
	assessAttributes(trees, evidence)
	return evidence, nil
}

func quantiles(values []float64) Quantiles {
	if len(values) == 0 {
		return Quantiles{}
	}
	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
	return Quantiles{MedianNS: sorted[int(math.Ceil(float64(len(sorted))*medianProbability))-1], P95NS: sorted[int(math.Ceil(float64(len(sorted))*p95Probability))-1]}
}

func assessFit(observed, modeled []float64) FitEvidence {
	fit := FitEvidence{ImportStatus: statusFit, ObservationCount: len(observed), ModeledCount: len(modeled), Observed: quantiles(observed), Modeled: quantiles(modeled), Reasons: []string{}}
	if len(observed) < minimumFitObservations || len(modeled) < minimumFitObservations {
		fit.ImportStatus = statusInsufficient
		fit.Reasons = append(fit.Reasons, "too_few_observations")
		return fit
	}
	for _, q := range []struct {
		name              string
		observed, modeled float64
	}{{"median", fit.Observed.MedianNS, fit.Modeled.MedianNS}, {"p95", fit.Observed.P95NS, fit.Modeled.P95NS}} {
		if math.Abs(q.observed-q.modeled) > math.Max(float64(fitAbsoluteTolerance), fitRelativeTolerance*q.observed) {
			fit.ImportStatus = statusPoorFit
			fit.Reasons = append(fit.Reasons, q.name+"_outside_tolerance")
		}
	}
	return fit
}

func attachEvidence(data []byte, evidence *ImportEvidence) ([]byte, error) {
	var cfg inferredConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("reading inferred topology for evidence: %w", err)
	}
	cfg.Import = evidence
	for svcName, svc := range cfg.Services {
		for opName, op := range svc.Operations {
			op.Import = evidence.Operations[svcName+"."+opName]
			op.Attributes = make(map[string]synth.AttributeValueConfig, len(op.Import.RetainedAttributes))
			for key, attr := range op.Import.RetainedAttributes {
				if value, ok := attr.Value.(float64); ok {
					attr.Value = &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!float", Value: strconv.FormatFloat(value, 'g', -1, 64)}
				}
				op.Attributes[key] = attr
			}
			svc.Operations[opName] = op
		}
	}
	var buf bytes.Buffer
	encoder := yaml.NewEncoder(&buf)
	encoder.SetIndent(2)
	err := encoder.Encode(cfg)
	if err != nil {
		return nil, fmt.Errorf("saving import evidence: %w", err)
	}
	if err := encoder.Close(); err != nil {
		return nil, fmt.Errorf("closing evidence encoder: %w", err)
	}
	return buf.Bytes(), nil
}

func (r Result) Summary() string {
	if r.Evidence == nil {
		return "Import assessment unavailable for summary-only input.\n"
	}
	counts := map[string]int{}
	for _, op := range r.Evidence.Operations {
		counts[op.Latency.ImportStatus]++
	}
	return fmt.Sprintf("Imported %d traces (%d spans): latency fit %d, poor fit %d, insufficient evidence %d. Evidence describes the original import.\n", r.Evidence.SourceTraceCount, r.Evidence.SourceSpanCount, counts[statusFit], counts[statusPoorFit], counts[statusInsufficient])
}

func (r Result) MarkdownReport() string {
	var b strings.Builder
	b.WriteString("# Trace import report\n\n" + r.Summary())
	if r.Evidence == nil {
		return b.String()
	}
	b.WriteString("\n| Operation | Own-time assessment | Total-time assessment | Observations |\n| --- | --- | --- | --- |\n")
	for _, ref := range sortedStringKeys(r.Evidence.Operations) {
		op := r.Evidence.Operations[ref]
		fmt.Fprintf(&b, "| %s | %s | %s | %d |\n", strings.NewReplacer("|", "\\|", "\n", " ").Replace(ref), op.Latency.OwnTime.ImportStatus, op.Latency.TotalTime.ImportStatus, op.Latency.ObservationCount)
	}
	b.WriteString("\n## Structured evidence\n\n```yaml\n")
	data, _ := yaml.Marshal(r.Evidence)
	b.Write(data)
	for _, ref := range sortedStringKeys(r.Evidence.Operations) {
		data, _ = yaml.Marshal(map[string]*OperationEvidence{ref: r.Evidence.Operations[ref]})
		b.Write(data)
	}
	b.WriteString("```\n")
	return b.String()
}
