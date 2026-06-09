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
  <strong>面向现代边缘的高性能 HTTP 缓存代理</strong><br>
  基于 Go 语言实现，采用 LSM-Tree 对象索引、多级存储和零停机热升级。
</p>

其他语言: [English](README.md)

---

## Tavern 是什么？

Tavern 是一个专为 CDN 边缘节点设计的 HTTP 缓存代理服务器。它位于客户端与上游源站之间，将响应缓存到磁盘上，并使用 LSM-Tree 支持的对象索引——在海量小文件场景下不会因内存不足而受限。

与传统缓存将所有元数据保存在 RAM 中不同，Tavern 使用嵌入式数据库来索引缓存对象，使其能够在有限内存下高效处理数百万缓存条目。

## 功能特性

### 缓存能力

- **请求合并** — 相同资源的并发请求合并为一次回源，防止缓存击穿和惊群效应
- **模糊刷新** — 对象即将过期时按可配置的比例异步回源更新，平滑回源流量
- **Range 请求缓存** — 完整支持单区间和多区间 Range 请求，可配置填充百分比
- **Vary 多版本缓存** — 遵守 HTTP `Vary` 头进行多版本缓存，支持配置忽略键以提高命中率
- **条件缓存** — `ETag` / `If-None-Match` / `If-Modified-Since` 校验与重新验证
- **缓存预热** — 基于可配置规则主动预热缓存条目
- **头重写** — 通过声明式 YAML 规则修改请求和响应头

### 存储引擎

- **LSM-tree 索引** — 对象元数据存储在 PebbleDB 或 NutsDB 中，将缓存容量与 RAM 大小解耦
- **多级存储桶** — 热/温/冷存储分层，根据访问模式自动升温和降温
- **桶选择策略** — 一致性哈希或轮询方式将缓存对象分布到各存储桶
- **切片磁盘存储** — 对象按可配置大小（默认 1 MB）分块存储，实现高效 I/O
- **目录感知路由** — 基于目录的缓存键索引，支持高效的目录级批量清理
- **淘汰策略** — FIFO、LRU、LFU，支持每桶对象数量上限

### 缓存失效 (PURGE)

- **URL 清理** — 单个缓存对象失效，支持软删除（标记过期）和硬删除（物理删除）
- **目录清理** — 批量失效 URL 路径前缀下的所有缓存对象
- **IP 白名单** — 限制清理操作的来源 IP
- **倒排索引** — 基于 SharedKV 的倒排索引，无需全量扫描即可快速目录清理

### 运维与可观测性

- **零停机升级** — 通过 `SIGUSR2` 信号使用 Cloudflare `tableflip` 实现二进制热升级，无连接丢失
- **优雅重启** — `SIGHUP` 信号触发平滑停止和重启
- **Prometheus 指标** — 内置 `/metrics` 端点，覆盖缓存、代理、服务器、存储层的计数器和直方图
- **PProf 性能分析** — `/debug/pprof/` 端点提供 Go 运行时分析，支持可选 Basic Auth
- **访问日志** — 结构化访问日志，支持可选的 AES 加密写入文件
- **健康检查** — `/healthz` 端点用于负载均衡探测

### 可扩展性

- **插件系统** — 通过启动时注册的 Go 插件扩展功能。内置插件包括：
  - `purge` — 缓存失效（见上文）
  - `qs` — 通过 SSE 流式推送的实时查询统计和 TopK 热点 URL 追踪
  - `verifier` — 对外部服务的异步 CRC 完整性校验
- **中间件管道** — 洋葱模型中间件链（Recovery → Rewrite → MultiRange → Caching）。通过 `init()` 注册自定义中间件。
- **存储后端** — 可插拔的存储桶实现（磁盘、内存、裸盘、自定义）和索引数据库引擎

### 工具链

| 工具 | 说明 |
|:---|:---|
| `tavern` | 主缓存代理服务器 |
| `tq` | 访问日志查询工具 — 解析和过滤加密访问日志 |
| `ttop` | 实时 TUI 仪表板 — 通过 SSE 获取实时缓存指标、热点 URL、CPU/内存 |

## 快速开始

### 环境要求

- Go 1.25+
- Linux 或 macOS（优雅升级依赖 Unix 信号）

### 安装与运行

```bash
git clone https://github.com/omalloc/tavern.git
cd tavern

# 从示例配置创建你的配置
cp config.example.yaml config.yaml

# 编译并运行
make build
./bin/tavern -c config.yaml
```

### Docker

```bash
docker build -t tavern .
docker run -p 8080:8080 -v ./config.yaml:/usr/local/tavern/config.yaml tavern
```

### Systemd

systemd unit 文件位于 `.build/systemd/system/tavern.service`。安装方式：

```bash
cp .build/systemd/system/tavern.service /etc/systemd/system/
systemctl enable --now tavern
```

无需停机重载配置：

```bash
systemctl kill -s SIGUSR2 tavern
```

### 验证

```bash
# 健康检查
curl http://localhost:8080/healthz

# 版本信息
curl http://localhost:8080/version

# Prometheus 指标
curl http://localhost:8080/metrics
```

