# Synthetic Topologies from DGG

*2026-02-25T16:29:43Z by Showboat 0.6.1*
<!-- showboat-id: e50c5b17-a645-447c-8167-70e6a325fc5b -->

Du et al. ("A Microservice Graph Generator with Production Characteristics", ICS 2025) built DGG, a generator that produces synthetic service dependency graphs matching production characteristics from Alibaba traces. The `dgg2motel` tool converts DGG's JSON output into motel topology YAML, enabling bulk testing of motel against a wide range of graph shapes.

This demo walks through converting a DGG call graph, inspecting the output, and running motel against the converted topology.

## A DGG call graph

DGG outputs call graphs as JSON. Each graph has nodes (microservices with type labels) and edges (calls between services with protocol and multiplicity). Here is a sample graph from DGG's Alibaba trace corpus — a normal service that fans out to a memcached cache, a blackhole leaf, and a function that itself calls another cache.

```bash
cat > /tmp/dgg-sample.json << 'EOF'
{
    "nodes": [
        {"node": "USER", "label": "relay"},
        {"node": "MS_normal+2.1", "label": "normal"},
        {"node": "MS_Memcached.2", "label": "Memcached"},
        {"node": "MS_blackhole.1_func1", "label": "blackhole"},
        {"node": "MS_normal+2.1_func2", "label": "normal"},
        {"node": "MS_Memcached.1", "label": "Memcached"}
    ],
    "edges": [
        {"rpcid": "0", "um": "USER", "dm": "MS_normal+2.1", "time": 1, "compara": "http"},
        {"rpcid": "0.1", "um": "MS_normal+2.1", "dm": "MS_Memcached.2", "time": 1, "compara": "mc"},
        {"rpcid": "0.2", "um": "MS_normal+2.1", "dm": "MS_blackhole.1_func1", "time": 1, "compara": "rpc"},
        {"rpcid": "0.3", "um": "MS_normal+2.1", "dm": "MS_normal+2.1_func2", "time": 1, "compara": "rpc"},
        {"rpcid": "0.3.1", "um": "MS_normal+2.1_func2", "dm": "MS_Memcached.1", "time": 2, "compara": "mc"}
    ],
    "num": 82
}
EOF
echo 'wrote /tmp/dgg-sample.json'

```

```output
wrote /tmp/dgg-sample.json
```

The `USER` node is the entry point — DGG always starts call graphs from `USER`. Node names encode the service type and instance: `MS_normal+2.1` is a "normal" microservice, `MS_Memcached.2` is a memcached instance. The `_func2` suffix indicates a function within a service — DGG models services with multiple callable interfaces.

The `rpcid` field is a hierarchical trace identifier: `0.3.1` means the first child of the third child of the root call. The `compara` field is the communication protocol (http, rpc, mc for memcached). The `time` field is a call multiplicity — `time: 2` on the edge from `MS_normal+2.1_func2` to `MS_Memcached.1` means two calls per invocation.

## Converting to motel topology

The `dgg2motel` converter maps DGG's graph structure to motel's topology format. Nodes with a `_funcN` suffix become operations within their parent service; nodes without a suffix get a `handle` operation. Edge multiplicities become `count` on calls. Service type labels determine synthetic duration defaults: memcached gets 1ms, blackhole 5ms, relay 10ms, normal 20ms — all with proportional variance.

```bash
go run ./tools/dgg2motel -file /tmp/dgg-sample.json
```

```output
version: 1

services:
  normal-2-1:
    operations:
      func2:
        duration: 20ms +/- 10ms
        calls:
          - target: memcached-1.handle
            count: 2
      handle:
        duration: 20ms +/- 10ms
        calls:
          - memcached-2.handle
          - blackhole-1.func1
          - normal-2-1.func2
  memcached-2:
    operations:
      handle:
        duration: 1ms +/- 500us
  blackhole-1:
    operations:
      func1:
        duration: 5ms +/- 2ms
  memcached-1:
    operations:
      handle:
        duration: 1ms +/- 500us

traffic:
  rate: 10/s
```

The converter grouped `MS_normal+2.1` and `MS_normal+2.1_func2` into a single `normal-2-1` service with two operations: `handle` (the base) and `func2`. The `time: 2` edge became `count: 2` on the call from `func2` to `memcached-1.handle`. The `USER` node was dropped — motel auto-detects root operations (those with no inbound calls).

## Checking the converted topology

```bash
go run ./tools/dgg2motel -file /tmp/dgg-sample.json > /tmp/dgg-topo.yaml && build/motel check /tmp/dgg-topo.yaml
```

