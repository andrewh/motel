package traceimport

import (
	"bufio"
	"compress/gzip"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
)

const (
	metaColumnParentName        = "parent_name"
	metaColumnChildrenSet       = "children_set"
	metaColumnNumCalls          = "num_calls"
	metaColumnNumReturningCalls = "num_returning_calls"
	metaColumnConcurrencyRate   = "concurrency_rate"
	metaColumnProfile           = "profile"

	metaOperationName       = "invoke"
	metaServicePrefix       = "meta-"
	metaIngressIDAttribute  = "meta.ingress_id"
	metaNameHashBytes       = 8
	metaFallbackServiceSlug = "ingress"
	metaParentDuration      = 5 * time.Millisecond
	metaChildDuration       = time.Millisecond
	gzipMagicByte0          = 0x1f
	gzipMagicByte1          = 0x8b
)

var requiredMetaSummaryColumns = []string{
	metaColumnParentName,
	metaColumnChildrenSet,
	metaColumnNumCalls,
	metaColumnNumReturningCalls,
	metaColumnConcurrencyRate,
	metaColumnProfile,
}

type metaParentRow struct {
	parentName        string
	children          []string
	numCalls          int
	numReturningCalls int
	concurrencyRate   float64
	profile           string
}

type metaNameMapper struct {
	names map[string]string
	attrs map[string]map[string]string
}

func importMetaSummary(r io.Reader, opts Options) (Result, error) {
	reader, closeReader, err := metaInputReader(r)
	if err != nil {
		return Result{}, err
	}
	defer closeReader()

	csvReader := csv.NewReader(reader)
	header, err := csvReader.Read()
	if err != nil {
		return Result{}, fmt.Errorf("read Meta summary header: %w", err)
	}
	cols := indexMetaHeader(header)
	for _, name := range requiredMetaSummaryColumns {
		if _, ok := cols[name]; !ok {
			return Result{}, fmt.Errorf("missing required Meta summary column %q", name)
		}
	}

	collector := NewStatsCollector()
	names := newMetaNameMapper()
	rowCount := 0
	sampleCount := 0
	spanCount := 0
	rowNumber := 1

	for {
		rowNumber++
		record, err := csvReader.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return Result{}, fmt.Errorf("read Meta summary row %d: %w", rowNumber, err)
		}
		row, err := parseMetaParentRow(record, cols)
		if err != nil {
			return Result{}, fmt.Errorf("parse Meta summary row %d: %w", rowNumber, err)
		}
		if opts.MetaProfile != "" && row.profile != opts.MetaProfile {
			continue
		}
		if !opts.MetaIncludeEmpty && len(row.children) == 0 {
			continue
		}

		weight := row.observationWeight()
		recordMetaInvocation(collector, names, row, weight)
		rowCount++
		sampleCount += weight
		spanCount += weight * (1 + len(row.children))
	}

	if rowCount == 0 {
		return Result{}, errors.New("no Meta summary rows matched the requested filters")
	}
	if sampleCount < opts.MinTraces {
		_, _ = fmt.Fprintf(opts.Warnings, "warning: only %d weighted Meta parent samples available from %d rows (requested minimum: %d); results may be inaccurate\n",
			sampleCount, rowCount, opts.MinTraces)
	}
	if sampleCount == 1 {
		_, _ = fmt.Fprintf(opts.Warnings, "warning: only 1 weighted Meta parent sample available; duration distributions will be exact values. Use more rows for statistical accuracy.\n")
	}
	reportConfidenceDiagnostics(collector, opts.MinTraces, opts.Warnings)

	yamlBytes, err := MarshalConfig(collector, names.serviceAttributes(), sampleCount, spanCount, 0)
	if err != nil {
		return Result{}, err
	}
	if err := validateRoundTrip(yamlBytes); err != nil {
		return Result{}, fmt.Errorf("round-trip validation failed (this is a bug): %w", err)
	}
	return Result{
		YAML:       yamlBytes,
		TraceCount: sampleCount,
		SpanCount:  spanCount,
	}, nil
}

func metaInputReader(r io.Reader) (io.Reader, func() error, error) {
	br := bufio.NewReader(r)
	magic, err := br.Peek(2)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, nil, fmt.Errorf("read Meta summary input: %w", err)
	}
	if len(magic) == 2 && magic[0] == gzipMagicByte0 && magic[1] == gzipMagicByte1 {
		gz, err := gzip.NewReader(br)
		if err != nil {
			return nil, nil, fmt.Errorf("open gzip stream: %w", err)
		}
		return gz, gz.Close, nil
	}
	return br, func() error { return nil }, nil
}

func indexMetaHeader(header []string) map[string]int {
	cols := make(map[string]int, len(header))
	for i, name := range header {
		cols[strings.TrimSpace(name)] = i
	}
	return cols
}

