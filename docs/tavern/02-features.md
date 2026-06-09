# Tavern 功能文档 / Tavern Features & Requirements

> 本文档完整描述 Tavern (L2 Cache) 的全部功能能力、配置选项和使用场景。
> 功能状态: ✅ = 已实现 | 🚧 = 部分实现 | ❌ = 已废弃

---

## 1. 核心缓存能力 / Core Caching Capabilities

### 1.1 功能状态矩阵 / Feature Status Matrix

| 功能 / Feature | 状态 / Status | 版本 / Since | 配置项 / Config Key |
|:---|:---:|:---|:---|
| **缓存预取 (Prefetch)** | ✅ | v1.0 | `X-Prefetch` 头 (L1 注入) |
| **URL 缓存推送 (URL Push)** | ✅ | v1.0 | PURGE 标记过期 / 删除 |
| **目录缓存推送 (DIR Push)** | ✅ | v1.0 | PURGE + `Purge-Type: dir` |
| **模糊刷新 (Fuzzy Refresh)** | ✅ | v1.0 | `caching.fuzzy_refresh` |
| **自动刷新 (Auto Refresh)** | ✅ | v1.0 | 通过异步回源校验实现 |
| **缓存校验 (Cache Validation)** | ✅ | v1.0 | If-None-Match / If-Modified-Since |
| **热点迁移 (Hot Migration)** | ✅ | v1.1 | `storage.migration` |
| **冷热分离 (Warm/Cold Split)** | ✅ | v1.1 | Bucket `type: hot/cold/warm` |
| **请求合并 (Request Collapsing)** | ✅ | v1.0 | `caching.collapsed_request` |
| **Vary 多版本缓存** | ✅ | v1.0 | `caching.vary_limit` / `vary_ignore_key` |
| **Header 重写 (Rewrite)** | ✅ | v1.0 | `server.middleware.rewrite` |
| **Multi-Range 支持** | ✅ | v1.0 | `server.middleware.multirange` |
| **CRC 文件校验** | ✅ | v1.1 | `plugin.verifier` |
| **图像压缩自适应 (WebP)** | ❌ | — | 已废弃 |

### 1.2 缓存预取 (Prefetch) / Cache Prefetch

**触发方式：** L1 Gateway 设置 `X-Prefetch: 1` 请求头。

**行为：**
- 当缓存对象即将过期时，在返回旧缓存内容的同时异步触发回源刷新
- 避免在缓存过期瞬间产生回源流量尖刺

**代码路径：** `server/middleware/caching/caching_prefetch.go`

### 1.3 模糊刷新 (Fuzzy Refresh) / Fuzzy Refresh

**配置：**
```yaml
server:
  middleware:
    - name: caching
      options:
        fuzzy_refresh: true
        fuzzy_refresh_rate: 0.1   # 在 TTL 剩余 10% 时触发异步刷新
```

**行为：**
- 在对象 TTL 剩余 `fuzzy_refresh_rate` 比例时（如剩余 10%），随机选择一个时间点触发异步回源刷新
- 过期前所有请求仍使用当前缓存版本
- 刷新成功后无缝切换到新版本

**代码路径：** `server/middleware/caching/caching_fuzzy_test.go`

### 1.4 请求合并 (Request Collapsing) / Request Collapsing

**配置：**
```yaml
server:
  middleware:
    - name: caching
      options:
        collapsed_request: true
        collapsed_request_wait_timeout: 100ms  # 等待超时
```

**行为：**
- 当多个并发请求同时请求同一个未缓存的资源时，仅第一个请求回源
- 后续请求等待第一个请求返回后共享结果（fan-out）
- 超时后自动放弃等待，独立回源

**实现：** 基于 `proxy/singleflight/` 的自定义实现，使用 `io.TeeReader` 将响应体同时写入等待者。

**代码路径：** `proxy/singleflight/`

### 1.5 Vary 多版本缓存 / Vary-Based Versioned Cache

