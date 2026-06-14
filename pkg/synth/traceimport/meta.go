package traceimport

import (
	"bufio"
	"compress/gzip"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
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

	metaOperationName  = "invoke"
	metaParentDuration = 5 * time.Millisecond
	metaChildDuration  = time.Millisecond
	gzipMagicByte0     = 0x1f
	gzipMagicByte1     = 0x8b
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
	parentName      string
	children        []string
	concurrencyRate float64
	profile         string
}

func importMetaSummary(r io.Reader, opts Options) ([]byte, error) {
	reader, closeReader, err := metaInputReader(r)
	if err != nil {
		return nil, err
	}
	defer closeReader()

	csvReader := csv.NewReader(reader)
	header, err := csvReader.Read()
	if err != nil {
		return nil, fmt.Errorf("read Meta summary header: %w", err)
	}
	cols := indexMetaHeader(header)
	for _, name := range requiredMetaSummaryColumns {
		if _, ok := cols[name]; !ok {
			return nil, fmt.Errorf("missing required Meta summary column %q", name)
		}
	}

	collector := NewStatsCollector()
	rowCount := 0
	spanCount := 0
	rowNumber := 1

	for {
		rowNumber++
		record, err := csvReader.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read Meta summary row %d: %w", rowNumber, err)
		}
		row, err := parseMetaParentRow(record, cols)
		if err != nil {
			return nil, fmt.Errorf("parse Meta summary row %d: %w", rowNumber, err)
		}
		if opts.MetaProfile != "" && row.profile != opts.MetaProfile {
			continue
		}
		if !opts.MetaIncludeEmpty && len(row.children) == 0 {
			continue
		}

		recordMetaInvocation(collector, row)
		rowCount++
		spanCount += 1 + len(row.children)
	}

	if rowCount == 0 {
		return nil, errors.New("no Meta summary rows matched the requested filters")
	}
	if rowCount < opts.MinTraces {
		_, _ = fmt.Fprintf(opts.Warnings, "warning: only %d Meta parent invocations available (requested minimum: %d); results may be inaccurate\n",
			rowCount, opts.MinTraces)
	}
	if rowCount == 1 {
		_, _ = fmt.Fprintf(opts.Warnings, "warning: only 1 Meta parent invocation available; duration distributions will be exact values. Use more rows for statistical accuracy.\n")
	}
	reportConfidenceDiagnostics(collector, opts.MinTraces, opts.Warnings)

	yamlBytes, err := MarshalConfig(collector, nil, rowCount, spanCount, 0)
	if err != nil {
		return nil, err
	}
	if err := validateRoundTrip(yamlBytes); err != nil {
		return nil, fmt.Errorf("round-trip validation failed (this is a bug): %w", err)
	}
	return yamlBytes, nil
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
	if _, err := parseMetaIntColumn(record, cols, metaColumnNumCalls); err != nil {
		return metaParentRow{}, err
	}
	if _, err := parseMetaIntColumn(record, cols, metaColumnNumReturningCalls); err != nil {
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
		parentName:      parentName,
		children:        children,
		concurrencyRate: concurrencyRate,
		profile:         profile,
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

func parseMetaIntColumn(record []string, cols map[string]int, name string) (int, error) {
	value, err := metaColumnValue(record, cols, name)
	if err != nil {
		return 0, err
	}
	n, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("%s %q: %w", name, value, err)
	}
	return n, nil
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

func recordMetaInvocation(collector *StatsCollector, row metaParentRow) {
	parentService := metaServiceName(row.parentName)
	parentStats := collector.getService(parentService)
	parentOp := collector.getOp(parentStats, metaOperationName)
	parentOp.RecordDuration(metaParentDuration+time.Duration(len(row.children))*metaChildDuration, false)
	parentOp.TotalCount++

	if len(row.children) >= 2 {
		vote := collector.getCallStyle(parentStats, metaOperationName)
		if row.concurrencyRate > 0 {
			vote.Parallel++
		} else {
			vote.Sequential++
		}
	}

	for _, child := range row.children {
		childService := metaServiceName(child)
		childRef := childService + "." + metaOperationName
		if parentOp.Calls == nil {
			parentOp.Calls = make(map[string]*CallStats)
		}
		call := parentOp.Calls[childRef]
		if call == nil {
			call = &CallStats{}
			parentOp.Calls[childRef] = call
		}
		call.Count++

		childOp := collector.getOp(collector.getService(childService), metaOperationName)
		childOp.RecordDuration(metaChildDuration, false)
		childOp.TotalCount++
	}
}

func metaServiceName(ingressID string) string {
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
