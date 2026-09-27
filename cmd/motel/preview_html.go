package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"math"
	"slices"
	"strings"

	"github.com/andrewh/motel/pkg/synth"
)

type previewReport struct {
	Title          string
	Duration       string
	TrafficSVG     template.HTML
	MapSVG         template.HTML
	Services       []previewService
	Scenarios      []previewScenario
	Roots          []string
	OperationCount int
	CallCount      int
	Run            previewRunReport
}

type previewRunReport struct {
	Duration      string
	Seed          uint64
	MaxTraces     int
	Stats         *synth.Stats
	Traces        []previewTrace
	Metrics       []previewMetric
	Logs          []previewLog
	RawJSON       string
	CapturedSpans int
	DroppedSpans  int
}

type previewTrace struct {
	ID    string
	Spans []previewSpanRow
}

type previewSpanRow struct {
	Service    string
	Name       string
	Kind       string
	StartMs    float64
	DurationMs float64
	Status     string
	Depth      int
	Left       float64
	Width      float64
	SpanID     string
}

type previewService struct {
	Name       string
	Operations []previewOperation
}

type previewOperation struct {
	Ref   string
	Root  bool
	Calls []string
}

type previewScenario struct {
	Name    string
	Window  string
	Traffic bool
	Changes []string
}

func renderPreviewHTML(w io.Writer, title string, cfg *synth.Config, topo *synth.Topology, samples []rateSample, scenarios []synth.Scenario, capture *previewCapture) error {
	var chart bytes.Buffer
	if err := renderSVG(&chart, samples, scenarios, title); err != nil {
		return err
	}
	mapSVG := renderServiceMap(topo, scenarios)
	report := previewReport{
		Title:      title,
		Duration:   samples[len(samples)-1].Elapsed.String(),
		TrafficSVG: template.HTML(chart.String()),
		MapSVG:     template.HTML(mapSVG),
	}
	if capture != nil {
		var err error
		report.Run, err = preparePreviewRunReport(capture)
		if err != nil {
			return err
		}
	}
	rootSet := make(map[string]bool, len(topo.Roots))
	for _, root := range topo.Roots {
		rootSet[root.Ref] = true
		report.Roots = append(report.Roots, root.Ref)
	}
	slices.Sort(report.Roots)
	for _, name := range sortedServiceNames(topo) {
		svc := topo.Services[name]
		item := previewService{Name: name}
		for _, opName := range sortedOperationNames(svc) {
			op := svc.Operations[opName]
			entry := previewOperation{Ref: op.Ref, Root: rootSet[op.Ref]}
			for _, call := range op.Calls {
				entry.Calls = append(entry.Calls, describeCall(call))
				report.CallCount++
			}
			slices.Sort(entry.Calls)
			item.Operations = append(item.Operations, entry)
			report.OperationCount++
		}
		report.Services = append(report.Services, item)
	}
	for i, sc := range scenarios {
		entry := previewScenario{
			Name:    sc.Name,
			Window:  fmt.Sprintf("%s–%s", sc.Start, sc.End),
			Traffic: sc.Traffic != nil,
		}
		refs := make([]string, 0, len(sc.Overrides))
		for ref := range sc.Overrides {
			refs = append(refs, ref)
		}
		slices.Sort(refs)
		for _, ref := range refs {
			override := sc.Overrides[ref]
			for target := range override.RemoveCalls {
				entry.Changes = append(entry.Changes, fmt.Sprintf("%s removes %s", ref, target))
			}
			for _, call := range override.AddCalls {
				entry.Changes = append(entry.Changes, fmt.Sprintf("%s adds %s", ref, describeCall(call)))
			}
			configured := cfg.Scenarios[i].Override[ref]
			if configured.Duration != "" {
				entry.Changes = append(entry.Changes, fmt.Sprintf("%s duration: %s", ref, configured.Duration))
			}
			if configured.ErrorRate != "" {
				entry.Changes = append(entry.Changes, fmt.Sprintf("%s error rate: %s", ref, configured.ErrorRate))
			}
			if len(configured.Attributes) > 0 {
				entry.Changes = append(entry.Changes, fmt.Sprintf("%s changes %d attributes", ref, len(configured.Attributes)))
			}
			if len(configured.Metrics) > 0 {
				entry.Changes = append(entry.Changes, fmt.Sprintf("%s changes %d metrics", ref, len(configured.Metrics)))
			}
			if configured.Logs != nil {
				entry.Changes = append(entry.Changes, fmt.Sprintf("%s changes logs", ref))
			}
		}
		slices.Sort(entry.Changes)
		report.Scenarios = append(report.Scenarios, entry)
	}
	return previewHTMLTemplate.Execute(w, report)
}

