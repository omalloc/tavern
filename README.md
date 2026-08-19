<h1 align="center">Tavern</h1>

<p align="center">
  <strong>A self-hosted HTTP cache for CDN edge delivery and origin protection.</strong><br>
  Collapse duplicate origin fetches, serve partial content efficiently, and operate cache nodes as one focused Go service.
</p>

<p align="center">
  <a href="https://github.com/omalloc/tavern/actions/workflows/go.yml"><img src="https://github.com/omalloc/tavern/actions/workflows/go.yml/badge.svg?branch=main" alt="Build Status"></a>
  <a href="https://pkg.go.dev/github.com/omalloc/tavern"><img src="https://pkg.go.dev/badge/github.com/omalloc/tavern" alt="Go Reference"></a>
  <a href="https://goreportcard.com/report/github.com/omalloc/tavern"><img src="https://goreportcard.com/badge/github.com/omalloc/tavern" alt="Go Report Card"></a>
  <a href="https://codecov.io/gh/omalloc/tavern"><img src="https://codecov.io/gh/omalloc/tavern/main/graph/badge.svg" alt="Code Coverage"></a>
  <a href="https://github.com/omalloc/tavern/blob/main/LICENSE"><img src="https://img.shields.io/github/license/omalloc/tavern" alt="License"></a>
</p>

<p align="center">
  <a href="docs/README.md">Documentation</a> ·
  <a href="config.example.yaml">Example configuration</a> ·
  <a href="CHANGELOG.md">Changelog</a> ·
  <a href="README.zh-CN.md">简体中文</a>
</p>

<p align="center">
  <a href="https://tavern.omalloc.com/">
    <img src="docs/dist/app_banner.png" alt="Tavern HTTP cache" width="100%">
  </a>
</p>

## Overview

Tavern is a reverse caching proxy for CDN edge and origin-shield workloads. Deploy it behind a gateway or load balancer to serve reusable responses locally, absorb concurrent cache misses, and prevent object expiry from becoming an origin traffic spike.

> [!NOTE]
> Tavern owns the **object delivery and cache lifecycle** layer. It is not a DNS service, global traffic scheduler, API gateway, or CDN control plane.

```text
Clients
   │
   ▼
Gateway / Load Balancer
   │
   ▼
Tavern ── cache hit ───────────────► Response
   │
   └──── controlled fetch/revalidation ──► Origin
```

Tavern is delivered as a CGO-free Go binary and configured with YAML. Run it as a single cache node or repeat the same service across an edge fleet.

## Why Tavern?

| Focus | What Tavern does | Operational result |
|:---|:---|:---|
| **Origin protection** | Collapses concurrent full-object misses and merges concurrent Range misses into one union fetch; refreshes objects before hard expiry | Fewer duplicate origin requests and smoother revalidation traffic |
| **Partial-content delivery** | Caches single, suffix, open-ended, and multi-range requests; reuses cached slices and fills missing ranges | Less redundant transfer for downloads, packages, and media objects |
| **Explicit cache lifecycle** | Supports URL or directory `PURGE`, soft expiry or hard deletion, bounded `Vary` variants, eviction, and storage tiering | Predictable invalidation and storage behavior |
| **Cache-node operations** | Exposes Prometheus metrics, health and profiling endpoints, structured logs, live terminal views, and graceful binary handover | Fewer surrounding components are needed to operate a node |

These capabilities also exist in different forms across NGINX, Varnish, Squid, and Apache Traffic Server. Tavern's focus is their integration in a compact, Go-native edge cache—not a claim that every individual feature is unique.

## Quick start

### Requirements

- Go 1.26+
- Linux or macOS
- An HTTP origin for cacheable content

### Build and run

```bash
git clone https://github.com/omalloc/tavern.git
cd tavern

cp config.example.yaml config.yaml
# Set upstream.address, storage paths, and log paths for this host.

make build
./bin/tavern -c config.yaml
```

In another terminal, verify the local administrative endpoints:

```bash
curl http://localhost:8080/healthz
curl http://localhost:8080/version
curl http://localhost:8080/metrics
```

After `upstream.address` points to a working origin, request the same cacheable URL twice and inspect `X-Cache`:

```bash
curl -sS -D - -o /dev/null http://localhost:8080/path/to/object
curl -sS -D - -o /dev/null http://localhost:8080/path/to/object
# The normal transition is MISS on the first request, then HIT.
```

The example configuration restricts administrative routes and `PURGE` to local hosts. Review `local_api_allow_hosts`, purge `allow_hosts`, PProf credentials, and filesystem paths before exposing a node.

## Where Tavern fits

| A good fit | Use another layer or product for |
|:---|:---|
| Origin shield or L2 cache behind an existing gateway | DNS, global traffic scheduling, or a managed CDN control plane |
| Range-heavy software, package, image, and media delivery | General API-gateway policy such as authentication and complex routing |
| Private edge networks requiring explicit purge and local storage control | Broad protocol support or a large runtime plugin ecosystem |
| Go infrastructure teams building custom middleware, storage, or control-plane integration | A drop-in replacement that must reuse NGINX, Varnish, Squid, or ATS configuration |

## Capabilities

### Caching and origin control

- Collapsed forwarding for full-object misses and concurrent missing ranges
- Probabilistic asynchronous refresh before hard expiry
- Conditional revalidation with `ETag` / `If-None-Match` and `Last-Modified` / `If-Modified-Since`
- Header-triggered cache prefetch
- Dedicated upstream connection pools over TCP or Unix sockets

