package main

import (
	"compress/gzip"
	"encoding/csv"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
)

const (
	defaultLimit          = 1000
	defaultParentDuration = 5 * time.Millisecond
	defaultChildDuration  = time.Millisecond
	defaultTraceSpacing   = 10 * time.Millisecond
	defaultStartTime      = "2022-12-21T00:00:00Z"
	rootParentSpanID      = "0000000000000000"
	operationName         = "invoke"
)

type options struct {
	input        string
	limit        int
	profile      string
	includeEmpty bool
	start        time.Time
}

type parentRow struct {
	parentName        string
	children          []string
	numCalls          string
	numReturningCalls string
	concurrencyRate   string
	profile           string
}

type stdouttraceEvent struct {
	Name                 string      `json:"Name"`
	SpanContext          spanContext `json:"SpanContext"`
	Parent               spanContext `json:"Parent"`
	StartTime            time.Time   `json:"StartTime"`
	EndTime              time.Time   `json:"EndTime"`
	Attributes           []sdkAttr   `json:"Attributes,omitempty"`
	Resource             []sdkAttr   `json:"Resource,omitempty"`
	Status               sdkStatus   `json:"Status"`
	InstrumentationScope sdkScope    `json:"InstrumentationScope"`
}

type spanContext struct {
	TraceID string `json:"TraceID"`
	SpanID  string `json:"SpanID"`
}

type sdkAttr struct {
	Key   string   `json:"Key"`
	Value sdkValue `json:"Value"`
}

type sdkValue struct {
	Type  string `json:"Type"`
	Value any    `json:"Value"`
}

type sdkStatus struct {
	Code string `json:"Code"`
}

type sdkScope struct {
	Name string `json:"Name"`
}

func main() {
	opts, err := parseFlags()
	if err != nil {
		fatal(err)
	}
	if err := run(os.Stdout, opts); err != nil {
		fatal(err)
	}
}

func parseFlags() (options, error) {
	input := flag.String("input", "", "path to summary_data_atc23/data/parent-data.csv.gz")
	limit := flag.Int("limit", defaultLimit, "maximum parent invocations to emit after filtering")
	profile := flag.String("profile", "", "optional trace profile filter: ads, fetch, or raas")
	includeEmpty := flag.Bool("include-empty", false, "include parent invocations with an empty children_set")
	startValue := flag.String("start", defaultStartTime, "base timestamp for synthetic spans")
	flag.Parse()

	if *input == "" {
		return options{}, errors.New("-input is required")
	}
	if *limit < 1 {
		return options{}, errors.New("-limit must be positive")
	}
	start, err := time.Parse(time.RFC3339, *startValue)
	if err != nil {
		return options{}, fmt.Errorf("parse -start: %w", err)
	}
	return options{
		input:        *input,
		limit:        *limit,
		profile:      *profile,
		includeEmpty: *includeEmpty,
		start:        start,
	}, nil
}

func run(w io.Writer, opts options) error {
	rc, err := openInput(opts.input)
	if err != nil {
		return err
	}
	defer rc.Close() //nolint:errcheck // read-only input

	reader := csv.NewReader(rc)
	header, err := reader.Read()
	if err != nil {
		return fmt.Errorf("read header: %w", err)
	}
	cols := indexHeader(header)
	required := []string{"parent_name", "children_set", "num_calls", "num_returning_calls", "concurrency_rate", "profile"}
	for _, name := range required {
		if _, ok := cols[name]; !ok {
			return fmt.Errorf("missing required column %q", name)
		}
	}

	enc := json.NewEncoder(w)
	emitted := 0
	rowNumber := 1
	for {
		record, err := reader.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("read row %d: %w", rowNumber+1, err)
		}
		rowNumber++

		row, err := parseParentRow(record, cols)
		if err != nil {
			return fmt.Errorf("parse row %d: %w", rowNumber, err)
		}
		if opts.profile != "" && row.profile != opts.profile {
			continue
		}
		if !opts.includeEmpty && len(row.children) == 0 {
			continue
		}

		traceStart := opts.start.Add(time.Duration(emitted) * defaultTraceSpacing)
		events := invocationEvents(row, emitted, traceStart)
		for _, event := range events {
			if err := enc.Encode(event); err != nil {
				return fmt.Errorf("write span: %w", err)
			}
		}
		emitted++
		if emitted >= opts.limit {
			break
		}
	}
	if emitted == 0 {
		return errors.New("no parent invocations matched the requested filters")
	}
	return nil
}

func openInput(path string) (io.ReadCloser, error) {
	f, err := os.Open(path) //nolint:gosec // user-supplied input path
	if err != nil {
		return nil, fmt.Errorf("open input: %w", err)
	}
	if !strings.HasSuffix(path, ".gz") {
		return f, nil
	}
	gz, err := gzip.NewReader(f)
	if err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("open gzip stream: %w", err)
	}
	return combinedReadCloser{Reader: gz, closers: []io.Closer{gz, f}}, nil
}

