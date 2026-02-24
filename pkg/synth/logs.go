// LogObserver derives log records from error and slow spans.
// Emits ERROR-severity logs for error spans and WARN-severity logs for slow spans.
package synth

import (
	"context"
	"fmt"
	"time"

	"go.opentelemetry.io/otel/log"
)

// LogObserver emits log records for notable span events.
type LogObserver struct {
	loggers       map[string]log.Logger
	slowThreshold time.Duration
}

// NewLogObserver creates a LogObserver that emits logs via per-service loggers.
// Each logger should come from a LoggerProvider whose resource has the correct service.name.
// A slowThreshold of 0 disables slow span detection.
func NewLogObserver(loggers map[string]log.Logger, slowThreshold time.Duration) *LogObserver {
	return &LogObserver{
		loggers:       loggers,
		slowThreshold: slowThreshold,
	}
}

// Observe emits log records for error spans and spans exceeding the slow threshold.
func (l *LogObserver) Observe(info SpanInfo) {
	logger := l.loggers[info.Service]
	if logger == nil {
		return
	}

	attrs := []log.KeyValue{
		log.String("operation.name", info.Operation),
	}

	if info.IsError {
		var rec log.Record
		rec.SetSeverity(log.SeverityError)
		rec.SetSeverityText("ERROR")
		rec.SetBody(log.StringValue(fmt.Sprintf("error in %s %s", info.Service, info.Operation)))
		rec.AddAttributes(attrs...)
		logger.Emit(context.Background(), rec)
	}

	if l.slowThreshold > 0 && info.Duration > l.slowThreshold {
		var rec log.Record
		rec.SetSeverity(log.SeverityWarn)
		rec.SetSeverityText("WARN")
		rec.SetBody(log.StringValue(fmt.Sprintf(
			"slow operation %s %s: %s (threshold %s)",
			info.Service, info.Operation, info.Duration, l.slowThreshold,
		)))
		rec.AddAttributes(attrs...)
		logger.Emit(context.Background(), rec)
	}
}
