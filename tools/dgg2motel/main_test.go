package main

import (
	"strings"
	"testing"
)

func TestCleanName(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"MS_normal+2.1", "normal-2-1"},
		{"MS_Memcached.1", "memcached-1"},
		{"MS_blackhole.1", "blackhole-1"},
		{"MS_relay+2.1", "relay-2-1"},
		{"MS_", "unknown"},
		{"", "unknown"},
		{"MS___", "unknown"},
		{"MS_a++b..c", "a-b-c"},
		{"simple", "simple"},
	}
	for _, tt := range tests {
		got := cleanName(tt.input)
		if got != tt.want {
			t.Errorf("cleanName(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestDurationForLabel(t *testing.T) {
	tests := []struct {
		label string
		want  string
	}{
		{"memcached", "1ms +/- 500us"},
		{"Memcached", "1ms +/- 500us"},
		{"blackhole", "5ms +/- 2ms"},
		{"relay", "10ms +/- 5ms"},
		{"normal", "20ms +/- 10ms"},
		{"anything-else", "20ms +/- 10ms"},
	}
	for _, tt := range tests {
		got := durationForLabel(tt.label)
		if got != tt.want {
			t.Errorf("durationForLabel(%q) = %q, want %q", tt.label, got, tt.want)
		}
	}
}

func TestConvertOne(t *testing.T) {
	input := `{
		"nodes": [
			{"node": "USER", "label": "relay"},
			{"node": "MS_normal+2.1", "label": "normal"},
			{"node": "MS_Memcached.1", "label": "Memcached"}
		],
		"edges": [
			{"rpcid": "0", "um": "USER", "dm": "MS_normal+2.1", "time": 1, "compara": "http"},
			{"rpcid": "0.1", "um": "MS_normal+2.1", "dm": "MS_Memcached.1", "time": 1, "compara": "mc"}
		],
		"num": 100
	}`

	got, err := convertOne([]byte(input))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "version: 1") {
		t.Error("missing version header")
	}
	if !strings.Contains(got, "normal-2-1:") {
		t.Error("missing service normal-2-1")
	}
	if !strings.Contains(got, "memcached-1.handle") {
		t.Error("missing call to memcached-1.handle")
	}
	if !strings.Contains(got, "rate: 10/s") {
		t.Error("missing traffic rate")
	}
}

func TestConvertOneFuncSuffix(t *testing.T) {
	input := `{
		"nodes": [
			{"node": "USER", "label": "relay"},
			{"node": "MS_normal+2.1", "label": "normal"},
			{"node": "MS_normal+2.1_func2", "label": "normal"}
		],
		"edges": [
			{"rpcid": "0", "um": "USER", "dm": "MS_normal+2.1", "time": 1, "compara": "http"},
			{"rpcid": "0.1", "um": "MS_normal+2.1", "dm": "MS_normal+2.1_func2", "time": 1, "compara": "rpc"}
		],
		"num": 50
	}`

	got, err := convertOne([]byte(input))
	if err != nil {
		t.Fatal(err)
	}
	// func2 should be an operation under normal-2-1, not a separate service.
	if strings.Count(got, "normal-2-1:") != 1 {
		t.Error("expected exactly one normal-2-1 service")
	}
	if !strings.Contains(got, "func2:") {
		t.Error("missing func2 operation")
	}
	if !strings.Contains(got, "handle:") {
		t.Error("missing handle operation")
	}
	if !strings.Contains(got, "normal-2-1.func2") {
		t.Error("missing call to normal-2-1.func2")
	}
}

func TestConvertOneCallCount(t *testing.T) {
	input := `{
		"nodes": [
			{"node": "USER", "label": "relay"},
			{"node": "MS_normal+2.1", "label": "normal"},
			{"node": "MS_Memcached.1", "label": "Memcached"}
		],
		"edges": [
			{"rpcid": "0", "um": "USER", "dm": "MS_normal+2.1", "time": 1, "compara": "http"},
			{"rpcid": "0.1", "um": "MS_normal+2.1", "dm": "MS_Memcached.1", "time": 3, "compara": "mc"}
		],
		"num": 10
	}`

	got, err := convertOne([]byte(input))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "count: 3") {
		t.Error("expected count: 3 for edge with time=3")
	}
}

func TestConvertOneDanglingEdge(t *testing.T) {
	input := `{
		"nodes": [
			{"node": "USER", "label": "relay"},
			{"node": "MS_normal+2.1", "label": "normal"}
		],
		"edges": [
			{"rpcid": "0", "um": "USER", "dm": "MS_normal+2.1", "time": 1, "compara": "http"},
			{"rpcid": "0.1", "um": "MS_normal+2.1", "dm": "MS_ghost.1", "time": 1, "compara": "rpc"}
		],
		"num": 1
	}`

	got, err := convertOne([]byte(input))
	if err != nil {
		t.Fatal(err)
	}
	// Dangling edge should be silently skipped, not panic.
	if strings.Contains(got, "ghost") {
		t.Error("dangling edge target should not appear in output")
	}
}

func TestConvertOneEmptyGraph(t *testing.T) {
	input := `{"nodes": [], "edges": [], "num": 0}`
	_, err := convertOne([]byte(input))
	if err == nil {
		t.Error("expected error for empty graph")
	}
}

func TestConvertOneNameCollision(t *testing.T) {
	input := `{
		"nodes": [
			{"node": "USER", "label": "relay"},
			{"node": "MS_a+b", "label": "normal"},
			{"node": "MS_a.b", "label": "normal"}
		],
		"edges": [
			{"rpcid": "0", "um": "USER", "dm": "MS_a+b", "time": 1, "compara": "http"}
		],
		"num": 1
	}`

	_, err := convertOne([]byte(input))
	if err == nil {
		t.Error("expected error for name collision")
	}
	if !strings.Contains(err.Error(), "collision") {
		t.Errorf("expected collision error, got: %v", err)
	}
}