type combinedReadCloser struct {
	io.Reader
	closers []io.Closer
}

func (c combinedReadCloser) Close() error {
	var closeErr error
	for _, closer := range c.closers {
		if err := closer.Close(); err != nil && closeErr == nil {
			closeErr = err
		}
	}
	return closeErr
}

func indexHeader(header []string) map[string]int {
	cols := make(map[string]int, len(header))
	for i, name := range header {
		cols[name] = i
	}
	return cols
}

func parseParentRow(record []string, cols map[string]int) (parentRow, error) {
	children, err := parseChildrenSet(record[cols["children_set"]])
	if err != nil {
		return parentRow{}, err
	}
	return parentRow{
		parentName:        record[cols["parent_name"]],
		children:          children,
		numCalls:          record[cols["num_calls"]],
		numReturningCalls: record[cols["num_returning_calls"]],
		concurrencyRate:   record[cols["concurrency_rate"]],
		profile:           record[cols["profile"]],
	}, nil
}

func parseChildrenSet(value string) ([]string, error) {
	value = strings.TrimSpace(value)
	if value == "" || value == "set()" {
		return nil, nil
	}
	if !strings.HasPrefix(value, "{") || !strings.HasSuffix(value, "}") {
		return nil, fmt.Errorf("children_set %q is not a Python set literal", value)
	}
	parts := strings.Split(strings.TrimSuffix(strings.TrimPrefix(value, "{"), "}"), ",")
	children := make([]string, 0, len(parts))
	for _, part := range parts {
		child := strings.Trim(strings.TrimSpace(part), `'"`)
		if child == "" {
			continue
		}
		children = append(children, child)
	}
	sort.Strings(children)
	return children, nil
}

func invocationEvents(row parentRow, index int, start time.Time) []stdouttraceEvent {
	traceID := fmt.Sprintf("%032x", index+1)
	rootSpanID := fmt.Sprintf("%016x", (index+1)<<16)
	rootService := serviceName(row.parentName)
	parentDuration := defaultParentDuration + time.Duration(len(row.children))*defaultChildDuration

	events := []stdouttraceEvent{{
		Name:        operationName,
		SpanContext: spanContext{TraceID: traceID, SpanID: rootSpanID},
		Parent:      spanContext{TraceID: traceID, SpanID: rootParentSpanID},
		StartTime:   start,
		EndTime:     start.Add(parentDuration),
		Attributes: []sdkAttr{
			stringAttr("meta.ingress_id", row.parentName),
			stringAttr("meta.profile", row.profile),
			stringAttr("meta.num_calls", row.numCalls),
			stringAttr("meta.num_returning_calls", row.numReturningCalls),
			stringAttr("meta.concurrency_rate", row.concurrencyRate),
		},
		Resource:             []sdkAttr{stringAttr("service.name", rootService)},
		Status:               sdkStatus{Code: "Unset"},
		InstrumentationScope: sdkScope{Name: rootService},
	}}

	parallel := isConcurrent(row.concurrencyRate)
	for i, child := range row.children {
		childStart := start.Add(defaultChildDuration)
		if !parallel {
			childStart = childStart.Add(time.Duration(i) * defaultChildDuration)
		}
		childService := serviceName(child)
		events = append(events, stdouttraceEvent{
			Name:        operationName,
			SpanContext: spanContext{TraceID: traceID, SpanID: fmt.Sprintf("%016x", ((index+1)<<16)+i+1)},
			Parent:      spanContext{TraceID: traceID, SpanID: rootSpanID},
			StartTime:   childStart,
			EndTime:     childStart.Add(defaultChildDuration),
			Attributes: []sdkAttr{
				stringAttr("meta.ingress_id", child),
				stringAttr("meta.profile", row.profile),
			},
			Resource:             []sdkAttr{stringAttr("service.name", childService)},
			Status:               sdkStatus{Code: "Unset"},
			InstrumentationScope: sdkScope{Name: childService},
		})
	}
	return events
}

func isConcurrent(value string) bool {
	rate, err := strconv.ParseFloat(value, 64)
	return err == nil && rate > 0
}

func serviceName(ingressID string) string {
	var b strings.Builder
	b.WriteString("meta-")
	lastHyphen := false
	for _, r := range strings.ToLower(ingressID) {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
			lastHyphen = false
		case !lastHyphen:
			b.WriteByte('-')
			lastHyphen = true
		}
	}
	return strings.TrimSuffix(b.String(), "-")
}

func stringAttr(key, value string) sdkAttr {
	return sdkAttr{
		Key: key,
		Value: sdkValue{
			Type:  "STRING",
			Value: value,
		},
	}
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