func parseMetaParentRow(record []string, cols map[string]int) (metaParentRow, error) {
	parentName, err := metaColumnValue(record, cols, metaColumnParentName)
	if err != nil {
		return metaParentRow{}, err
	}
	childrenValue, err := metaColumnValue(record, cols, metaColumnChildrenSet)
	if err != nil {
		return metaParentRow{}, err
	}
	children, err := parseMetaChildrenSet(childrenValue)
	if err != nil {
		return metaParentRow{}, err
	}
	numCalls, err := parseMetaCountColumn(record, cols, metaColumnNumCalls)
	if err != nil {
		return metaParentRow{}, err
	}
	numReturningCalls, err := parseMetaCountColumn(record, cols, metaColumnNumReturningCalls)
	if err != nil {
		return metaParentRow{}, err
	}
	concurrencyRate, err := parseMetaFloatColumn(record, cols, metaColumnConcurrencyRate)
	if err != nil {
		return metaParentRow{}, err
	}
	profile, err := metaColumnValue(record, cols, metaColumnProfile)
	if err != nil {
		return metaParentRow{}, err
	}
	return metaParentRow{
		parentName:        parentName,
		children:          children,
		numCalls:          numCalls,
		numReturningCalls: numReturningCalls,
		concurrencyRate:   concurrencyRate,
		profile:           profile,
	}, nil
}

func metaColumnValue(record []string, cols map[string]int, name string) (string, error) {
	idx, ok := cols[name]
	if !ok {
		return "", fmt.Errorf("missing column %q", name)
	}
	if idx >= len(record) {
		return "", fmt.Errorf("missing value for column %q", name)
	}
	return strings.TrimSpace(record[idx]), nil
}

func parseMetaCountColumn(record []string, cols map[string]int, name string) (int, error) {
	value, err := metaColumnValue(record, cols, name)
	if err != nil {
		return 0, err
	}
	if n, err := strconv.Atoi(value); err == nil {
		if n < 0 {
			return 0, fmt.Errorf("%s %q must be non-negative", name, value)
		}
		return n, nil
	}
	n, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return 0, fmt.Errorf("%s %q: %w", name, value, err)
	}
	if n < 0 || math.Trunc(n) != n {
		return 0, fmt.Errorf("%s %q must be a non-negative integer", name, value)
	}
	maxInt := int(^uint(0) >> 1)
	if n > float64(maxInt) {
		return 0, fmt.Errorf("%s %q exceeds maximum integer size", name, value)
	}
	return int(n), nil
}

func parseMetaFloatColumn(record []string, cols map[string]int, name string) (float64, error) {
	value, err := metaColumnValue(record, cols, name)
	if err != nil {
		return 0, err
	}
	n, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return 0, fmt.Errorf("%s %q: %w", name, value, err)
	}
	return n, nil
}

func parseMetaChildrenSet(value string) ([]string, error) {
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

func (r metaParentRow) observationWeight() int {
	if r.numCalls > 0 {
		return r.numCalls
	}
	return 1
}

func newMetaNameMapper() *metaNameMapper {
	return &metaNameMapper{
		names: make(map[string]string),
		attrs: make(map[string]map[string]string),
	}
}

func (m *metaNameMapper) serviceName(ingressID string) string {
	if name, ok := m.names[ingressID]; ok {
		return name
	}
	name := metaServiceName(ingressID)
	m.names[ingressID] = name
	m.attrs[name] = map[string]string{
		metaIngressIDAttribute: ingressID,
	}
	return name
}

func (m *metaNameMapper) serviceAttributes() map[string]map[string]string {
	return m.attrs
}

func recordMetaInvocation(collector *StatsCollector, names *metaNameMapper, row metaParentRow, weight int) {
	parentService := names.serviceName(row.parentName)
	parentStats := collector.getService(parentService)
	parentOp := collector.getOp(parentStats, metaOperationName)
	// Meta summary rows have no latency, so these are nominal placeholders.
	parentOp.RecordDuration(metaParentDuration+time.Duration(len(row.children))*metaChildDuration, weight)
	parentOp.TotalCount += weight
	if row.numCalls > row.numReturningCalls {
		parentOp.ErrorCount += row.numCalls - row.numReturningCalls
	}

	if len(row.children) >= 2 {
		vote := collector.getCallStyle(parentStats, metaOperationName)
		if row.concurrencyRate > 0 {
			vote.Parallel += weight
		} else {
			vote.Sequential += weight
		}
	}

	for _, child := range row.children {
		childService := names.serviceName(child)
		childRef := childService + "." + metaOperationName
		if parentOp.Calls == nil {
			parentOp.Calls = make(map[string]*CallStats)
		}
		call := parentOp.Calls[childRef]
		if call == nil {
			call = &CallStats{}
			parentOp.Calls[childRef] = call
		}
		call.Count += weight

		childOp := collector.getOp(collector.getService(childService), metaOperationName)
		childOp.RecordDuration(metaChildDuration, weight)
		childOp.TotalCount += weight
	}
}

func metaServiceName(ingressID string) string {
	digest := sha256.Sum256([]byte(ingressID))
	return metaServicePrefix + metaServiceSlug(ingressID) + "-" + hex.EncodeToString(digest[:metaNameHashBytes])
}

func metaServiceSlug(ingressID string) string {
	var b strings.Builder
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
	slug := strings.Trim(b.String(), "-")
	if slug == "" {
		return metaFallbackServiceSlug
	}
	return slug
}
