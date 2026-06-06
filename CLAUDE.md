# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

Go module: `github.com/omalloc/tavern` (Go 1.25, CGO-disabled static binary).

## Build & Test Commands

```bash
make build              # -> bin/tavern (static, CGO_ENABLED=0)
make toolchain          # -> bin/tq, bin/ttop (CLI tools)
make check              # go vet ./... + staticcheck ./...
make generate           # go generate ./... (protocol constants from internal/protocol/protocol.conf)
make run                # go run . -c config.yaml
make clean              # rm -rf bin/*
make install            # go mod tidy
make init               # sets GOPROXY, installs staticcheck

# Run all tests (CI starts tavern first)
go test -count=1 -v ./...

# Run a single package's tests (can run standalone for packages alongside source)
go test -count=1 -v ./storage/...
```

**Config file discovery**: `-c <path>` flag, defaults to `config.yaml` in CWD.

**CI** (`.github/workflows/go.yml`): `make build` → start `./bin/tavern -c ./tests/config.test.yaml` → `go test ./...`. Tests in `tests/` are integration tests requiring a running tavern. Unit tests (files named `*_test.go` alongside source) can run standalone. Also has `tests/all-features/` and `tests/mockserver/`.

## Architecture

Tavern is an HTTP caching proxy / CDN edge cache. It sits between clients and upstream origin servers, caching responses on disk with an LSM-tree-backed object index (PebbleDB or NutsDB).

### Request Flow

```
Client Request
  -> server/server.go (HTTPServer)
       -> server/mod/wrap.go (request filling, trace injection, response recording)
       -> Internal routes (/metrics, /healthz, /debug/pprof/, /version) for local IPs
       -> Cache pipeline (all other requests):
            -> Middleware chain (Recovery -> Rewrite -> MultiRange -> Caching)
            -> Plugin HandleFunc handlers
            -> Access log (optionally encrypted, written to file)
```

### Middleware Chain

Middlewares wrap `http.RoundTripper` in an onion chain — the innermost RoundTripper is the upstream proxy. Each middleware is a `func(http.RoundTripper) http.RoundTripper`. Defined in `server/middleware/middleware.go`. Registered via `init()` in each middleware package, created by name from config (`server/middleware/registry.go`).

Key middleware: `server/middleware/caching/` — cache key computation, object lookup/storage, chunked file handling, fuzzy refresh, request collapsing, Vary handling, range request filling, async revalidation, CRC/file-change detection.

### Storage Layer (`storage/`)

Multi-tier storage with these concepts:

- **IndexDB** (PebbleDB or NutsDB): LSM-tree for object metadata, avoids RAM blowup. Interface at `api/defined/v1/storage/indexdb.go`.
- **Buckets** (disk, memory, rawdisk, empty): Chunked file storage (configurable `slice_size`, default 1MB). Buckets at `storage/bucket/`.
- **Bucket Selector** (hashring or roundrobin): Distributes cache objects across buckets by URL hash. At `storage/selector/`.
- **SharedKV**: Cross-bucket key-value store for counters and shared state. At `storage/sharedkv/`.
- **Tiering**: Hot/warm/cold buckets with automatic Promote/Demote based on access patterns (`storage/migrator.go`).
- **DirAware**: Directory-aware cache key routing (`storage/diraware/`).

Key interface: `storage.Storage` (at `api/defined/v1/storage/storage.go`) — `Selector` + `Buckets()` + `SharedKV()` + `PURGE(url, control)` + `io.Closer`.

### Proxy (`proxy/`)

Upstream reverse proxy with:
- Per-upstream-address `http.Client` pools (TCP or Unix socket)
- Node selection via `omalloc/proxy` selector (configured via `Upstream.Balancing`)
- Custom `singleflight` for request coalescing on cache misses — uses `io.TeeReader` to fan out response bodies

### Plugin System (`plugin/`)

Plugins implement `pluginv1.Plugin` (`transport.Server` + `AddRouter` + `HandleFunc`). Registered via `init()`, created by name from config. Built-in plugins:
- **purge**: Handles `PURGE` HTTP method — IP allowlist, `Purge-Type` header (soft=expire, hard=delete)
- **qs**: Query Stats — SSE endpoint for real-time metrics (used by `ttop`), tracks hot URLs via TopK
- **verifier**: Sends cache completion events to an external CRC verification service

