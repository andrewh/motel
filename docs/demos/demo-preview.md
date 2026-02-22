# motel: Preview Traffic

*2026-02-22T09:00:00Z*

motel preview renders the effective traffic rate over time as an SVG chart. This is useful for verifying bursty patterns, scenario overrides, and ramp-up shapes before sending traffic to a collector.

## Basic preview

Generate an SVG from the stress-test example topology. The chart shows the rate curve with scenario windows shaded.

```bash
motel preview --duration 3m docs/examples/stress-test.yaml -o /tmp/preview.svg
file /tmp/preview.svg | grep -c 'SVG'
```

```output
1
```

## Inferred duration

Without `--duration`, motel infers a preview window from the topology's scenarios. It uses the latest scenario end time plus a 10% buffer.

```bash
motel preview docs/examples/stress-test.yaml -o /tmp/inferred.svg
head -1 /tmp/inferred.svg | grep -c '<svg'
```

```output
1
```

## Preview to stdout

With no `-o` flag, the SVG goes to stdout. Pipe it to a file or viewer.

```bash
motel preview --duration 1m docs/examples/traffic-patterns.yaml | head -1 | grep -c '<svg'
```

```output
1
```

## Uniform traffic

A flat rate produces a horizontal line across the chart.

```bash
cat > /tmp/uniform-preview.yaml << 'EOF'
version: 1
services:
  svc:
    operations:
      op:
        duration: 10ms
traffic:
  rate: 100/s
  pattern: uniform
EOF
motel preview --duration 30s /tmp/uniform-preview.yaml | grep -o 'polyline' | head -1
```

```output
polyline
```

## Bursty traffic with scenarios

The stress-test topology combines a bursty base pattern with scenario overrides. The preview chart shows both the burst spikes and the scenario windows.

```bash
motel preview --duration 3m docs/examples/stress-test.yaml | grep 'class="scenario-rect"' | wc -l | tr -d ' '
```

```output
2
```

Two shaded rectangles appear — one for each scenario defined in the topology (sustained peak and extreme burst).

## SVG structure

The output is a self-contained SVG with inline styles, no external fonts or dependencies. It renders on GitHub, in browsers, and in most image viewers.

```bash
motel preview --duration 1m docs/examples/stress-test.yaml | grep -c 'font-family'
```

```output
1
```
