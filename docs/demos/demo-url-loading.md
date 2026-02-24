# motel: Loading Topologies from URLs

*2026-02-24T13:44:42Z by Showboat 0.6.1*
<!-- showboat-id: 673702d5-5bd8-4a83-a614-cc2ee3061f7e -->

motel accepts `http://` and `https://` URLs anywhere a topology file path is accepted (`validate`, `check`, `run`, and `preview`). This is useful for sharing topologies via GitHub, internal servers, or any HTTP endpoint. URL fetches have a 10-second timeout and a 10 MB response body limit.

## Local HTTP server

To demonstrate URL loading without depending on an external service, we serve a topology file over HTTP using Python's built-in HTTP server.

```bash
python3 -m http.server 18923 --directory docs/examples &>/dev/null &
sleep 0.3
curl -s -o /dev/null -w "HTTP server on port 18923: %{http_code}\n" http://localhost:18923/basic-topology.yaml
```

```output
HTTP server on port 18923: 200
```

## Validate a remote topology

`motel validate` accepts a URL just like a file path.

```bash
build/motel validate http://localhost:18923/basic-topology.yaml
```

```output
Configuration valid: 5 services, 2 root operations

To generate signals:
  motel run --stdout http://localhost:18923/basic-topology.yaml

See https://github.com/andrewh/motel/tree/main/docs/examples for more examples.
```

## Check a remote topology

`motel check` works the same way — structural analysis runs on the fetched topology.

```bash
build/motel check --samples 0 http://localhost:18923/basic-topology.yaml
```

```output
PASS  max-depth: 2 (limit: 10)
      path: gateway.GET /users → user-service.list → postgres.query
PASS  max-fan-out: 2 (limit: 100)
      worst: order-service.create
PASS  max-spans: 4 static worst-case (limit: 10000)
```

## Generate traces from a remote topology

`motel run` fetches the topology once at startup, then generates traces as normal.

```bash
build/motel run --stdout --duration 200ms http://localhost:18923/basic-topology.yaml 2>/dev/null |
  jq -rs '"services: \([.[].Attributes[] | select(.Key == "synth.service") | .Value.Value] | unique)"'
```

```output
services: ["gateway","order-service","postgres","redis","user-service"]
```

All five services from the remote topology appear in the generated spans.

## Preview a remote topology

`motel preview` also accepts URLs.

```bash
build/motel preview http://localhost:18923/basic-topology.yaml | head -15
```

```output
<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 800 400" width="800" height="400">
<style>
  text { font-family: -apple-system, 'Segoe UI', Roboto, sans-serif; fill: #333; }
  .title { font-size: 14px; font-weight: 600; }
  .axis-label { font-size: 11px; }
  .tick-label { font-size: 10px; fill: #666; }
  .grid { stroke: #e0e0e0; stroke-width: 1; }
  .rate-line { fill: none; stroke: #2563eb; stroke-width: 1.5; stroke-linejoin: round; }
  .scenario-rect { fill: #f59e0b; fill-opacity: 0.15; stroke: #f59e0b; stroke-width: 1; stroke-opacity: 0.4; }
  .scenario-label { font-size: 9px; fill: #92400e; }
</style>
<rect width="800" height="400" fill="white"/>
<text x="70" y="24" class="title">basic-topology.yaml</text>
<rect x="285" y="40" width="430" height="310" class="scenario-rect"/>
<line x1="70" y1="350" x2="780" y2="350" class="grid"/>
```

## Error handling

motel reports HTTP errors clearly. A 404 from the server:

```bash
build/motel validate http://localhost:18923/nonexistent.yaml 2>&1; echo "exit code: $?"
```

```output
Error: reading config: fetching http://localhost:18923/nonexistent.yaml: HTTP 404
exit code: 1
```

An unreachable server:

```bash
build/motel validate http://localhost:1/topology.yaml 2>&1 | grep -c 'connection refused'
```

```output
1
```

## Clean up

```bash
kill %python3 2>/dev/null; echo "server stopped"
```

```output
server stopped
```