**配置：**
```yaml
server:
  middleware:
    - name: caching
      options:
        vary_limit: 100                  # Vary 版本数上限
        vary_ignore_key:
          - "Cookie"                      # 忽略 Cookie（避免版本爆炸）
          - "Access-Control-Request-Headers"
          - "Access-Control-Request-Method"
```

**行为：**
- 根据源站响应的 `Vary` 头创建同一 URL 的多个缓存版本
- `vary_ignore_key` 列表中的 Header 不计入 Vary Key，提高缓存命中率
- `vary_limit` 限制单一 URL 的版本数上限，防止版本爆炸

**代码路径：** `server/middleware/caching/caching_vary.go`

### 1.6 缓存校验 (Cache Validation) / Cache Validation

**行为：**
- 当缓存对象过期时，携带 `If-None-Match` (ETag) 或 `If-Modified-Since` 回源
- 源站返回 `304 Not Modified` → 续期缓存 (REVALIDATE_HIT)
- 源站返回 `200 OK` → 替换缓存 (REVALIDATE_MISS)

**代码路径：** `server/middleware/caching/caching_revalidate.go`

### 1.7 CRC 文件校验 / CRC File Verification

**配置：**
```yaml
plugin:
  - name: verifier
    options:
      endpoint: https://crc-svc.omalloc.com/receive
      api_key: your_api_key_here
      timeout: 5
      report_ratio: 100            # 上报比例 (%)
```

**行为：**
- 缓存写入完成后异步计算文件 xxhash (原为 md5，v1.1 升级)
- 将 hash 上报到外部 CRC 校验服务 (CRC-Center)
- 校验服务对比源站 hash 以检测缓存文件篡改/损坏
- `report_ratio` 控制采样比例

**代码路径：** `plugin/verifier/`, `server/middleware/caching/internal_checksum.go`

---

## 2. 缓存清除 (PURGE) / Cache Invalidation

### 2.1 请求格式 / Request Format

| 方法 / Method | 端点 / URL | 功能 / Purpose |
|:---|:---|:---|
| `PURGE` | `http://<host>/<path>` | 清除单个 URL 缓存 |
| `PURGE` | `http://<host>/<prefix>` | 清除目录前缀下所有缓存 |

### 2.2 Purge-Type 头 / Purge-Type Header

| 值 / Value | 模式 / Mode | 策略 / Strategy |
|:---|:---|:---|
| (空 / empty) | `file` | 软删除 (MarkExpired) — 设过期时间为过去，下次访问触发重校验 |
| `file,hard` | `file` | 硬删除 (Hard) — 直接删除缓存文件和元数据 |
| `dir` | `dir` | 目录标记过期 (当前为空操作，已知限制) |
| `dir,hard` | `dir` | 目录硬删除 — 清理该前缀下的所有缓存 |

### 2.3 安全控制 / Security

```yaml
plugin:
  - name: purge
    options:
      allow_hosts:
        - "127.0.0.1"
        - "::1"
        - "10.0.0.0/8"
      header_name: "Purge-Type"
      log_path: "logs/purge.log"
```

- `allow_hosts`: IP 白名单（仅这些源 IP 可执行 PURGE）
- 非白名单请求返回 `403 Forbidden`

### 2.4 内部存储流程 / Internal Storage Flow

```
PURGE Request → plugin/purge/purge.go
  ├─ 提取 storeUrl (优先 i-x-store-url, 否则 req.URL)
  ├─ 解析 Purge-Type → (file/dir, hard/soft)
  ├─ Directory?: 检查 SharedKV if/domain/<host> 计数器
  └─ Storage.PURGE(storeUrl, PurgeControl) → storage/storage.go
       ├─ File + Hard:  bucket.Discard(id)
       ├─ File + Soft:  bucket.Lookup(id) → 设 ExpiresAt=过去 → bucket.Store(md)
       ├─ Dir + Hard:   SharedKV ix/<bucketID>/<prefix> 倒排索引 → DiscardWithHash
       │                 → Fallback: 扫描 IndexDB 匹配 Path 前缀
       └─ Dir + Soft:   当前为空操作 (已知限制)
```