### Graceful Upgrades

Uses Cloudflare `tableflip`. `SIGUSR2` triggers zero-downtime upgrade — closes storage, calls `flip.Upgrade()`, new binary takes over. `SIGHUP` triggers graceful restart. See `main.go` lines ~188-206.

### Internal Framework (`contrib/`)

Kratos-inspired, standalone:
- `contrib/kratos/app.go` — App lifecycle (Start/Stop hooks, signal handling)
- `contrib/log/` — Structured logging with level filtering, context propagation, lumberjack rotation
- `contrib/config/` — YAML config loading with file/remote providers and change watcher
- `contrib/transport/` — HTTP server interface
- `contrib/container/list/` — Generic doubly-linked list

### Protocol & Headers

Internal headers defined in `internal/protocol/protocol.conf`, generated via `go generate`:
- `X-Request-ID`, `X-Cache`, `X-FS-Mem`, `X-Prefetch`, `X-CacheTime`
- Internal trace/store/swapfile/fill-range/error-code/upstream-addr headers

### Configuration

Defined in `conf/conf.go` as `Bootstrap` struct. Key sections: `Strict`, `Hostname`, `PidFile`, `Logger`, `Server` (with `PProf`, `AccessLog`), `Plugin`, `Upstream`, `Storage` (with `Buckets`, `DBType`, `EvictionPolicy`, `SelectionPolicy`, `SliceSize`, `DirAware`, `Migration`). Loaded from YAML. See `config.example.yaml`.

### Utility Packages (`pkg/`)

- `pkg/encoding/` — Content encoding (brotli, gzip, etc.)
- `pkg/errors/` — Error handling utilities
- `pkg/pathtrie/` — Path-based Trie for route matching
- `pkg/algorithm/` — Generic algorithms
- `pkg/metrics/` — Prometheus metrics helpers
- `pkg/traces/` — Request tracing
- `pkg/e2e/` — End-to-end test helpers
- `pkg/iobuf/` — I/O buffering
- `pkg/mapstruct/` — Map-to-struct decoding
- `pkg/x/` — Extended stdlib utilities

## Conventions

- **Interface compliance**: `var _ Interface = (*Concrete)(nil)` at package scope — every implementation asserts compile-time interface satisfaction.
- **Registration**: Packages self-register via `init()` + global registry (plugins, middleware, indexdb). Blank-import the backend packages in `main.go` or the consumer to activate.
- **Constructors**: `New(config *conf.X, logger log.Logger) (Type, error)` is the dominant pattern — config pointer + logger injected, error returned.
- **Logging**: Use `log.NewHelper(logger)` for structured key-value logging. Never use global log (except warn-level in `init()`).
- **Config**: Structs carry both `json:"…"` and `yaml:"…"` tags. Config flows top-down as pointers.
- **Test packages**: White-box `package foo` for internal tests (`contrib/log/`); black-box `package foo_test` for public API tests (`storage/`, `storage/bucket/disk/`). Integration tests live in `tests/` and need a running tavern.
- **Error handling**: Custom `pkg/errors.Error` for HTTP-visible errors. Sentinel errors via `errors.New`/`fmt.Errorf`. Interfaces return `(T, error)` — no panics in library code.
- **Naming**: Short Go-style names. Interfaces live in `api/defined/v1/` as contracts. Concrete implementations in sub-packages.

## Extension Points

To add a **middleware**: Create `server/middleware/<name>/`, implement `http.RoundTripper` wrapper, register via `middleware.RegisterFactory` in `init()`.

To add a **plugin**: Create `plugin/<name>/`, implement `pluginv1.Plugin`, register via `plugin.RegisterFactory` in `init()`. Blank-import in `main.go`.

To add a **storage bucket backend**: Implement `Bucket` from `api/defined/v1/storage/`, add to `storage/builder.go`'s `NewBucket`.

To add an **index DB backend**: Implement `IndexDB` from `api/defined/v1/storage/indexdb.go`, register in `storage/indexdb/registry.go`.