```output
PASS  max-depth: 2 (limit: 10)
      path: normal-2-1.handle → normal-2-1.func2 → memcached-1.handle
      p50: 2  p95: 2  p99: 2  max: 2  (1000 samples)
PASS  max-fan-out: 3 (limit: 100)
      worst: normal-2-1.handle
      p50: 3  p95: 3  p99: 3  max: 3  (1000 samples)
PASS  max-spans: 6 static worst-case, 6 observed/1000 samples (limit: 10000)
      p50: 6  p95: 6  p99: 6  max: 6  (1000 samples)
```

The longest path is `normal-2-1.handle → normal-2-1.func2 → memcached-1.handle` at depth 2. Fan-out of 3 is at the root handle operation, which calls memcached-2, blackhole-1, and its own func2. Total spans per trace is 6: the root handle (1) + memcached-2 (1) + blackhole-1 func1 (1) + func2 (1) + 2x memcached-1 (2). No variance in the percentiles because this topology has no probabilistic calls or retries.

## Running traces

```bash
build/motel run --stdout --duration 2s /tmp/dgg-topo.yaml 2>&1 > /dev/null | jq '{traces, spans, errors, spans_bounded}'
```

```output
{
  "traces": 20,
  "spans": 120,
  "errors": 0,
  "spans_bounded": 0
}
```

20 traces, 120 spans — exactly 6 spans per trace as motel check predicted. No errors because no error rate was set. The converter produces clean topologies that motel runs without issue.

## Bulk conversion

DGG's sample corpus contains 111 call graphs across 6 type clusters, derived from Alibaba production traces. The `-dir` flag converts an entire directory tree at once.

```bash
go run ./tools/dgg2motel -dir /tmp/dgg-samples/DGG_gen_cgs -out /tmp/dgg-topologies
```

```output
converted 111 graphs, skipped 0
```

## Testing the corpus

The included `test_corpus.sh` script runs `motel check` and `motel run` on every topology in a directory. It reports failures and timeouts separately.

```bash
./tools/dgg2motel/test_corpus.sh /tmp/dgg-topologies
```

```output
testing 111 topologies from /tmp/dgg-topologies
motel: build/motel
duration: 1s


=== results ===
check: 111 pass, 0 fail
run:   111 pass, 0 fail, 0 timeout
```

## Stress testing at higher rates

The `RATE` environment variable overrides the traffic rate in all topologies, enabling stress testing. At 1000 traces/s, the corpus exercises motel's engine across a range of graph shapes at production-like throughput.

```bash
RATE="1000/s" DURATION="2s" ./tools/dgg2motel/test_corpus.sh /tmp/dgg-topologies
```

```output
testing 111 topologies from /tmp/dgg-topologies
motel: build/motel
duration: 2s


=== results ===
check: 111 pass, 0 fail
run:   111 pass, 0 fail, 0 timeout
```

## Corpus shape distribution

```bash
for f in /tmp/dgg-topologies/20250109_150211/*/*.yaml; do
    build/motel check --samples 0 "$f" 2>/dev/null
done | awk '
/max-depth:/   { d=$3+0; depths[d]++ }
/max-fan-out:/ { f=$3+0; fans[f]++ }
/max-spans:/   { s=$3+0; spans[s]++ }
END {
    printf "depth:    "; for(i=0;i<=10;i++) if(depths[i]) printf "%dx%d  ", depths[i], i; print ""
    printf "fan-out:  "; for(i=0;i<=10;i++) if(fans[i]) printf "%dx%d  ", fans[i], i; print ""
    printf "spans:    "; for(i=0;i<=20;i++) if(spans[i]) printf "%dx%d  ", spans[i], i; print ""
}'

```

```output
depth:    20x0  42x1  45x2  2x3  2x4  
fan-out:  20x0  23x1  21x2  13x3  16x4  16x5  1x7  1x8  
spans:    20x1  14x2  19x3  11x4  11x5  9x6  14x7  7x8  3x9  1x10  2x11  
```

The format is `countxvalue` — for example, `45x2` means 45 topologies have max depth 2. The corpus covers depths 0-4, fan-outs 0-8, and span counts 1-11. The 20 topologies at depth 0 and fan-out 0 are single-service graphs (type5 in DGG's clustering). The heaviest topologies have 11 spans per trace.

These are small by production standards — Alibaba's traces in the Du et al. paper reach depths of 15+ and hundreds of spans. The sample corpus represents clustered archetypes rather than the full distribution. Running the DGG generator itself with different parameters would produce deeper and wider graphs for more aggressive stress testing.