**详细设计：** 参见 `docs/purge.md`

---

## 3. 存储特性 / Storage Features

### 3.1 存储桶类型 / Bucket Types

| 类型 / Type | 配置值 / Config | 存储位置 / Storage | 用途 / Use Case |
|:---|:---|:---|:---|
| **磁盘 (Disk)** | `normal` / `warm` | 文件系统 (分块) | 主缓存存储 |
| **热数据 (Hot)** | `hot` | 快速磁盘/SSD | 热点对象 |
| **冷数据 (Cold)** | `cold` | 慢速磁盘/HDD | 低频对象 (迁移目标) |
| **内存 (Memory)** | `memory` | 进程内存 | 超热数据（仅限一个桶） |
| **裸盘 (RawDisk)** | `rawdisk` | 裸设备直接 IO | 极限性能场景 |
| **空 (Empty)** | `empty` | 无 | NOP/禁用缓存 |

### 3.2 淘汰策略 / Eviction Policies

| 策略 / Policy | 配置值 / Config | 算法 / Algorithm |
|:---|:---|:---|
| **先进先出 / FIFO** | `fifo` | 最早缓存的对象先淘汰 |
| **最近最少使用 / LRU** | `lru` | 基于访问时间的淘汰 (Clock + Counter bits: `Mark`) |
| **最少使用频率 / LFU** | `lfu` | 基于访问频率的淘汰 |

**Mark 结构 (64 bits):**
```
┌──────────────────────────────────────┬──────────────────────┐
│     Clock bits (48)                  │  Counter bits (16)   │
│     Last access timestamp            │  Access refs count   │
└──────────────────────────────────────┴──────────────────────┘
```

**代码路径：** `api/defined/v1/storage/storage.go:148-182` (`Mark` 类型)

### 3.3 Bucket 选择策略 / Bucket Selection Policy

| 策略 / Policy | 配置值 / Config | 算法 / Algorithm |
|:---|:---|:---|
| **一致性哈希 / Hash Ring** | `hashring` | 按 URL 哈希分配到桶 |
| **轮询 / Round Robin** | `roundrobin` | 按请求顺序轮流分配 |

### 3.4 冷热分层与迁移 / Tiering & Migration

**配置：**
```yaml
storage:
  migration:
    enabled: true
    promote:
      min_hits: 10        # 在时间窗口内命中 ≥ N 次则提升
      window: 1m          # 时间窗口
    demote:
      min_hits: 2         # 在时间窗口内命中 ≤ N 次则降级
      window: 5m          # 时间窗口
      occupancy: 75       # 存储使用率超过此比例触发降级
```

**行为：**
- **Promote (提升)**: Hot bucket 中的对象在 `window` 内访问次数 ≥ `min_hits` → 迁移到更快的存储层
- **Demote (降级)**: Warm bucket 中的对象在 `window` 内访问次数 ≤ `min_hits` + 存储使用率 > `occupancy%` → 迁移到更慢的存储层
- 迁移操作：`bucket.Migrate(ctx, id, destBucket)` → 复制对象数据与元数据 → 从源桶删除

**代码路径：** `storage/migrator.go`

### 3.5 目录感知 (DirAware) / Directory-Aware Routing

**配置：**
```yaml
storage:
  diraware:
    enabled: true
    store_path: /cache1/.diraware
    auto_clear: true     # 每天凌晨 2 点自动清理过期任务
```

**行为：**
- 支持目录级别的缓存刷新任务存储
- 将 SharedKV 从内存替换为持久化存储 (`store_path`)
- `auto_clear` 自动清理过期推送任务

**代码路径：** `storage/diraware/`

### 3.6 分块文件存储 / Chunked File Storage

**配置：**
```yaml
storage:
  slice_size: 1048576  # 1MB，每个 chunk 的大小
```

**行为：**
- 大文件按 `slice_size` 分块存储
- 每个 chunk 独立写入和读取
- 支持 Range 请求部分命中 (PART_HIT / PART_MISS)
- 磁盘桶: `storage/bucket/disk/disk.go`

