package main

import (
	"compress/gzip"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/andrewh/motel/pkg/synth/traceimport"
)

func writeGzipCSV(t *testing.T, lines []string) string {
	t.Helper()

	dir := t.TempDir()
	input := filepath.Join(dir, "parent-data.csv.gz")
	f, err := os.Create(input)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(f)
	_, err = gz.Write([]byte(strings.Join(lines, "\n")))
	if err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return input
}

func TestParseChildrenSet(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  []string
	}{
		{name: "empty set", input: "set()", want: nil},
		{name: "single child", input: "{'AACD'}", want: []string{"AACD"}},
		{name: "sorted children", input: "{'AAJB', 'ABAC'}", want: []string{"AAJB", "ABAC"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseChildrenSet(tt.input)
			if err != nil {
				t.Fatalf("parseChildrenSet() error = %v", err)
			}
			if strings.Join(got, ",") != strings.Join(tt.want, ",") {
				t.Fatalf("parseChildrenSet() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestParseChildrenSetRejectsUnexpectedFormat(t *testing.T) {
	if _, err := parseChildrenSet("['AACD']"); err == nil {
		t.Fatal("parseChildrenSet() error = nil, want error")
	}
}

func TestParseParentRowParsesNumericFields(t *testing.T) {
	header := []string{
		columnParentName,
		columnChildrenSet,
		columnNumCalls,
		columnNumReturningCalls,
		columnConcurrencyRate,
		columnProfile,
	}
	record := []string{"10382-00001", "{'AAJB'}", "5", "4", "0.5", "ads"}

	row, err := parseParentRow(record, indexHeader(header))
	if err != nil {
		t.Fatalf("parseParentRow() error = %v", err)
	}
	if row.numCalls != 5 {
		t.Fatalf("numCalls = %d, want 5", row.numCalls)
	}
	if row.numReturningCalls != 4 {
		t.Fatalf("numReturningCalls = %d, want 4", row.numReturningCalls)
	}
	if row.concurrencyRate != 0.5 {
		t.Fatalf("concurrencyRate = %v, want 0.5", row.concurrencyRate)
	}
}

func TestParseParentRowRejectsInvalidNumericField(t *testing.T) {
	header := []string{
		columnParentName,
		columnChildrenSet,
		columnNumCalls,
		columnNumReturningCalls,
		columnConcurrencyRate,
		columnProfile,
	}
	record := []string{"10382-00001", "{'AAJB'}", "five", "4", "0.5", "ads"}

	_, err := parseParentRow(record, indexHeader(header))
	if err == nil {
		t.Fatal("parseParentRow() error = nil, want error")
	}
	if !strings.Contains(err.Error(), columnNumCalls) {
		t.Fatalf("parseParentRow() error = %v, want %q", err, columnNumCalls)
	}
}

func TestInvocationEvents(t *testing.T) {
	start := time.Date(2022, 12, 21, 0, 0, 0, 0, time.UTC)
	row := parentRow{
		parentName:        "10382-00001",
		children:          []string{"AAJB", "ABAC"},
		numCalls:          5,
		numReturningCalls: 4,
		concurrencyRate:   0.5,
		profile:           "ads",
	}

	events := invocationEvents(row, 0, start)
	if len(events) != 3 {
		t.Fatalf("len(events) = %d, want 3", len(events))
	}
	if events[0].Resource[0].Value.Value != "meta-10382-00001" {
		t.Fatalf("root service = %v, want meta-10382-00001", events[0].Resource[0].Value.Value)
	}
	if events[1].Parent.SpanID != events[0].SpanContext.SpanID {
		t.Fatalf("child parent span = %q, want %q", events[1].Parent.SpanID, events[0].SpanContext.SpanID)
	}
	if !events[1].StartTime.Equal(events[2].StartTime) {
		t.Fatalf("concurrent children start at %s and %s", events[1].StartTime, events[2].StartTime)
	}
	if events[0].Attributes[2].Value.Value != "5" {
		t.Fatalf("num_calls attribute = %v, want 5", events[0].Attributes[2].Value.Value)
	}
}

func TestInvocationEventsRootParentImportsAsEmpty(t *testing.T) {
	start := time.Date(2022, 12, 21, 0, 0, 0, 0, time.UTC)
	events := invocationEvents(parentRow{
		parentName:        "10382-00001",
		numCalls:          5,
		numReturningCalls: 4,
		concurrencyRate:   0,
		profile:           "ads",
	}, 0, start)

	var out strings.Builder
	if err := json.NewEncoder(&out).Encode(events[0]); err != nil {
		t.Fatalf("encode event: %v", err)
	}
	spans, err := traceimport.ParseSpans(strings.NewReader(out.String()), traceimport.FormatStdouttrace)
	if err != nil {
		t.Fatalf("ParseSpans() error = %v", err)
	}
	if len(spans) != 1 {
		t.Fatalf("len(spans) = %d, want 1", len(spans))
	}
	if spans[0].ParentID != "" {
		t.Fatalf("root ParentID = %q, want empty", spans[0].ParentID)
	}
}

func TestSpanIDUsesUint64Layout(t *testing.T) {
	tests := []struct {
		traceOrdinal uint64
		childOrdinal uint64
		want         string
	}{
		{traceOrdinal: 1, childOrdinal: 0, want: "0000000100000000"},
		{traceOrdinal: 1, childOrdinal: 1, want: "0000000100000001"},
		{traceOrdinal: 2, childOrdinal: 0, want: "0000000200000000"},
	}

	for _, tt := range tests {
		got := spanID(tt.traceOrdinal, tt.childOrdinal)
		if got != tt.want {
			t.Fatalf("spanID(%d, %d) = %q, want %q", tt.traceOrdinal, tt.childOrdinal, got, tt.want)
		}
	}
}

func TestServiceNameNormalizesIngressID(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{input: "AAJB", want: "meta-aajb"},
		{input: "A..B__", want: "meta-a-b"},
		{input: "10382-00001", want: "meta-10382-00001"},
		{input: "\u03a3 Worker", want: "meta-\u03c3-worker"},
	}

	for _, tt := range tests {
		got := serviceName(tt.input)
		if got != tt.want {
			t.Fatalf("serviceName(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestRunConvertsParentData(t *testing.T) {
	input := writeGzipCSV(t, []string{
		"parent_name,children_set,c_set_index,num_calls,num_returning_calls,concurrency_rate,profile",
		"00001-00001,set(),1,0,0,0.0,ads",
		"10382-00001,\"{'AAJB', 'ABAC'}\",2,5,4,0.5,ads",
		"20400-00001,{'AACD'},1,1,1,0.0,fetch",
		"",
	})

	var out strings.Builder
	err := run(&out, options{
		input:   input,
		limit:   1,
		profile: "ads",
		start:   time.Date(2022, 12, 21, 0, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("run() error = %v", err)
	}

	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("got %d JSON lines, want 3", len(lines))
	}
	var event stdouttraceEvent
	if err := json.Unmarshal([]byte(lines[0]), &event); err != nil {
		t.Fatalf("unmarshal first event: %v", err)
	}
	if event.Attributes[1].Value.Value != "ads" {
		t.Fatalf("profile attribute = %v, want ads", event.Attributes[1].Value.Value)
	}
}

func TestRunIncludesEmptyParents(t *testing.T) {
	input := writeGzipCSV(t, []string{
		"parent_name,children_set,c_set_index,num_calls,num_returning_calls,concurrency_rate,profile",
		"00001-00001,set(),1,0,0,0.0,ads",
		"10382-00001,\"{'AAJB', 'ABAC'}\",2,5,4,0.5,ads",
		"",
	})

	var out strings.Builder
	err := run(&out, options{
		input:        input,
		limit:        1,
		profile:      "ads",
		includeEmpty: true,
		start:        time.Date(2022, 12, 21, 0, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("run() error = %v", err)
	}

	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 1 {
		t.Fatalf("got %d JSON lines, want 1", len(lines))
	}
	var event stdouttraceEvent
	if err := json.Unmarshal([]byte(lines[0]), &event); err != nil {
		t.Fatalf("unmarshal first event: %v", err)
	}
	if event.Resource[0].Value.Value != "meta-00001-00001" {
		t.Fatalf("service.name = %v, want meta-00001-00001", event.Resource[0].Value.Value)
	}
}

func TestRunReportsParseDataRowNumber(t *testing.T) {
	input := writeGzipCSV(t, []string{
		"parent_name,children_set,c_set_index,num_calls,num_returning_calls,concurrency_rate,profile",
		"10382-00001,not-a-set,1,5,4,0.5,ads",
		"",
	})

	err := run(&strings.Builder{}, options{
		input: input,
		limit: 1,
		start: time.Date(2022, 12, 21, 0, 0, 0, 0, time.UTC),
	})
	if err == nil {
		t.Fatal("run() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "parse row 2") {
		t.Fatalf("run() error = %v, want parse row 2", err)
	}
}