## 配置

Tavern 使用单一 YAML 配置文件。关键配置段：

```yaml
server:
  addr: ":8080"
  middleware:          # 中间件链：recovery, rewrite, multirange, caching
    - name: caching
      options:
        fuzzy_refresh: true
        collapsed_request: true
        vary_ignore_key: ["Cookie"]

upstream:
  balancing: wrr       # 加权轮询
  address:
    - http://10.0.0.1:8000
    - http://10.0.0.2:8000

storage:
  db_type: pebble      # pebble | nutsdb
  eviction_policy: lru # fifo | lru | lfu
  selection_policy: hashring
  slice_size: 1048576  # 1 MB 分块
  migration:           # 冷热分层
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
> 完整带注释的配置请参见 [`config.example.yaml`](config.example.yaml)。

## 架构

```
客户端请求
  └── server (HTTPServer)
        ├── 内部路由: /metrics, /healthz, /debug/pprof/, /version
        └── 缓存管道:
              ├── Recovery      — panic 恢复、故障阈值
              ├── Rewrite       — 请求/响应头变换
              ├── MultiRange    — 多区间 Range 请求处理
              └── Caching       — 缓存键计算、对象查找、分块读取、
              │                   模糊刷新、请求合并、Vary 处理、异步重新验证
              └── Upstream Proxy — 连接池、节点选择、单飞合并
                  (最内层)
```

### 存储层

```
Caching 中间件
  └── Storage.Selector (hashring / roundrobin)
        └── Bucket (disk / memory / rawdisk)
              ├── 对象文件 (分块，1 MB 切片)
              └── IndexDB (PebbleDB / NutsDB) — 对象元数据
        └── SharedKV — 跨桶计数器、倒排索引
        └── DirAware — 目录级缓存键路由
        └── Migrator (可选) — 热/温/冷分层升降级
```

## 项目结构

```
tavern/
├── api/defined/v1/     接口契约 (storage, plugin, middleware)
├── cmd/
│   ├── tq/             访问日志查询 CLI
│   └── top/            实时 TUI 监控 (ttop)
├── conf/               配置结构定义
├── contrib/            内部框架 (应用生命周期、日志、配置)
├── docs/               文档 (架构、功能、生态)
├── internal/protocol/  生成的协议常量 (headers)
├── pkg/                工具包 (metrics, traces, encoding, iobuf)
├── plugin/
│   ├── purge/          缓存失效 (URL + 目录)
│   ├── qs/             查询统计、SSE、TopK 追踪
│   └── verifier/       CRC 完整性校验
├── proxy/              上游反向代理与连接池
├── server/
│   ├── middleware/     中间件链 (recovery, rewrite, multirange, caching)
│   └── mod/            请求包装、trace 注入
├── storage/
│   ├── bucket/         存储桶实现 (disk, memory, empty)
│   ├── indexdb/        索引数据库实现 (pebble, nutsdb)
│   ├── selector/       桶选择策略 (hashring, roundrobin)
│   ├── sharedkv/       跨桶键值存储
│   └── diraware/       目录感知缓存路由
└── tests/              集成测试 (需要运行 tavern)
```

## 文档

| 文档 | 说明 |
|:---|:---|
| [生态概览](docs/ecosystem/overview.md) | 三层 CDN 架构和全链路请求流程 |
| [协议规范](docs/ecosystem/protocol.md) | 内部 TR-*/X-* 头定义 |
| [Tavern 项目文档](docs/tavern/01-project.md) | 项目概述、技术栈、快速开始 |
| [Tavern 功能文档](docs/tavern/02-features.md) | 完整功能参考 |
| [Tavern 架构文档](docs/tavern/03-architecture.md) | 深入：中间件、存储、代理、插件系统 |
| [PURGE 设计](docs/purge.md) | 缓存失效设计、API 与内部实现 |
| [CDN 缓存分析](docs/cdn-cache-analysis.md) | Tavern vs Squid vs ATS 对比 |
| [Grafana 仪表板](docs/Grafana-Prometheus-Dashboard.json) | 预构建的 Grafana 仪表板模板 |

## 生态系统

- **Tavern Gateway** — 基于 OpenResty 的 L1/L3 网关，配合 Tavern 的 L2 缓存层
- **[CRC-Center](https://github.com/omalloc/trust-receive)** — 缓存文件完整性校验服务（由 `verifier` 插件使用）

## 致谢

Tavern 建立在以下优秀的开源工作之上：

- **[Kratos](https://github.com/go-kratos/kratos)** — 微服务框架，启发了模块化架构设计
- **[Pebble](https://github.com/cockroachdb/pebble)** — CockroachDB 的 LSM-tree K/V 存储引擎，驱动对象索引
- **[tableflip](https://github.com/cloudflare/tableflip)** — Cloudflare 的零停机进程升级方案
- **[NutsDB](https://github.com/nutsdb/nutsdb)** — 纯 Go 嵌入式 K/V 存储（备选索引后端）
- **[Prometheus Go Client](https://github.com/prometheus/client_golang)** — 指标采集
