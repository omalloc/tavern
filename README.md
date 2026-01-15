<h1 align="center">Tavern</h1>

<p align="center"><a href="https://tavern.omalloc.com/" target="_blank"><img src="https://www.omalloc.com/app_banner.webp?raw=true"></a></p>
<p align="center">
<a href="https://github.com/omalloc/tavern/actions"><img src="https://github.com/omalloc/tavern/actions/workflows/go.yml/badge.svg?branch=main" alt="Build Status"></a>
<a href="https://pkg.go.dev/github.com/omalloc/tavern"><img src="https://pkg.go.dev/badge/github.com/omalloc/tavern" alt="GoDoc"></a>
<a href="https://codecov.io/gh/omalloc/tavern"><img src="https://codecov.io/gh/omalloc/tavern/master/graph/badge.svg" alt="codeCov"></a>
<a href="https://goreportcard.com/report/github.com/omalloc/tavern"><img src="https://goreportcard.com/badge/github.com/omalloc/tavern" alt="Report Card"></a>
<a href="https://github.com/omalloc/tavern/blob/main/LICENSE"><img src="https://img.shields.io/github/license/omalloc/tavern" alt="License"></a>
</p>

<p align="center" x-desc="Sponsor">
</p>

<p align="center" x-desc="desc">
Tavern is a high-performance HTTP caching proxy server implemented in Go. It leverages a modern service framework to deliver a flexible architecture, strong extensibility, and excellent performance.
</p>

Other languages:  [简体中文](README.zh-CN.md) 

## ✨ Features

- **Core Caching Capabilities**:
  - [x] Prefetch
  - [x] Cache Push (URL/DIR Push)
    - [x] URL mark expired
    - [x] URL cache file delete
    - [ ] DIR mark expired
    - [x] DIR cache file delete
  - [x] Fuzzy refresh (Fuzzing fetch)
  - [x] Auto refresh
  - [x] Cache validation
  - [ ] Hot migration
  - [ ] Warm/cold split
  - [x] Upstream collapse request (request coalescing)
  - [ ] ~~Image compression adaptation (WebP support)~~
  - [x] Vary-based versioned cache (Vary cache)
  - [x] Headers rewrite
  - [x] Multiple Range requests support
  - [x] CacheFile verification (CRC checksum / EdgeMode)
    - You may need [CRC-Center](https://github.com/omalloc/trust-receive) Service.
- **Modern Architecture**:
  - Built on the **Kratos** framework for high extensibility and module reuse
  - **Plugin System**: Extend core business logic via plugins
  - **Storage Layer**: Decoupled storage backend with memory, disk, and custom implementations
- **Reliability & Operations**:
  - **Graceful Upgrade**: Zero-downtime config reload and binary upgrade
  - **Failure Recovery**: Built-in panic recovery and error handling
  - **Observability**: Native Prometheus metrics and PProf profiling
- **Traffic Control**:
  - Header rewrite (Rewrite)
  - Upstream load balancing (via custom Selector)

## 🚀 Quick Start

### Requirements

- Go 1.24+
- Linux/macOS (Graceful restart may be limited on Windows)

### 1. Fetch & Configure

Clone the repository and prepare the configuration file:

```bash
git clone https://github.com/omalloc/tavern.git
cd tavern

# Initialize with example configuration
cp config.example.yaml config.yaml
```

### 2. Run the Service

**Development mode:**

```bash
# Loads config.yaml from the current directory by default
go run main.go
```

**Build and run:**

```bash
make build
./bin/tavern -c config.yaml
```

### 3. Debugging & Monitoring

Once started, you can monitor and debug using the following (ports depend on `config.yaml`):

- **Metrics**: Access `/metrics` for Prometheus metrics (default prefix `tr_tavern_`)
- **PProf**: When debug mode is enabled, visit `/debug/pprof/` for profiling

## 🧩 Project Structure

- `api/`: Protocol and interface definitions
- `conf/`: Configuration definitions and parsing
- `plugin/`: Plugin interfaces and implementations
- `proxy/`: Core proxy and forwarding logic
- `server/`: HTTP server implementation and middleware
- `storage/`: Storage engine abstractions and implementations

## 📝 License

[MIT License](LICENSE)

## 🙏 Acknowledgments

This project integrates and is inspired by the following excellent open-source projects. Many thanks:

- **[Kratos](https://github.com/go-kratos/kratos)**: A powerful microservice framework that inspired Tavern's modern architecture.
- **[Pebble](https://github.com/cockroachdb/pebble)**: A high-performance key-value store by CockroachDB, powering efficient persistent caching.
- **[tableflip](https://github.com/cloudflare/tableflip)**: Cloudflare's graceful upgrade solution enabling zero-downtime restarts.
- **[Prometheus Go Client](https://github.com/prometheus/client_golang)**: Strong observability support for metrics.