func preparePreviewRunReport(capture *previewCapture) (previewRunReport, error) {
	raw, err := json.MarshalIndent(capture, "", "  ")
	if err != nil {
		return previewRunReport{}, fmt.Errorf("encoding preview capture: %w", err)
	}
	report := previewRunReport{
		Duration:      capture.Duration,
		Seed:          capture.Seed,
		MaxTraces:     capture.MaxTraces,
		Stats:         capture.Stats,
		Metrics:       capture.Metrics,
		Logs:          capture.Logs,
		RawJSON:       string(raw),
		CapturedSpans: len(capture.Spans),
		DroppedSpans:  capture.DroppedSpans,
	}
	byTrace := make(map[string][]previewSpan)
	for _, span := range capture.Spans {
		byTrace[span.TraceID] = append(byTrace[span.TraceID], span)
	}
	ids := make([]string, 0, len(byTrace))
	for id := range byTrace {
		ids = append(ids, id)
	}
	slices.SortFunc(ids, func(a, b string) int {
		startA, startB := byTrace[a][0].StartMs, byTrace[b][0].StartMs
		if startA < startB {
			return -1
		}
		if startA > startB {
			return 1
		}
		return strings.Compare(a, b)
	})
	for _, id := range ids {
		spans := byTrace[id]
		first := spans[0].StartMs
		last := float64(first)
		byID := make(map[string]previewSpan, len(spans))
		for _, span := range spans {
			first = min(first, span.StartMs)
			last = max(last, float64(span.StartMs)+span.DurationMs)
			byID[span.SpanID] = span
		}
		trace := previewTrace{ID: id}
		for _, span := range spans {
			depth := 0
			parent := span.ParentSpanID
			for parent != "" && depth < len(spans) {
				ancestor, ok := byID[parent]
				if !ok {
					break
				}
				depth++
				parent = ancestor.ParentSpanID
			}
			extent := max(last-float64(first), 1)
			left := (float64(span.StartMs-first) / extent) * 100
			width := max(span.DurationMs/extent*100, 1)
			width = min(width, 100-left)
			trace.Spans = append(trace.Spans, previewSpanRow{
				Service:    span.Service,
				Name:       span.Name,
				Kind:       span.Kind,
				StartMs:    float64(span.StartMs - first),
				DurationMs: span.DurationMs,
				Status:     span.Status,
				Depth:      depth,
				Left:       left,
				Width:      width,
				SpanID:     span.SpanID,
			})
		}
		report.Traces = append(report.Traces, trace)
	}
	return report, nil
}

func describeCall(call synth.Call) string {
	parts := []string{call.Operation.Ref}
	if call.Probability > 0 && call.Probability < 1 {
		parts = append(parts, fmt.Sprintf("%g%%", call.Probability*100))
	}
	if call.Count > 1 {
		parts = append(parts, fmt.Sprintf("×%d", call.Count))
	}
	if call.Async {
		parts = append(parts, "async")
	}
	if call.Producer {
		parts = append(parts, "producer")
	}
	if call.Condition != "" {
		parts = append(parts, "when "+call.Condition)
	}
	return strings.Join(parts, " · ")
}

