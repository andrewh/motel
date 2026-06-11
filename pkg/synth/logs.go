// LogObserver emits topology-defined log records and derived error/slow logs.
// Topology log templates support severity, body interpolation, conditions,
// probability, and timing anchors. Services without topology logs fall back
// to derived ERROR logs for error spans and WARN logs for slow spans.
package synth

import (
	"context"
	"fmt"
	"maps"
	"math/rand/v2"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/otel/log"
	"go.opentelemetry.io/otel/trace"
)

// severityByName maps normalised severity text to OTel log severity numbers.
var severityByName = map[string]log.Severity{
	logSeverityTrace: log.SeverityTrace,
	logSeverityDebug: log.SeverityDebug,
	logSeverityInfo:  log.SeverityInfo,
	logSeverityWarn:  log.SeverityWarn,
	logSeverityError: log.SeverityError,
	logSeverityFatal: log.SeverityFatal,
}

// placeholderPattern matches {key} placeholders in log body templates.
var placeholderPattern = regexp.MustCompile(`\{([^{}]+)\}`)

// logTemplate holds one resolved topology log definition ready for emission.
type logTemplate struct {
	severity     log.Severity
	severityText string
	body         string
	condition    string
	probability  float64
	atEnd        bool
	delay        time.Duration
	attrGens     map[string]AttributeGenerator
	attrKeys     []string // sorted for deterministic attribute order
	operation    string   // non-empty if operation-level (fires only for this op)
}

// LogObserver emits log records for observed spans.
type LogObserver struct {
	loggers       map[string]log.Logger
	slowThreshold time.Duration
	templates     map[string][]logTemplate
	serviceNames  map[string]bool // for disambiguating override refs containing dots
	rng           *rand.Rand
	mu            sync.Mutex

	overrideMu   sync.RWMutex
	addTemplates map[string][]logTemplate // scenario-added templates keyed by service
	disabled     map[string]bool          // scopes whose base logs are muted, keyed by override ref
}

// NewLogObserver creates a LogObserver from topology log definitions.
// Each logger should come from a LoggerProvider whose resource has the correct service.name.
// Services that define no topology logs emit derived ERROR logs for error spans
// and WARN logs for spans exceeding slowThreshold (0 disables slow detection).
// A nil topo disables topology logs entirely; a nil rng creates a new source.
func NewLogObserver(loggers map[string]log.Logger, topo *Topology, slowThreshold time.Duration, rng *rand.Rand) (*LogObserver, error) {
	if rng == nil {
		rng = rand.New(rand.NewPCG(rand.Uint64(), rand.Uint64())) //nolint:gosec // synthetic data, not security-sensitive
	}

	templates := make(map[string][]logTemplate)
	serviceNames := make(map[string]bool)
	if topo != nil {
		for svcName := range topo.Services {
			serviceNames[svcName] = true
		}
		for svcName, svc := range topo.Services {
			var tpls []logTemplate

			// Service-level logs (fire for every span in this service)
			for _, ld := range svc.Logs {
				tpl, err := newLogTemplate(ld, "")
				if err != nil {
					return nil, fmt.Errorf("service %q: %w", svcName, err)
				}
				tpls = append(tpls, tpl)
			}

			// Operation-level logs (fire only for the specific operation)
			opNames := make([]string, 0, len(svc.Operations))
			for opName := range svc.Operations {
				opNames = append(opNames, opName)
			}
			slices.Sort(opNames)
			for _, opName := range opNames {
				for _, ld := range svc.Operations[opName].Logs {
					tpl, err := newLogTemplate(ld, opName)
					if err != nil {
						return nil, fmt.Errorf("service %q operation %q: %w", svcName, opName, err)
					}
					tpls = append(tpls, tpl)
				}
			}

			if len(tpls) > 0 {
				templates[svcName] = tpls
			}
		}
	}

	return &LogObserver{
		loggers:       loggers,
		slowThreshold: slowThreshold,
		templates:     templates,
		serviceNames:  serviceNames,
		rng:           rng,
	}, nil
}

