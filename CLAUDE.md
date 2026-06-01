# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Build & Test Commands

```bash
# Build the main binary (static, CGO disabled)
make build              # -> bin/tavern

# Build CLI tools (access log pretty-printer, terminal dashboard)
make toolchain          # -> bin/tq, bin/ttop

# Run static analysis
make check              # go vet ./... + staticcheck ./...

# Generate protocol constants (from internal/protocol/protocol.conf)
make generate           # go generate ./...

# Run the service locally
make run                # runs with -c config.yaml

# Run all tests (requires a running tavern instance)
go test -count=1 -v ./...

# Run a single package's tests
go test -count=1 -v ./storage/...
```

Tests in `tests/` are integration tests that require a running tavern. CI starts tavern with `./bin/tavern -c ./tests/config.test.yaml` before running `go test ./...`. Unit tests (files alongside source) can run standalone.

## Architecture

Tavern is an HTTP caching proxy / CDN edge cache. It sits between clients and upstream origin servers, caching responses on disk with an LSM-tree-backed object index.

### Request Flow

```
Client Request
  -> HTTPServer (server/server.go)
       -> Internal routes (/metrics, /healthz, /debug/pprof/, /version) for local IPs
       -> Cache pipeline (all other requests):
            -> Middleware chain (Recovery -> Rewrite -> MultiRange -> Caching)
            -> Plugin HandleFunc handlers
            -> Access log
```

### Middleware Chain

Middlewares wrap `http.RoundTripper` in an onion chain — the innermost RoundTripper is the upstream proxy. Each middleware is a `func(http.RoundTripper) http.RoundTripper`. Defined in `server/middleware/middleware.go`. They are registered via `init()` in each middleware package and created by name from config (`server/middleware/registry.go`).

Key middleware: `server/middleware/caching/` — cache key computation, object lookup/storage, chunked file handling, fuzzy refresh, request collapsing, Vary handling, range request filling, async revalidation, CRC/file-change detection.

### Storage Layer (`storage/`)

Multi-tier storage with these concepts:

- **IndexDB** (PebbleDB or NutsDB): An LSM-tree that stores object metadata/index so millions of cached objects don't consume RAM. Interface at `api/defined/v1/storage/`.
- **Buckets** (disk, memory, rawdisk, empty): Where cached file data lives. Objects are stored as chunked files (configurable `slice_size`, default 1MB).
- **Bucket Selector** (hashring or roundrobin): Distributes cache objects across buckets by URL hash.
- **SharedKV**: Cross-bucket key-value store for counters and shared state.
- **Tiering**: Buckets can be hot/warm/cold with automatic Promote/Demote based on access patterns (`storage/migrator.go`).

Key interface: `storage.Storage` (at `api/defined/v1/storage/storage.go`) — `Selector` + `Buckets()` + `SharedKV()` + `PURGE(url, control)` + `io.Closer`.

### Proxy (`proxy/`)

Upstream reverse proxy with:
- Per-upstream-address `http.Client` pools (TCP or Unix socket)
- Node selection via `omalloc/proxy` selector
- Custom `singleflight` implementation for request coalescing on cache misses — uses `io.TeeReader` to fan out response bodies to multiple concurrent waiters

### Plugin System (`plugin/`)

Plugins implement `pluginv1.Plugin` (`transport.Server` + `AddRouter` + `HandleFunc`). Registered via `init()` and created by name from config. Three built-in plugins:
- **purge**: Handles `PURGE` HTTP method — validates IP allowlist, parses `Purge-Type` header, delegates to storage PURGE (soft=mark expired, hard=delete)
- **qs**: Query Stats — exposes SSE endpoint for real-time metrics (used by `ttop` terminal dashboard), tracks hot URLs via TopK
- **verifier**: Sends cache completion events to an external CRC verification service

### Graceful Upgrades

Uses Cloudflare `tableflip`. `SIGUSR2` triggers a zero-downtime binary upgrade — the running process closes storage, calls `flip.Upgrade()`, and the new binary takes over the listener. See `main.go` lines 188-206.

### Internal Framework (`contrib/`)

Self-contained framework (Kratos-inspired, not importing the actual Kratos library):
- `contrib/kratos/app.go` — App lifecycle manager (Start/Stop hooks, signal handling)
- `contrib/log/` — Structured logging with level filtering and context propagation
- `contrib/config/` — Config loading with file provider and change watcher

### Configuration

Defined in `conf/conf.go` as `Bootstrap` struct with `Server`, `Logger`, `Upstream`, `Storage` (with `Buckets`), `Plugin`, `DirAware`, and `Migration` sections. Loaded from YAML via `contrib/config`. See `config.example.yaml`.

## Extension Points

To add a **middleware**: Create a package in `server/middleware/<name>/`, implement the `http.RoundTripper` wrapper, and register it in an `init()` function via `middleware.RegisterFactory`.

To add a **plugin**: Create a package in `plugin/<name>/`, implement `pluginv1.Plugin`, and register it in an `init()` via `plugin.RegisterFactory`. Blank-import the package in `main.go`.

To add a **storage bucket backend**: Implement the `Bucket` interface from `api/defined/v1/storage/` and add it to `storage/builder.go`'s `NewBucket` factory.

To add an **index DB backend**: Implement `IndexDB` from `api/defined/v1/storage/` and register in `storage/indexdb/registry.go`.