func sortedServiceNames(topo *synth.Topology) []string {
	names := make([]string, 0, len(topo.Services))
	for name := range topo.Services {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

func sortedOperationNames(svc *synth.Service) []string {
	names := make([]string, 0, len(svc.Operations))
	for name := range svc.Operations {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

type mapEdge struct {
	Source string
	Target string
	Added  bool
}

func renderServiceMap(topo *synth.Topology, scenarios []synth.Scenario) string {
	names := sortedServiceNames(topo)
	depths := make(map[*synth.Operation]int)
	var visit func(*synth.Operation, int)
	visit = func(op *synth.Operation, depth int) {
		if previous, seen := depths[op]; seen && previous >= depth {
			return
		}
		depths[op] = depth
		for _, call := range op.Calls {
			visit(call.Operation, depth+1)
		}
	}
	for _, root := range topo.Roots {
		visit(root, 0)
	}
	layers := make(map[string]int, len(names))
	for op, depth := range depths {
		layers[op.Service.Name] = max(layers[op.Service.Name], depth)
	}
	columns := make(map[int][]string)
	maxLayer := 0
	for _, name := range names {
		layer := layers[name]
		columns[layer] = append(columns[layer], name)
		maxLayer = max(maxLayer, layer)
	}
	maxRows := 0
	for _, column := range columns {
		maxRows = max(maxRows, len(column))
	}
	const (
		nodeWidth  = 176
		nodeHeight = 48
		cellWidth  = 236
		cellHeight = 88
		padding    = 42
	)
	width := 2*padding + (maxLayer+1)*cellWidth
	height := 2*padding + maxRows*cellHeight
	type point struct{ x, y int }
	positions := make(map[string]point, len(names))
	for col, column := range columns {
		for row, name := range column {
			positions[name] = point{padding + col*cellWidth + nodeWidth/2, padding + row*cellHeight + nodeHeight/2}
		}
	}
	edges := make(map[string]mapEdge)
	serviceByRef := make(map[string]string)
	for _, name := range names {
		for _, opName := range sortedOperationNames(topo.Services[name]) {
			op := topo.Services[name].Operations[opName]
			serviceByRef[op.Ref] = name
			for _, call := range op.Calls {
				target := call.Operation.Service.Name
				edges[name+"\x00"+target] = mapEdge{Source: name, Target: target}
			}
		}
	}
	for _, sc := range scenarios {
		for ref, override := range sc.Overrides {
			if !override.HasCallChanges() {
				continue
			}
			source := serviceByRef[ref]
			if source == "" {
				continue
			}
			for _, call := range override.AddCalls {
				target := call.Operation.Service.Name
				key := source + "\x00" + target
				if _, exists := edges[key]; !exists {
					edges[key] = mapEdge{Source: source, Target: target, Added: true}
				}
			}
		}
	}
	keys := make([]string, 0, len(edges))
	for key := range edges {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	var b strings.Builder
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 %d %d" width="%d" height="%d" role="img" aria-label="Defined service topology"><defs><marker id="arrow" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="7" markerHeight="7" orient="auto"><path d="M 0 0 L 10 5 L 0 10 z" fill="#47667a"/></marker></defs>`, width, height, width, height)
	b.WriteString(`<style>.edge{fill:none;stroke:#47667a;stroke-width:1.6;marker-end:url(#arrow)}.added{stroke:#b36b28;stroke-dasharray:6 4}.node{fill:#f3f6f5;stroke:#466273;stroke-width:1.5}.root{fill:#e4f2ea;stroke:#217b54}.label{font:600 14px ui-sans-serif,system-ui,sans-serif;fill:#17303e}</style>`)
	for _, key := range keys {
		edge := edges[key]
		from, to := positions[edge.Source], positions[edge.Target]
		className := "edge"
		if edge.Added {
			className += " added"
		}
		b.WriteString(`<path class="` + className + `" d="`)
		switch {
		case edge.Source == edge.Target:
			fmt.Fprintf(&b, "M %d %d C %d %d, %d %d, %d %d", from.x+nodeWidth/2-15, from.y-nodeHeight/2, from.x+nodeWidth/2+45, from.y-64, from.x-nodeWidth/2-45, from.y-64, from.x-nodeWidth/2+15, from.y-nodeHeight/2)
		default:
			startX, endX := from.x+nodeWidth/2, to.x-nodeWidth/2
			if to.x <= from.x {
				startX, endX = from.x-nodeWidth/2, to.x+nodeWidth/2
			}
			bend := int(math.Abs(float64(endX-startX))) / 2
			if endX < startX {
				bend = -bend
			}
			fmt.Fprintf(&b, "M %d %d C %d %d, %d %d, %d %d", startX, from.y, startX+bend, from.y, endX-bend, to.y, endX, to.y)
		}
		fmt.Fprintf(&b, `"><title>%s → %s</title></path>`, xmlEscape(edge.Source), xmlEscape(edge.Target))
	}
	rootServices := make(map[string]bool, len(topo.Roots))
	for _, root := range topo.Roots {
		rootServices[root.Service.Name] = true
	}
	for _, name := range names {
		p := positions[name]
		className := "node"
		if rootServices[name] {
			className += " root"
		}
		fmt.Fprintf(&b, `<g><rect class="%s" x="%d" y="%d" width="%d" height="%d" rx="5"/><title>%s</title><text class="label" x="%d" y="%d" text-anchor="middle">%s</text></g>`, className, p.x-nodeWidth/2, p.y-nodeHeight/2, nodeWidth, nodeHeight, xmlEscape(name), p.x, p.y+5, xmlEscape(shortenMapLabel(name)))
	}
	b.WriteString(`</svg>`)
	return b.String()
}

func shortenMapLabel(name string) string {
	const limit = 21
	runes := []rune(name)
	if len(runes) <= limit {
		return name
	}
	return string(runes[:limit-1]) + "…"
}

var previewHTMLTemplate = template.Must(template.New("preview").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>motel preview · {{.Title}}</title>
<style>
:root{color-scheme:light;--ink:#17303e;--muted:#526875;--line:#cbd8d7;--paper:#f7f8f3;--surface:#fffefa;--green:#217b54;--amber:#a15e1d}
*{box-sizing:border-box}body{margin:0;background:var(--paper);color:var(--ink);font:16px/1.5 ui-sans-serif,system-ui,sans-serif}main{max-width:1200px;margin:auto;padding:clamp(24px,5vw,72px)}header{border-bottom:2px solid var(--ink);padding-bottom:28px;margin-bottom:40px}.eyebrow{color:var(--green);font-size:.75rem;font-weight:800;letter-spacing:.14em;text-transform:uppercase}h1{font-size:clamp(2rem,5vw,4rem);line-height:1.05;letter-spacing:-.045em;margin:12px 0;overflow-wrap:anywhere}h2{font-size:clamp(1.4rem,2vw,2rem);letter-spacing:-.025em;margin:0 0 12px}h3{font-size:1.15rem;margin:24px 0 10px}p{margin:0 0 16px}.subtle{color:var(--muted)}.facts{display:flex;flex-wrap:wrap;gap:10px 32px;margin:24px 0 0;padding:0;list-style:none}.facts strong{font-size:1.5rem;margin-right:7px;font-variant-numeric:tabular-nums}.section{margin:0 0 54px}.graphic{background:var(--surface);border:1px solid var(--line);padding:12px;overflow:auto}.graphic svg{display:block;max-width:100%;height:auto;margin:auto}.map svg{min-width:700px;max-width:none}.legend{display:flex;gap:24px;flex-wrap:wrap;margin:14px 0 0;color:var(--muted);font-size:.875rem}.legend span:before{content:"";display:inline-block;width:24px;border-top:2px solid var(--ink);vertical-align:middle;margin-right:8px}.legend .extra:before{border-color:var(--amber);border-style:dashed}.legend .root:before{border-color:var(--green)}.service{border-top:1px solid var(--line);padding:18px 0;display:grid;grid-template-columns:minmax(150px,1fr) 3fr;gap:24px}.service h3{margin:0;font-size:1.1rem}.operation{margin:0 0 12px}.operation strong{overflow-wrap:anywhere}.operation ul{margin:4px 0 0;padding-left:22px;color:var(--muted)}.tag{color:var(--green);font-size:.75rem;font-weight:700;text-transform:uppercase;letter-spacing:.08em}.scenario{border-top:1px solid var(--line);padding:18px 0;display:grid;grid-template-columns:minmax(150px,1fr) 3fr;gap:24px}.scenario h3{margin:0}.scenario ul{margin:0;padding-left:22px}.note{font-size:.875rem;color:var(--muted)}details{border-top:1px solid var(--line);padding:12px 0}summary{cursor:pointer;font-weight:650;overflow-wrap:anywhere}.trace{margin:16px 0;min-width:600px}.trace-row{display:grid;grid-template-columns:minmax(170px,1fr) 100px 90px minmax(180px,2fr);gap:12px;align-items:center;padding:5px 0;border-bottom:1px solid var(--line);font-size:.82rem}.trace-name{overflow-wrap:anywhere}.trace-timeline{height:14px;background:#e8eeec;position:relative}.trace-bar{position:absolute;top:2px;height:10px;background:var(--green)}.trace-bar.error{background:var(--amber)}.scroll{overflow:auto}.data-table{width:100%;border-collapse:collapse;font-size:.875rem}.data-table th,.data-table td{text-align:left;border-bottom:1px solid var(--line);padding:8px;vertical-align:top}.data-table td{overflow-wrap:anywhere}pre{white-space:pre-wrap;overflow-wrap:anywhere;font-size:.75rem;max-height:600px;overflow:auto;padding:16px;background:var(--surface)}code{font:inherit;overflow-wrap:anywhere}footer{border-top:1px solid var(--line);padding-top:18px;color:var(--muted);font-size:.875rem}@media(max-width:650px){.service,.scenario{display:block}.service h3,.scenario h3{margin-bottom:10px}}
</style>
</head>
<body><main>
<header><div class="eyebrow">motel / topology preview</div><h1>{{.Title}}</h1><p class="subtle">Defined topology and effective trace rate over {{.Duration}}.</p><ul class="facts"><li><strong>{{len .Services}}</strong>services</li><li><strong>{{.OperationCount}}</strong>operations</li><li><strong>{{.CallCount}}</strong>defined calls</li><li><strong>{{len .Roots}}</strong>root operations</li><li><strong>{{len .Scenarios}}</strong>scenarios</li></ul></header>
<section class="section"><h2>Trace rate</h2><p class="subtle">Traffic patterns and scenario traffic overrides. Shaded regions show scenario windows.</p><div class="graphic">{{.TrafficSVG}}</div></section>
<section class="section"><h2>Service map</h2><p class="subtle">Arrows show defined calls between services. Dashed arrows appear only in scenario additions.</p><div class="graphic map">{{.MapSVG}}</div><div class="legend"><span class="root">contains a root operation</span><span>defined call</span><span class="extra">scenario addition</span></div></section>
{{if .Run.Stats}}<section class="section"><h2>Captured run</h2><p class="subtle">One local simulation: up to {{.Run.Duration}}, seed {{.Run.Seed}}. Up to {{.Run.MaxTraces}} traces, 1,000 spans, 500 metric data points, and 500 logs are stored in this file.</p><ul class="facts"><li><strong>{{.Run.Stats.Traces}}</strong>traces generated</li><li><strong>{{.Run.Stats.Spans}}</strong>spans generated</li><li><strong>{{.Run.Stats.Errors}}</strong>span errors</li><li><strong>{{.Run.Stats.ElapsedMs}}</strong>ms elapsed</li><li><strong>{{len .Run.Metrics}}</strong>metric points</li><li><strong>{{len .Run.Logs}}</strong>logs</li></ul>{{if .Run.DroppedSpans}}<p class="note">{{.Run.CapturedSpans}} spans captured; {{.Run.DroppedSpans}} omitted by the capture limit.</p>{{end}}
<h3>Traces</h3>{{if .Run.Traces}}{{range .Run.Traces}}<details><summary>Trace {{.ID}} · {{len .Spans}} captured spans</summary><div class="scroll"><div class="trace">{{range .Spans}}<div class="trace-row"><div class="trace-name" style="padding-left:{{.Depth}}em"><strong>{{.Service}}</strong> / {{.Name}} <span class="note">{{.Kind}}</span></div><span>{{printf "%.2f" .StartMs}} ms</span><span>{{printf "%.2f" .DurationMs}} ms</span><div class="trace-timeline" title="{{.Status}} · {{.SpanID}}"><span class="trace-bar {{if eq .Status "Error"}}error{{end}}" style="left:{{printf "%.2f" .Left}}%;width:{{printf "%.2f" .Width}}%"></span></div></div>{{end}}</div></div></details>{{end}}{{else}}<p class="note">No traces captured.</p>{{end}}
<h3>Metrics</h3>{{if .Run.Metrics}}<div class="scroll"><table class="data-table"><thead><tr><th>Service</th><th>Metric</th><th>Type</th><th>Value</th></tr></thead><tbody>{{range .Run.Metrics}}<tr><td>{{.Service}}</td><td>{{.Name}}</td><td>{{.Type}}</td><td>{{.Value}} {{.Unit}}</td></tr>{{end}}</tbody></table></div>{{else}}<p class="note">No metric data points emitted.</p>{{end}}
<h3>Logs</h3>{{if .Run.Logs}}<div class="scroll"><table class="data-table"><thead><tr><th>Service</th><th>Severity</th><th>Message</th><th>Trace</th></tr></thead><tbody>{{range .Run.Logs}}<tr><td>{{.Service}}</td><td>{{.Severity}}</td><td>{{.Body}}</td><td>{{.TraceID}}</td></tr>{{end}}</tbody></table></div>{{else}}<p class="note">No logs emitted.</p>{{end}}
<details><summary>Raw captured output (JSON)</summary><pre>{{.Run.RawJSON}}</pre></details></section>{{end}}
<section class="section"><h2>Operations and calls</h2><p class="note">Call details are from the baseline topology. Probabilities below 100%, repeated calls, and async calls are labelled.</p>{{range .Services}}<div class="service"><h3>{{.Name}}</h3><div>{{range .Operations}}<div class="operation"><strong>{{.Ref}}</strong>{{if .Root}} <span class="tag">root</span>{{end}}{{if .Calls}}<ul>{{range .Calls}}<li>{{.}}</li>{{end}}</ul>{{else}}<p class="note">No downstream calls</p>{{end}}</div>{{end}}</div></div>{{end}}</section>
<section class="section"><h2>Scenarios</h2>{{if .Scenarios}}{{range .Scenarios}}<div class="scenario"><div><h3>{{.Name}}</h3><span class="note">{{.Window}}</span></div><div>{{if .Traffic}}<p>Traffic override</p>{{end}}{{if .Changes}}<ul>{{range .Changes}}<li>{{.}}</li>{{end}}</ul>{{end}}{{if and (not .Traffic) (not .Changes)}}<p class="note">No traffic or operation overrides.</p>{{end}}</div></div>{{end}}{{else}}<p class="note">No scenarios defined.</p>{{end}}</section>
<footer>Static preview of the topology definition. The map is not an observed trace or a throughput forecast.</footer>
</main></body></html>`))