// SetOverrides replaces the active scenario log overrides. The engine calls
// this as scenario windows open and close; a nil map clears all overrides.
// Added log definitions are pre-built into templates here so the per-span
// path only performs map lookups.
func (l *LogObserver) SetOverrides(overrides map[string]Override) {
	var added map[string][]logTemplate
	var disabled map[string]bool
	for _, ref := range slices.Sorted(maps.Keys(overrides)) {
		ov := overrides[ref]
		if ov.DisableLogs {
			if disabled == nil {
				disabled = make(map[string]bool)
			}
			disabled[ref] = true
		}
		if len(ov.AddLogs) == 0 {
			continue
		}
		svcName, opName := l.splitScopeRef(ref)
		for _, ld := range ov.AddLogs {
			tpl, err := newLogTemplate(ld, opName)
			if err != nil {
				continue // severity was validated at config load; unreachable
			}
			if added == nil {
				added = make(map[string][]logTemplate)
			}
			added[svcName] = append(added[svcName], tpl)
		}
	}
	l.overrideMu.Lock()
	l.addTemplates = added
	l.disabled = disabled
	l.overrideMu.Unlock()
}

// splitScopeRef splits an override ref into service and operation names.
// A ref matching a known service name is service-scoped (empty operation);
// otherwise it is split at the first dot, like resolveRef.
func (l *LogObserver) splitScopeRef(ref string) (service, operation string) {
	if l.serviceNames[ref] {
		return ref, ""
	}
	svcName, opName, ok := strings.Cut(ref, ".")
	if !ok {
		return ref, ""
	}
	return svcName, opName
}

// newLogTemplate builds a logTemplate from a resolved LogDefinition.
func newLogTemplate(ld LogDefinition, operation string) (logTemplate, error) {
	sev, ok := severityByName[ld.Severity]
	if !ok {
		return logTemplate{}, fmt.Errorf("log severity %q is not one of TRACE, DEBUG, INFO, WARN, ERROR, FATAL", ld.Severity)
	}
	attrKeys := make([]string, 0, len(ld.Attributes))
	for k := range ld.Attributes {
		attrKeys = append(attrKeys, k)
	}
	slices.Sort(attrKeys)
	return logTemplate{
		severity:     sev,
		severityText: ld.Severity,
		body:         ld.Body,
		condition:    ld.Condition,
		probability:  ld.Probability,
		atEnd:        ld.AtEnd,
		delay:        ld.Delay,
		attrGens:     ld.Attributes,
		attrKeys:     attrKeys,
		operation:    operation,
	}, nil
}

// Observe emits log records for the completed span. Services with topology
// log templates emit those; services without fall back to derived error/slow logs.
// Active scenario overrides can mute the base logs for a scope and add
// window-scoped templates on top.
func (l *LogObserver) Observe(info SpanInfo) {
	logger := l.loggers[info.Service]
	if logger == nil {
		return
	}

	// Correlate emitted records with the span via the context's span context.
	ctx := trace.ContextWithSpanContext(context.Background(), info.SpanContext)

	l.overrideMu.RLock()
	added := l.addTemplates[info.Service]
	muted := l.disabled[info.Service] || l.disabled[info.Service+"."+info.Operation]
	l.overrideMu.RUnlock()

	templates := l.templates[info.Service]
	if !muted {
		if len(templates) == 0 && len(added) == 0 {
			l.emitDerived(ctx, logger, info)
			return
		}
		for i := range templates {
			l.emitTemplate(ctx, logger, &templates[i], info)
		}
	}
	for i := range added {
		l.emitTemplate(ctx, logger, &added[i], info)
	}
}

