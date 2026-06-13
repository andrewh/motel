# motel: Swarm Testing for Topology Exploration

*2026-06-13T14:58:55Z by Showboat 0.6.1*
<!-- showboat-id: a0eb5103-def7-4a22-b32f-c4e30bc518c8 -->

Swarm testing is an opt-in sampling strategy for motel check. The default random sampler follows the probabilities in the topology, which is useful for empirical percentiles. The swarm sampler fixes subsets of probabilistic choices to their extremes so a small number of samples can exercise rare combinations, retry paths, and error-conditioned branches.

## Rare fan-out choices

This topology has one root operation and five optional backend calls. Each call is configured with an extremely small probability, so a single random sample is expected to miss all of them. Static analysis still sees the structural upper bound because all five calls could fire together.

```bash
cat > /tmp/swarm-rare-fanout.yaml << 'EOF'
version: 1
services:
  gateway:
    operations:
      request:
        duration: 10ms
        calls:
          - target: backend.one
            probability: 0.000000000000001
          - target: backend.two
            probability: 0.000000000000001
          - target: backend.three
            probability: 0.000000000000001
          - target: backend.four
            probability: 0.000000000000001
          - target: backend.five
            probability: 0.000000000000001
  backend:
    operations:
      one:
        duration: 5ms
      two:
        duration: 5ms
      three:
        duration: 5ms
      four:
        duration: 5ms
      five:
        duration: 5ms
traffic:
  rate: 10/s
EOF
echo 'wrote /tmp/swarm-rare-fanout.yaml'
```

```output
wrote /tmp/swarm-rare-fanout.yaml
```

```bash
build/motel check --samples 1 --seed 42 --sample-strategy random /tmp/swarm-rare-fanout.yaml
```

```output
PASS  max-depth: 1 (limit: 10)
      path: gateway.request → backend.one
      p50: 0  p95: 0  p99: 0  max: 0  (1 samples)
PASS  max-fan-out: 5 (limit: 100)
      worst: gateway.request
      p50: 0  p95: 0  p99: 0  max: 0  (1 samples)
PASS  max-spans: 6 static worst-case, 1 observed/1 samples (limit: 10000)
      p50: 1  p95: 1  p99: 1  max: 1  (1 samples)
```

```bash
build/motel check --samples 1 --seed 42 --sample-strategy swarm /tmp/swarm-rare-fanout.yaml
```

```output
PASS  max-depth: 1 (limit: 10)
      path: gateway.request → backend.one
      p50: 1  p95: 1  p99: 1  max: 1  (1 samples)
PASS  max-fan-out: 5 (limit: 100)
      worst: gateway.request
      p50: 5  p95: 5  p99: 5  max: 5  (1 samples)
PASS  max-spans: 6 static worst-case, 6 observed/1 samples (limit: 10000)
      p50: 6  p95: 6  p99: 6  max: 6  (1 samples)
```

With the same seed and sample count, swarm reaches the structural corner case immediately: all five rare calls fire in the first partition. The static max-spans value does not change; only the sampled observation changes from 1 span to 6 spans. This is the main reason to use swarm when checking whether a topology has hidden fan-out or span-count cliffs.

## Retry path activation

Retries are also choice points for swarm exploration. In a normal sampled trace, retries only appear when a child attempt fails or times out. Swarm can force the retry control flow so the sampled trace includes the extra attempt spans even when the child operation itself would otherwise succeed.

```bash
cat > /tmp/swarm-retries.yaml << 'EOF'
version: 1
services:
  gateway:
    operations:
      request:
        duration: 10ms
        calls:
          - target: worker.step
            retries: 2
            retry_backoff: 1ms
  worker:
    operations:
      step:
        duration: 5ms
traffic:
  rate: 10/s
EOF
echo 'wrote /tmp/swarm-retries.yaml'
```

```output
wrote /tmp/swarm-retries.yaml
```

```bash
build/motel check --samples 1 --seed 42 --sample-strategy random /tmp/swarm-retries.yaml
```

```output
PASS  max-depth: 1 (limit: 10)
      path: gateway.request → worker.step
      p50: 1  p95: 1  p99: 1  max: 1  (1 samples)
PASS  max-fan-out: 3 (limit: 100)
      worst: gateway.request
      p50: 1  p95: 1  p99: 1  max: 1  (1 samples)
PASS  max-spans: 4 static worst-case, 2 observed/1 samples (limit: 10000)
      p50: 2  p95: 2  p99: 2  max: 2  (1 samples)
```

```bash
build/motel check --samples 1 --seed 42 --sample-strategy swarm /tmp/swarm-retries.yaml
```

```output
PASS  max-depth: 1 (limit: 10)
      path: gateway.request → worker.step
      p50: 1  p95: 1  p99: 1  max: 1  (1 samples)
PASS  max-fan-out: 3 (limit: 100)
      worst: gateway.request
      p50: 3  p95: 3  p99: 3  max: 3  (1 samples)
PASS  max-spans: 4 static worst-case, 4 observed/1 samples (limit: 10000)
      p50: 4  p95: 4  p99: 4  max: 4  (1 samples)
```

The retry topology has a static max-spans value of 4: one gateway span plus three worker attempts. Random sampling observes two spans because the first attempt succeeds and no retry is needed. Swarm forces the retry activation choice, so the observed fan-out and span count match the structural retry path with one sample.

## Choosing a strategy

Use random sampling when percentile checks should reflect the topology's configured probabilities. Use swarm sampling when you want to stress the structural shape of the topology and quickly expose rare combinations. Swarm percentile lines describe the partitions explored by the strategy, not production-frequency percentiles.