---

## 4. 流量控制 / Traffic Control

### 4.1 Header 重写 / Header Rewrite

**配置：**
```yaml
server:
  middleware:
    - name: rewrite
      options:
        request_headers_rewrite:
          set:
            X-Custom-Header: "value"
          add:
            X-Forwarded-For: "$remote_addr"
          remove:
            - "X-Unwanted-Header"
        response_headers_rewrite:
          set:
            X-Frame-Options: "DENY"
            X-Content-Type-Options: "nosniff"
            Referrer-Policy: "no-referrer"
          add:
            X-XSS-Protection: "1; mode=block"
          remove:
            - "Server"
```

**操作类型 / Operation Types:**
- `set`: 覆盖设置（存在则替换，不存在则添加）
- `add`: 追加（可以追加多个值）
- `remove`: 删除

**代码路径：** `server/middleware/rewrite/rewrite.go`

### 4.2 上游负载均衡 / Upstream Load Balancing

**配置：**
```yaml
upstream:
  balancing: wrr               # wrr (加权轮询)
  address:
    - http://127.0.0.1:8000
    - unix:///tmp/gw.sock      # Unix Socket 支持
  max_idle_conns: 1000
  max_idle_conns_per_host: 500
  max_connections_per_server: 100
  insecure_skip_verify: true    # 跳过 TLS 验证
  resolve_addresses: false
  features:
    limit_rate_by_fd: true      # 基于 FD 的速率限制
```

**行为：**
- 每个上游地址维护独立的 `http.Client` 连接池
- 基于 `omalloc/proxy` 的 Selector 抽象进行节点选择
- 支持 TCP 和 Unix Socket 两种传输方式
- `limit_rate_by_fd`: 启用文件描述符级别的速率限制

**代码路径：** `proxy/proxy.go`

---

## 5. 运维特性 / Operational Features

### 5.1 平滑升级 / Graceful Upgrade

| 信号 / Signal | 行为 / Behavior |
|:---|:---|
| `SIGUSR2` | 二进制热升级 (tableflip) — 关闭存储 → `flip.Upgrade()` → 新进程接管。零连接丢失。 |
| `SIGHUP` | 优雅重启 — 重新加载配置，重新打开日志文件 |
| `SIGINT` / `SIGTERM` | 优雅关闭 — 停止接收新请求 → 等待进行中请求完成 → 关闭存储 → 退出 |

**代码路径：** `main.go:188-206`

### 5.2 故障恢复 / Failure Recovery

| 机制 / Mechanism | 行为 / Behavior |
|:---|:---|
| **Panic Recovery** | 中间件 `recovery` 捕获 `http.Handler` 中的 panic，返回 500，保持服务运行 |
| **故障熔断** | `fail_count_threshold` + `fail_window` 时间内超过阈值则熔断 |
| **请求超时** | 多层超时：读写超时、空闲超时、Header 读取超时 |

**配置：**
```yaml
server:
  read_timeout: 60s
  write_timeout: 60s
  idle_timeout: 90s
  read_header_timeout: 30s
  max_header_bytes: 1048576
  middleware:
    - name: recovery
      options:
        fail_count_threshold: 20
        fail_window: 60
```

### 5.3 可观测性 / Observability

#### Prometheus 指标 / Prometheus Metrics

**前缀：** `tr_tavern_`

| 指标类别 / Category | 指标示例 / Examples | 来源 / Source |
|:---|:---|:---|
| **缓存指标** | 命中率、MISS 数、BYPASS 数、对象数量 | `server/middleware/caching/metrics.go` |
| **存储指标** | 桶对象数、磁盘使用、淘汰事件 | `storage/bucket/disk/metrics.go` |
| **代理指标** | 上游请求数、延迟、连接池状态 | `proxy/metrics.go` |
| **服务器指标** | 请求数、响应时间、状态码分布 | `server/` |
| **插件指标** | PURGE 次数、QS 统计 | `plugin/purge/`, `plugin/qs/` |