// emitTemplate emits one log record for a span if the template's operation
// scope, condition, and probability allow it.
func (l *LogObserver) emitTemplate(ctx context.Context, logger log.Logger, tpl *logTemplate, info SpanInfo) {
	if tpl.operation != "" && tpl.operation != info.Operation {
		return
	}
	switch tpl.condition {
	case logConditionError:
		if !info.IsError {
			return
		}
	case logConditionSuccess:
		if info.IsError {
			return
		}
	case logConditionSlow:
		if l.slowThreshold <= 0 || info.Duration <= l.slowThreshold {
			return
		}
	}

	// Lock only while sampling the RNG.
	l.mu.Lock()
	if l.rng.Float64() >= tpl.probability {
		l.mu.Unlock()
		return
	}
	attrValues := make(map[string]any, len(tpl.attrGens))
	for _, k := range tpl.attrKeys {
		attrValues[k] = tpl.attrGens[k].Generate(l.rng)
	}
	l.mu.Unlock()

	timestamp := info.Timestamp
	if tpl.atEnd {
		timestamp = timestamp.Add(info.Duration)
	}
	timestamp = timestamp.Add(tpl.delay)

	attrs := make([]log.KeyValue, 0, len(tpl.attrKeys)+1)
	attrs = append(attrs, log.String("operation.name", info.Operation))
	for _, k := range tpl.attrKeys {
		attrs = append(attrs, logKeyValue(k, attrValues[k]))
	}

	var rec log.Record
	rec.SetTimestamp(timestamp)
	rec.SetSeverity(tpl.severity)
	rec.SetSeverityText(tpl.severityText)
	rec.SetBody(log.StringValue(interpolateBody(tpl.body, attrValues, info)))
	rec.AddAttributes(attrs...)
	logger.Emit(ctx, rec)
}

// emitDerived emits the built-in ERROR and WARN log records for services
// without topology log definitions.
func (l *LogObserver) emitDerived(ctx context.Context, logger log.Logger, info SpanInfo) {
	attrs := []log.KeyValue{
		log.String("operation.name", info.Operation),
	}

	if info.IsError {
		var rec log.Record
		rec.SetTimestamp(info.Timestamp)
		rec.SetSeverity(log.SeverityError)
		rec.SetSeverityText(logSeverityError)
		rec.SetBody(log.StringValue(fmt.Sprintf("error in %s %s", info.Service, info.Operation)))
		rec.AddAttributes(attrs...)
		logger.Emit(ctx, rec)
	}

	if l.slowThreshold > 0 && info.Duration > l.slowThreshold {
		var rec log.Record
		rec.SetTimestamp(info.Timestamp)
		rec.SetSeverity(log.SeverityWarn)
		rec.SetSeverityText(logSeverityWarn)
		rec.SetBody(log.StringValue(fmt.Sprintf(
			"slow operation %s %s: %s (threshold %s)",
			info.Service, info.Operation, info.Duration, l.slowThreshold,
		)))
		rec.AddAttributes(attrs...)
		logger.Emit(ctx, rec)
	}
}

// interpolateBody replaces {key} placeholders in a log body template.
// Keys resolve against the record's own attributes first, then the span's
// attributes, then the built-ins service.name and operation.name.
// Unresolved placeholders are left as literal text.
func interpolateBody(body string, logAttrs map[string]any, info SpanInfo) string {
	if !strings.Contains(body, "{") {
		return body
	}
	return placeholderPattern.ReplaceAllStringFunc(body, func(match string) string {
		key := match[1 : len(match)-1]
		if v, ok := logAttrs[key]; ok {
			return fmt.Sprint(v)
		}
		for _, kv := range info.Attrs {
			if string(kv.Key) == key {
				return kv.Value.Emit()
			}
		}
		switch key {
		case "service.name":
			return info.Service
		case "operation.name":
			return info.Operation
		}
		return match
	})
}

// logKeyValue converts a generated attribute value to a typed log.KeyValue.
func logKeyValue(key string, value any) log.KeyValue {
	switch v := value.(type) {
	case string:
		return log.String(key, v)
	case bool:
		return log.Bool(key, v)
	case int:
		return log.Int(key, v)
	case int64:
		return log.Int64(key, v)
	case float64:
		return log.Float64(key, v)
	default:
		return log.String(key, fmt.Sprint(v))
	}
}