### HTTP delivery

- Single-range, suffix-range, open-ended, and multi-range responses
- Partial cache hits and configurable missing-range fill
- `Vary`-aware variants with a configurable upper bound and ignored keys
- Optional query-string inclusion in cache keys
- Declarative request and response header rewriting
- Chunked object storage for incremental reads and writes

### Invalidation and storage

- URL and directory-prefix `PURGE`
- Soft invalidation for later revalidation or hard deletion for immediate removal
- Source allowlists for invalidation requests
- Disk and memory buckets with hash-ring or round-robin placement
- FIFO, LRU, and LFU eviction policies
- Optional hot, warm, and cold promotion or demotion
- Persistent object metadata through PebbleDB or NutsDB

### Extension points

- Composable `http.RoundTripper` middleware
- Built-in purge, query-stats, and integrity-verifier plugins
- Replaceable storage buckets, selectors, and index backends
- Compile-time registration through Go interfaces and `init()`; extensions are linked into the binary rather than loaded as runtime plugins

## Configuration

Tavern uses one YAML configuration file. This excerpt shows the primary request and storage path; see [`config.example.yaml`](config.example.yaml) for all options.

```yaml
server:
  addr: ":8080"
  middleware:
    - name: recovery
    - name: multirange
    - name: caching
      options:
        collapsed_request: true
        collapsed_request_wait_timeout: 100ms
        fuzzy_refresh: true
        fuzzy_refresh_rate: 0.1
        fill_range_percent: 100
        vary_limit: 100

upstream:
  address:
    - http://127.0.0.1:8000

storage:
  driver: native
  db_type: pebble
  eviction_policy: lru
  selection_policy: hashring
  slice_size: 1048576
  buckets:
    - path: /var/lib/tavern/cache
      type: normal
      max_object_limit: 10000000
```

### Invalidate cached objects

```bash
# Mark one object expired; the next request revalidates it.
curl -X PURGE http://localhost:8080/assets/app.js

# Delete one object immediately.
curl -X PURGE -H 'Purge-Type: file,hard' \
  http://localhost:8080/assets/app.js

# Mark every object under a path expired.
curl -X PURGE -H 'Purge-Type: dir' \
  http://localhost:8080/assets/
```

`PURGE` requests must come from an address configured in the plugin's `allow_hosts` list.

## Operations

| Interface | Purpose |
|:---|:---|
| `/metrics` | Prometheus cache, proxy, server, and storage metrics |
| `/healthz` / `/version` | Load-balancer health checks and build information |
| `/debug/pprof/` | Go runtime profiling, with optional Basic Auth |
| `ttop` | Live traffic rates, response codes, resource use, and hot URLs over SSE |
| `tq` | Access-log querying, including encrypted access logs |
| `SIGUSR2` | Graceful binary upgrade and listener handover through `tableflip` |

Access logs are structured and can optionally be encrypted on disk. Administrative HTTP endpoints are controlled by `local_api_allow_hosts`.

## Architecture

Tavern composes the request path from `http.RoundTripper` middleware. The upstream proxy is the innermost transport, while cache and delivery behavior remain independently replaceable.

```text
HTTP Server
├── Local endpoints: metrics, health, version, PProf
├── Plugins: purge, query stats, integrity verifier
└── Request pipeline
    ├── Recovery
    ├── Rewrite
    ├── MultiRange
    ├── Caching
    └── Upstream proxy
        ├── connection pooling
        ├── TCP / Unix socket transport
        └── request coalescing

Caching
└── Storage selector
    ├── disk / memory buckets
    ├── optional hot / warm / cold migration
    ├── shared state for purge and counters
    └── persistent object metadata
```

## Development

| Command | Purpose |
|:---|:---|
| `make build` | Build the static `bin/tavern` server binary |
| `make toolchain` | Build `bin/tq` and `bin/ttop` |
| `make check` | Run `go vet` and `staticcheck` |
| `make generate` | Regenerate protocol constants |
| `go test -count=1 -v ./storage/...` | Run a standalone package test suite |
| `go test -count=1 -v ./...` | Run all tests; integration tests require a running Tavern instance |

Issues and pull requests are welcome. Keep changes focused, add tests beside the affected package, and run the relevant package tests before submitting a pull request.

## Documentation

| Document | Description |
|:---|:---|
| [Documentation index](docs/README.md) | Entry point for project and ecosystem documentation |
| [Ecosystem overview](docs/ecosystem/overview.md) | Multi-layer CDN topology and request lifecycle |
| [Feature reference](docs/tavern/02-features.md) | Cache, storage, purge, and operations behavior |
| [Architecture](docs/tavern/03-architecture.md) | Middleware, proxy, storage, and plugin internals |
| [PURGE design](docs/purge.md) | Invalidation protocol and implementation |
| [Grafana dashboard](docs/Grafana-Prometheus-Dashboard.json) | Prometheus dashboard template |

## Ecosystem

- **Tavern Gateway** — an OpenResty-based L1/L3 gateway designed to complement Tavern as the L2 cache
- **[CRC-Center](https://github.com/omalloc/trust-receive)** — external cache-file integrity verification used by the `verifier` plugin

## License

Tavern is available under the terms of the [MIT License](LICENSE).