#### PProf 性能分析 / PProf Profiling

```
GET /debug/pprof/            # 索引页
GET /debug/pprof/profile     # CPU Profile
GET /debug/pprof/heap        # 内存 Profile
GET /debug/pprof/goroutine   # Goroutine 状态
```

**访问控制：** Basic Auth (`pprof.username` / `pprof.password`)

### 5.4 CLI 工具 / CLI Tools

#### ttop — 实时缓存状态监控

```bash
./bin/ttop -c config.yaml
```

**功能：**
- 实时显示缓存命中率、请求速率 (RPS，平滑计算)
- TopK 热门 URL 排行
- 插件 QS SSE 数据可视化
- CPU/内存使用率监控

**代码路径：** `cmd/top/`

#### tq — 命令行查询工具

```bash
./bin/tq
```

**功能：** 命令行操作缓存、查询存储状态

**代码路径：** `cmd/tq/`

### 5.5 访问日志 / Access Log

**配置：**
```yaml
server:
  access_log:
    enabled: true
    path: /var/log/tavern/access.log
    encrypt:
      enabled: false
      secret: "123"
```

**特性：**
- 可选的 AES 加密 (保护敏感 URL 信息)
- 日志轮转 (lumberjack: max_size, max_backups, max_age, compress)

### 5.6 内部路由 / Internal Routes

| 路径 / Path | 用途 / Purpose | 访问限制 / Access |
|:---|:---|:---|
| `/metrics` | Prometheus 指标导出 | `local_api_allow_hosts` |
| `/healthz` | 健康检查 | `local_api_allow_hosts` |
| `/version` | 版本信息 | `local_api_allow_hosts` |
| `/debug/pprof/` | 性能分析 | Basic Auth |

**local_api_allow_hosts:**
```yaml
server:
  local_api_allow_hosts:
    - "localhost"
    - "127.0.0.1"
    - "127.1"
```

---

## 6. 配置速览 / Configuration Quick Reference

```yaml
strict: true                                # 严格模式
logger:
  level: info                               # debug/info/warn/error
  path: /var/log/tavern/tavern.log
  max_size: 10                              # 日志文件最大 MB
  max_backups: 10                           # 保留日志数
  max_age: 1                                # 保留天数
  compress: false

server:
  addr: ":8080"
  read_timeout: 60s
  write_timeout: 60s
  idle_timeout: 90s
  read_header_timeout: 30s
  max_header_bytes: 1048576
  pprof: { username: "admin", password: "password" }
  local_api_allow_hosts: ["localhost", "127.0.0.1"]
  middleware:
    - name: recovery
    - name: rewrite
    - name: multirange
    - name: caching
      options:
        fuzzy_refresh: true
        collapsed_request: true
        vary_limit: 100
        object_pool_size: 20000
  access_log:
    enabled: true
    path: /var/log/tavern/access.log
    encrypt: { enabled: false }

plugin:
  - name: qs-plugin
  - name: purge
    options:
      allow_hosts: ["127.0.0.1"]
  - name: verifier
    options:
      endpoint: https://crc-svc.omalloc.com/receive

upstream:
  balancing: wrr
  address: ["http://127.0.0.1:8000"]
  max_idle_conns: 1000
  max_idle_conns_per_host: 500

storage:
  driver: native
  db_type: pebble
  eviction_policy: lru
  selection_policy: hashring
  slice_size: 1048576
  migration:
    enabled: false
  diraware:
    enabled: true
  buckets:
    - path: /cache1
      type: warm
      max_object_limit: 10000000
```

---

## 7. 相关文档 / Related Documents

- [Tavern 项目文档 / Tavern Project](./01-project.md)
- [Tavern 架构文档 / Tavern Architecture](./03-architecture.md)
- [PURGE 设计 / PURGE Design](../purge.md)
- [生态概览 / Ecosystem Overview](../ecosystem/overview.md)

---

*Document generated: 2026-06-09 | Source: config.example.yaml, README.md, CHANGELOG.md, source code*
