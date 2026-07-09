# Exploring Workiva/go-datastructures for motel

[Workiva/go-datastructures](https://github.com/Workiva/go-datastructures) is
an Apache-2.0 collection of 21 general-purpose data structure packages
(tries, heaps, trees, tries, lock-free queues, persistent collections). This
note surveys it against motel's actual code paths to see whether any
structure earns its place as a dependency, rather than adding it
speculatively.

`go.mod` currently pulls in only what OTel export, YAML parsing, and testing
require — no general-purpose data structure library. That's a deliberate bar
to clear: stdlib `map`/`slice`/`container/heap` cover most needs, and every
added dependency is something `make lint`/security review/Go module
maintenance has to account for indefinitely.

## What the library provides

| Package                       | Summary                                                          |
| ------------------------------ | ---------------------------------------------------------------- |
| Augmented Tree                 | Red-black tree indexing n-dimensional intervals for range/overlap queries |
| Bitarray                       | Sparse/dense uint64 membership sets                               |
| Futures                        | Broadcast a value to many listeners without channel fan-out races |
| Queue                          | Non-blocking FIFO/priority queues, CAS-based MPMC ring buffer     |
| Fibonacci Heap                 | O(1) insert/merge, cheap decrease-key                             |
| Range Tree                     | N-dimensional sorted point list for range membership              |
| Set                            | Generic `interface{}` set                                        |
| Threadsafe                     | Mutex-wrapped common collections                                  |
| AVL Tree                       | Branch-copy immutable BBST (read-heavy, serialized writes)        |
| X-Fast / Y-Fast Trie           | Integer predecessor/successor queries                             |
| Fast Integer Hashmap           | Linear-probing map for small/medium int keys                      |
| Skiplist                       | Ordered structure, no rotations                                   |
| Sort                           | Multithreaded bucket sort                                         |
| Numerics                       | Nonlinear constrained optimization                                |
| B+ Tree / Immutable B Tree     | Cache-local / bulk-optimized ordered trees                        |
| Ctrie / Dtrie                  | Lock-free / persistent hash tries with O(1) snapshots             |
| Persistent List                | Immutable cons-list                                                |
| Simple Graph                   | Undirected graph, O(1) edge/vertex ops, no parallel edges          |

## Where it plausibly fits motel

### Interval index for scenario activation (Augmented Tree)

`ActiveScenarios` (`pkg/synth/scenario.go:264`) linearly scans every
`Scenario` on **every simulation iteration** — once per generated trace in
batch mode (`engine.go:154`), once per admitted trace in realtime mode
(`engine.go:299`) — checking `elapsed >= Start && elapsed < End`. That's
exactly the interval-stabbing query the Augmented Tree computes in
`O(log n + k)` instead of `O(n)`.

It's a real hot path, but the win doesn't materialize at realistic scale.
Topologies define scenarios as a handful of named time windows (see
`docs/examples/`), not hundreds of overlapping ones — two integer
comparisons per scenario is noise next to the span/attribute/exporter work
already happening in the same iteration. This becomes worth doing only if
motel grows a use case with dozens-to-hundreds of concurrently-defined
scenarios; there's no such use case today. Worth remembering, not adopting
speculatively — and if it ever is worth doing, a ~20-line hand-rolled
interval index (sorted-by-start slice + binary search, since scenario sets
are static once built) would avoid pulling in the whole dependency for one
query shape.

### Discrete-event scheduling (Fibonacci Heap / priority queue)

The engine has no central "what happens next" event queue. Batch mode
(`Engine.Run`, `engine.go:117`) walks one trace to completion per loop
iteration; realtime mode (`Engine.runRealtime`, `engine.go:260`) spawns one
goroutine per in-flight trace behind a semaphore
(`engine.go:265`–`384`). Concurrency is Go's goroutine scheduler, not a
simulation-level event queue.

If motel ever needs genuine discrete-event semantics — e.g. modelling
resource contention or queueing delay across many concurrently in-flight
traces in a single-threaded, deterministic event loop rather than
goroutine-per-trace — a timestamp-ordered priority queue is the standard
DES structure (pop earliest event, process it, push follow-on events). The
Fibonacci Heap's O(1) insert/merge and cheap decrease-key would fit that
role. This is speculative in the same way: it only pays off if the engine's
concurrency model changes from goroutine-per-trace to a single event loop,
which isn't planned. `SimulationState` (`pkg/synth/state.go`) already
tracks queue depth, backpressure, and circuit breakers per-operation without
needing this — the current model gets cross-trace effects without a global
event queue.

## Where it doesn't fit

- **Skiplist, AVL/B+/Ctrie/Dtrie, X/Y-Fast Trie, Range Tree** — motel has no
  code path doing repeated ordered inserts/deletes, versioned snapshots, or
  multi-dimensional range queries. `Topology` and `Scenario` are built once
  per run and read-only afterward (`BuildTopology`, `BuildScenarios`); Go
  maps and slices already cover every lookup.
- **Set, Fast Integer Hashmap, Bitarray** — every dedup/membership check in
  the codebase (`spanContextRegistry.targets`, `os.FailureWindow` pruning,
  cycle-detection `visited` sets in `check.go` and `scenario.go`) is small
  and already a plain Go map. Swapping in a specialized structure wouldn't
  change algorithmic complexity, only add an import.
- **Simple Graph** — `validateScenarioCycles` (`scenario.go:184`) and the
  worst-case DFS in `check.go` build an adjacency map and do cycle
  detection by hand in ~20 lines each. The library's graph type has no
  built-in cycle detection and doesn't model directed edges as motel's call
  graph needs, so it wouldn't actually replace this code.
- **Numerics** — `traceimport`'s stats collector (`pkg/synth/traceimport/stats.go`)
  fits distributions from observed percentiles directly, not via nonlinear
  optimization; there's no curve-fitting problem to hand off.
- **Futures, threadsafe queue/ring buffer** — motel's concurrency (worker
  semaphore + `sync.WaitGroup` in `runRealtime`, `sync.RWMutex` in
  `spanContextRegistry`) is small and already idiomatic Go; these packages
  solve broadcast/backpressure problems motel doesn't have.
- **Sort** — `traceimport` sorts small in-memory slices (span lists, root
  timestamps) with `sort.Slice`; multithreaded bucket sort's crossover point
  is far above motel's data volumes.
- **Persistent List** — no code path builds up an immutable list
  incrementally; motel doesn't have a hot-topology-reload feature that would
  want copy-on-write snapshots (no `--watch`/reload flag exists today).

## Recommendation

Don't add the dependency. Both plausible fits (interval index, DES priority
queue) are real techniques for real problem shapes, but neither problem
exists in motel at a scale where the stdlib approach is inadequate today.
If scenario counts or engine concurrency model change enough to matter,
revisit the two sections above — they're the two specific triggers worth
watching for, not a general "add this library" decision.
