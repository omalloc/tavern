<h1 align="center">Tavern</h1>

<p align="center">
  <a href="https://tavern.omalloc.com/" target="_blank">
    <img src="https://www.omalloc.com/app_banner.webp?raw=true" alt="Tavern Banner">
  </a>
</p>

<p align="center">
  <a href="https://github.com/omalloc/tavern/actions"><img src="https://github.com/omalloc/tavern/actions/workflows/go.yml/badge.svg?branch=main" alt="Build Status"></a>
  <a href="https://pkg.go.dev/github.com/omalloc/tavern"><img src="https://pkg.go.dev/badge/github.com/omalloc/tavern" alt="Go Reference"></a>
  <a href="https://goreportcard.com/report/github.com/omalloc/tavern"><img src="https://goreportcard.com/badge/github.com/omalloc/tavern" alt="Go Report Card"></a>
  <a href="https://github.com/omalloc/tavern/blob/main/LICENSE"><img src="https://img.shields.io/github/license/omalloc/tavern" alt="License"></a>
  <a href="https://codecov.io/gh/omalloc/tavern"><img src="https://codecov.io/gh/omalloc/tavern/master/graph/badge.svg" alt="Code Coverage"></a>
</p>

<p align="center">
  <strong>High-performance HTTP caching proxy for the modern edge</strong><br>
  Built in Go with an LSM-tree-backed object index, multi-tier storage, and zero-downtime upgrades.
</p>

Other languages: [简体中文](README.zh-CN.md)

---

## What is Tavern?

Tavern is an HTTP caching proxy server designed for CDN edge nodes. It sits between clients and upstream origin servers, caching responses on disk with an LSM-tree-backed object index — supporting massive numbers of small files without memory pressure.

Unlike traditional caches that hold all metadata in RAM, Tavern uses embedded databases to index cached objects, enabling efficient operation with limited memory while serving millions of cache entries.

## Features

### Caching

- **Request coalescing** — Collapses concurrent requests for the same resource into a single upstream fetch, preventing thundering herd
- **Fuzzy refresh** — Asynchronously revalidates objects nearing expiry with a configurable jitter rate, smoothing origin traffic
- **Range request caching** — Full support for single and multi-range requests, with configurable fill percentage
- **Vary-based versioning** — Respects HTTP `Vary` headers for multi-variant caching, with configurable ignore keys to maximize hit ratio
- **Conditional caching** — `ETag` / `If-None-Match` / `If-Modified-Since` validation and revalidation
- **Cache prefetch** — Proactively warm cache entries based on configurable rules
- **Header rewriting** — Modify request and response headers through declarative YAML rules

### Storage

- **LSM-tree indexing** — Object metadata stored in PebbleDB or NutsDB, decoupling cache capacity from RAM size
- **Multi-tier buckets** — Hot / warm / cold storage tiers with automatic promotion and demotion based on access patterns
- **Bucket selection** — Hash-ring or round-robin distribution of cache objects across storage buckets
- **Slice-based disk storage** — Objects divided into configurable-sized chunks (default 1 MB) for efficient I/O
- **Directory-aware routing** — Per-directory cache key indexing for efficient directory-level purge operations
- **Eviction policies** — FIFO, LRU, and LFU with per-bucket object limits

### Cache Invalidation (PURGE)

- **URL purge** — Invalidate individual cached objects, either soft (mark expired) or hard (delete)
- **Directory purge** — Bulk invalidate all cached objects under a URL path prefix
- **IP allowlisting** — Restrict purge access to trusted control-plane hosts
- **Inverted indexing** — SharedKV-backed index for fast directory purge without full scans

### Operations & Observability

- **Zero-downtime upgrades** — Binary hot-upgrade via `SIGUSR2` using Cloudflare's `tableflip`; no dropped connections
- **Graceful restart** — `SIGHUP` triggers a clean stop and restart
- **Prometheus metrics** — Built-in `/metrics` endpoint with counters, histograms, and gauges across cache, proxy, server, and storage layers
- **PProf profiling** — Go runtime profiling at `/debug/pprof/` with optional basic auth
- **Access logging** — Structured access logs with optional AES encryption to file
- **Health check** — `/healthz` endpoint for load balancer integration

### Extensibility

- **Plugin system** — Extend functionality through Go plugins registered at startup. Built-in plugins include:
  - `purge` — Cache invalidation (see above)
  - `qs` — Real-time query stats with SSE streaming and TopK hot-URL tracking
  - `verifier` — Asynchronous CRC integrity verification against an external service
- **Middleware pipeline** — Onion-model middleware chain (Recovery → Rewrite → MultiRange → Caching). Register custom middleware via `init()`.
- **Storage backends** — Pluggable bucket implementations (disk, memory, raw disk, custom) and index DB engines

### Toolchain

| Tool | Description |
|:---|:---|
| `tavern` | Main caching proxy server |
| `tq` | Access log query tool — parse and filter encrypted access logs |
| `ttop` | Real-time TUI dashboard — live cache metrics, hot URLs, CPU/memory via SSE |

## Quick Start

### Prerequisites

- Go 1.25+
- Linux or macOS (graceful upgrade uses Unix signals)

### Install & Run

```bash
git clone https://github.com/omalloc/tavern.git
cd tavern

# Create your config from the example
cp config.example.yaml config.yaml

# Build and run
make build
./bin/tavern -c config.yaml
```

### Docker

```bash
docker build -t tavern .
docker run -p 8080:8080 -v ./config.yaml:/usr/local/tavern/config.yaml tavern
```

### Systemd

A systemd unit file is provided at `.build/systemd/system/tavern.service`. Install it with:

```bash
cp .build/systemd/system/tavern.service /etc/systemd/system/
systemctl enable --now tavern
```

Reload the configuration without downtime:

```bash
systemctl kill -s SIGUSR2 tavern
```

### Verify

```bash
# Health check
curl http://localhost:8080/healthz

# Version info
curl http://localhost:8080/version

# Prometheus metrics
curl http://localhost:8080/metrics
```

## Configuration

Tavern uses a single YAML configuration file. Key sections:

```yaml
server:
  addr: ":8080"
  middleware:          # Middleware chain: recovery, rewrite, multirange, caching
    - name: caching
      options:
        fuzzy_refresh: true
        collapsed_request: true
        vary_ignore_key: ["Cookie"]

upstream:
  balancing: wrr       # Weighted round-robin
  address:
    - http://10.0.0.1:8000
    - http://10.0.0.2:8000

storage:
  db_type: pebble      # pebble | nutsdb
  eviction_policy: lru # fifo | lru | lfu
  selection_policy: hashring
  slice_size: 1048576  # 1 MB chunks
  migration:           # Hot/cold tiering
    enabled: true
    promote:
      min_hits: 10
      window: 1m
    demote:
      min_hits: 2
      window: 5m
  buckets:
    - path: /cache/hot
      type: hot
      max_object_limit: 10000000
    - path: /cache/warm
      type: warm
```

> [!TIP]
> See [`config.example.yaml`](config.example.yaml) for a complete annotated configuration with all options.

## Architecture

```
Client Request
  └── server (HTTPServer)
        ├── Internal routes: /metrics, /healthz, /debug/pprof/, /version
        └── Cache pipeline:
              ├── Recovery      — panic recovery, failure threshold
              ├── Rewrite       — request/response header transformation
              ├── MultiRange    — multi-range request handling
              └── Caching       — cache key computation, object lookup,
              │                   chunked reads, fuzzy refresh, request
              │                   collapsing, Vary handling, async revalidation
              └── Upstream Proxy — connection pooling, node selection,
                  (innermost)      singleflight coalescing
```

### Storage Layer

```
Caching Middleware
  └── Storage.Selector (hashring / roundrobin)
        └── Bucket (disk / memory / rawdisk)
              ├── Object files (chunked, 1 MB slices)
              └── IndexDB (PebbleDB / NutsDB) — object metadata
        └── SharedKV — cross-bucket counters, inverted indexes
        └── DirAware — directory-level cache key routing
        └── Migrator (optional) — hot/warm/cold tier promotion & demotion
```

## Project Structure

```
tavern/
├── api/defined/v1/     Interface contracts (storage, plugin, middleware)
├── cmd/
│   ├── tq/             Access log query CLI
│   └── top/            Real-time TUI monitoring (ttop)
├── conf/               Configuration struct definitions
├── contrib/            Internal framework (app lifecycle, logging, config)
├── docs/               Documentation (architecture, features, ecosystem)
├── internal/protocol/  Generated protocol constants (headers)
├── pkg/                Utility packages (metrics, traces, encoding, iobuf)
├── plugin/
│   ├── purge/          Cache invalidation (URL + directory)
│   ├── qs/             Query stats, SSE, TopK tracking
│   └── verifier/       CRC integrity verification
├── proxy/              Upstream reverse proxy with connection pooling
├── server/
│   ├── middleware/     Middleware chain (recovery, rewrite, multirange, caching)
│   └── mod/            Request wrapping, trace injection
├── storage/
│   ├── bucket/         Bucket implementations (disk, memory, empty)
│   ├── indexdb/        Index DB implementations (pebble, nutsdb)
│   ├── selector/       Bucket selection (hashring, roundrobin)
│   ├── sharedkv/       Cross-bucket key-value store
│   └── diraware/       Directory-aware cache routing
└── tests/              Integration tests (require running tavern)
```

## Documentation

| Document | Description |
|:---|:---|
| [Ecosystem Overview](docs/ecosystem/overview.md) | Three-tier CDN architecture and full request lifecycle |
| [Protocol Specification](docs/ecosystem/protocol.md) | Internal TR-*/X-* header definitions |
| [Tavern Project](docs/tavern/01-project.md) | Project overview, tech stack, quick start |
| [Tavern Features](docs/tavern/02-features.md) | Complete feature reference |
| [Tavern Architecture](docs/tavern/03-architecture.md) | Deep dive: middleware, storage, proxy, plugin systems |
| [PURGE Design](docs/purge.md) | Cache invalidation design, API, and internals |
| [CDN Cache Analysis](docs/cdn-cache-analysis.md) | Tavern vs Squid vs ATS comparison |
| [Grafana Dashboard](docs/Grafana-Prometheus-Dashboard.json) | Pre-built Grafana dashboard template |

## Ecosystem

- **Tavern Gateway** — OpenResty-based L1/L3 gateway complementing Tavern's L2 cache
- **[CRC-Center](https://github.com/omalloc/trust-receive)** — Cache file integrity verification service (used by the `verifier` plugin)

## Acknowledgments

Tavern builds on excellent open-source work:

- **[Kratos](https://github.com/go-kratos/kratos)** — Microservice framework inspiring the modular architecture
- **[Pebble](https://github.com/cockroachdb/pebble)** — CockroachDB's LSM-tree K/V store, powering the object index
- **[tableflip](https://github.com/cloudflare/tableflip)** — Cloudflare's zero-downtime process upgrade
- **[NutsDB](https://github.com/nutsdb/nutsdb)** — Pure Go embeddable K/V store (alternative index backend)
- **[Prometheus Go Client](https://github.com/prometheus/client_golang)** — Metrics instrumentation
