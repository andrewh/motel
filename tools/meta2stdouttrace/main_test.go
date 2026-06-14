package main

import (
	"compress/gzip"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

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

func TestInvocationEvents(t *testing.T) {
	start := time.Date(2022, 12, 21, 0, 0, 0, 0, time.UTC)
	row := parentRow{
		parentName:        "10382-00001",
		children:          []string{"AAJB", "ABAC"},
		numCalls:          "5",
		numReturningCalls: "4",
		concurrencyRate:   "0.5",
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
}

func TestRunConvertsParentData(t *testing.T) {
	dir := t.TempDir()
	input := filepath.Join(dir, "parent-data.csv.gz")
	f, err := os.Create(input)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(f)
	_, err = gz.Write([]byte(strings.Join([]string{
		"parent_name,children_set,c_set_index,num_calls,num_returning_calls,concurrency_rate,profile",
		"00001-00001,set(),1,0,0,0.0,ads",
		"10382-00001,\"{'AAJB', 'ABAC'}\",2,5,4,0.5,ads",
		"20400-00001,{'AACD'},1,1,1,0.0,fetch",
		"",
	}, "\n")))
	if err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	var out strings.Builder
	err = run(&out, options{
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
