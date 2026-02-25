# DeathStarBench Topologies

Motel topologies modelling the microservice applications from
[DeathStarBench](https://github.com/delimitrou/DeathStarBench) (Gan et al.,
"An Open-Source Benchmark Suite for Microservices and Their Hardware-Software
Implications for Cloud & Edge Systems", ASPLOS 2019,
[doi:10.1145/3297858.3304013](https://doi.org/10.1145/3297858.3304013)).

These are non-trivial, independently verifiable architectures widely used in
microservice research. They make good starting points for realistic
benchmarking and experimentation.

## Files

| File | Services | Description |
|------|----------|-------------|
| `social-network.yaml` | 15 | Social network with compose-post fan-out, timeline reads, and data store tiers (MongoDB, Memcached, Redis). Includes a post-storage degradation scenario. |
| `hotel-reservation.yaml` | 12 | Hotel reservation system with parallel search (geo + rate), reviews, attractions, cache-then-store access patterns, and a geo latency spike scenario. |

Service counts include data store services (mongodb, memcached, redis).

## Usage

```sh
motel validate docs/examples/dsb/social-network.yaml
motel check docs/examples/dsb/social-network.yaml
motel run --stdout --duration 5s docs/examples/dsb/social-network.yaml

motel validate docs/examples/dsb/hotel-reservation.yaml
motel check docs/examples/dsb/hotel-reservation.yaml
motel run --stdout --duration 5s docs/examples/dsb/hotel-reservation.yaml
```
