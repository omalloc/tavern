# Tavern 架构文档 / Tavern Architecture Documentation

> 本文档深入描述 Tavern (L2 Cache) 的内部架构、组件设计、数据流和核心算法的实现细节。
> 所有代码路径引用使用 `file.go:line` 格式。

---

## 1. 整体架构 / Overall Architecture

```mermaid
graph TB
    subgraph "外部 / External"
        Client[客户端 Client]
        GW_L1[Gateway L1<br/>:80/:443]
        GW_L3[Gateway L3<br/>:8000]
        Origin[源站 Origin]
    end

    subgraph "Tavern Core"
        Server[HTTPServer<br/>server/server.go]
        Router[Internal Router<br/>/metrics, /healthz, /pprof]

        subgraph "Middleware Chain / 中间件链"
            Recovery[Recovery<br/>Panic 恢复]
            Rewrite[Rewrite<br/>Header 重写]
            MultiRange[MultiRange<br/>Range 支持]
            Caching[Caching<br/>缓存核心]
        end

        Proxy[Proxy Layer<br/>proxy/proxy.go]

        subgraph "Plugin System / 插件系统"
            PurgePlugin[Purge<br/>缓存清除]
            QSPlugin[QS<br/>查询统计]
            VerifierPlugin[Verifier<br/>CRC 校验]
        end
    end

    subgraph "Storage Layer / 存储层"
        StorageAPI[Storage Interface<br/>api/defined/v1/storage/]
        NativeStorage[nativeStorage<br/>storage/storage.go]

        subgraph "Components / 组件"
            Selector[Bucket Selector<br/>hashring/roundrobin]
            IndexDB[IndexDB<br/>PebbleDB/NutsDB]
            Buckets[Buckets<br/>disk/memory/hot/cold]
            SharedKV[SharedKV<br/>mem/store]
            DirAware[DirAware<br/>目录感知]
        end

        subgraph "Persistence / 持久化"
            Disk[disk/<br/>分块文件存储]
            PebbleDB[PebbleDB<br/>LSM-Tree 索引]
            StoreKV[StoreSharedKV<br/>持久化 KV]
        end
    end

    subgraph "Framework / 内部框架"
        App[App Lifecycle<br/>contrib/kratos/]
        Logger[Structured Logger<br/>contrib/log/]
        Config[Config Loader<br/>contrib/config/]
    end

    Client --> GW_L1
    GW_L1 --> Server
    Server --> Router
    Server --> Recovery
    Recovery --> Rewrite
    Rewrite --> MultiRange
    MultiRange --> Caching
    Caching --> Proxy
    Proxy --> GW_L3
    GW_L3 --> Origin

    Caching <--> StorageAPI
    StorageAPI --> NativeStorage
    NativeStorage --> Selector
    NativeStorage --> IndexDB
    NativeStorage --> Buckets
    NativeStorage --> SharedKV
    NativeStorage --> DirAware
    IndexDB --> PebbleDB
    SharedKV --> StoreKV
    Buckets --> Disk

    PurgePlugin --> NativeStorage
    QSPlugin --> NativeStorage
    VerifierPlugin --> NativeStorage

    App --> Server
    App --> Logger
    App --> Config
```

---

## 2. 请求生命周期 / Request Lifecycle

### 2.1 完整时序图 / Full Sequence Diagram

```mermaid
sequenceDiagram
    participant Client as HTTP Client
    participant Server as HTTPServer
    participant Router as Internal Router
    participant MW as Middleware Chain
    participant Cache as Caching MW
    participant Plugin as Plugin Handlers
    participant Proxy as Proxy Layer
    participant Storage as Storage Layer
    participant L3 as Gateway L3

    Client->>Server: HTTP Request

    Note over Server: server/mod/wrap.go
    Server->>Server: 注入 Trace ID
    Server->>Server: 提取请求上下文
    Server->>Server: 准备 Response Recorder

    Server->>Router: route?

    alt Internal Route (/metrics, /healthz, ...)
        Router->>Router: 检查 local_api_allow_hosts
        Router->>Server: 内部响应
    else Cache Pipeline (所有其他请求)
        Router->>MW: 进入中间件链

        Note over MW: === Middleware Onion ===
        MW->>MW: Recovery.RoundTrip()
        Note over MW: defer+recover() 捕获 panic

        MW->>MW: Rewrite.RoundTrip()
        Note over MW: 应用 request_headers_rewrite
        Note over MW: 修改请求 Header

        MW->>MW: MultiRange.RoundTrip()
        Note over MW: 处理 Multi-Range 请求头
        Note over MW: merge: false (分别返回)

        MW->>Cache: Caching.RoundTrip()

        Note over Cache: === 缓存查找 / Cache Lookup ===
        Cache->>Cache: 计算缓存 Key
        Note over Cache: URL + Vary + Query<br/>配置: include_query_in_cache_key

        Cache->>Cache: 查找 IndexDB 元数据
        Note over Cache: IndexDB.Get(cacheKey)

        alt 缓存命中 (HIT)
            Cache->>Storage: Lookup(objectID)
            Storage->>Cache: Metadata (含文件路径/Chunk 信息)
            Cache->>Cache: 检查过期 & CRC
            Cache->>Cache: fuzzy_refresh? → 异步回源
            Note over Cache: 检查 X-Prefetch 头

            Cache->>Storage: ReadChunkFile(objectID, chunkIndex)
            Storage->>Cache: File Reader (io.ReadCloser)
            Cache->>Server: 200 OK (X-Cache: HIT from disk)

        else 缓存未命中 (MISS)
            Note over Cache: === 请求合并 / Request Collapsing ===
            Cache->>Cache: collapsed_request?
            Note over Cache: singleflight.Do(cacheKey)
            alt 等待已有请求
                Cache->>Cache: 阻塞等待 (collapsed_request_wait_timeout)
            else 发起新请求
                Cache->>Proxy: RoundTrip (回源)

                Note over Proxy: proxy/proxy.go
                Proxy->>Proxy: 选择上游节点
                Note over Proxy: balancing: wrr → Selector

                Proxy->>L3: HTTP Request → Gateway L3 :8000
                L3-->>Proxy: Response

                Note over Cache: === 缓存存储 / Cache Store ===
                Cache->>Cache: 检查响应可缓存性
                Note over Cache: Status, Cache-Control,<br/>TR-ERRCODE, etc.

                Cache->>Storage: Bucket.Select(id)
                Storage->>Cache: Target Bucket

                Cache->>Storage: WriteChunkFile(id, chunkIndex)
                Note over Storage: 分块写入 (slice_size)<br/>每个 chunk 独立文件

                loop 每个 Chunk
                    Cache->>Storage: Write(data)
                    Note over Storage: 异步 flush (async_flush_chunk)
                end

                Cache->>Storage: Store(metadata)
                Note over Storage: 写入 IndexDB 元数据<br/>更新 SharedKV 倒排索引

                Cache->>Cache: verifier 异步 CRC 校验
            end
            Cache->>Server: 200 OK (X-Cache: MISS)
        end

        Note over MW: Rewrite 响应头重写
        Note over MW: set/add/remove response headers

        Server->>Server: ResponseRecorder.Flush()
        Server->>Server: 记录 Access Log
        Server->>Client: HTTP Response
    end
```

### 2.2 关键代码路径 / Key Code Paths

| 步骤 / Step | 文件 / File | 函数 / Function |
|:---|:---|:---|
| 请求填充 / Request Filling | `server/mod/wrap.go` | `RequestFiller` |
| 追踪注入 / Trace Injection | `server/mod/wrap.go` | `InjectTrace` |
| 响应记录 / Response Recording | `server/mod/wrap.go` | `ResponseRecorder` |
| 中间件链执行 / Middleware Chain | `server/middleware/middleware.go` | `Chain()` |
| 缓存 Key 计算 / Cache Key | `server/middleware/caching/caching.go` | `cacheKey()` |
| 缓存查找 / Cache Lookup | `server/middleware/caching/caching.go` | `lookup()` |
| 请求合并 / Request Collapsing | `server/middleware/caching/locker.go` | `collapsedRequest()` |
| 回源 / Origin Fetch | `proxy/proxy.go` | `RoundTrip()` |
| 上游选择 / Upstream Select | `proxy/proxy.go` | `selectNode()` |
| 缓存写入 / Cache Store | `server/middleware/caching/processor.go` | `store()` |
| 响应返回 / Response | `server/middleware/caching/caching.go` | `RoundTrip()` |

---

## 3. 中间件链 / Middleware Chain

### 3.1 洋葱模型 / Onion Model

```mermaid
graph LR
    subgraph "请求流 / Request Flow"
        direction TB
        R1[HTTP Request] --> M1
        M1[Recovery] --> M2
        M2[Rewrite] --> M3
        M3[MultiRange] --> M4
        M4[Caching] --> Core
        Core[(Proxy<br/>回源)] --> M4R
        M4R[Caching<br/>响应处理] --> M3R
        M3R[MultiRange<br/>响应处理] --> M2R
        M2R[Rewrite<br/>响应处理] --> M1R
        M1R[Recovery<br/>defer+recover] --> R2
        R2[HTTP Response]
    end

    style M1 fill:#ff9999
    style M2 fill:#99ccff
    style M3 fill:#99ff99
    style M4 fill:#ffcc99
```

### 3.2 中间件接口定义 / Middleware Interface

**文件：** `server/middleware/middleware.go`

```go
// Middleware 是一个处理函数，包装一个 http.RoundTripper 返回新的 http.RoundTripper
type Middleware func(http.RoundTripper) http.RoundTripper

// Factory 是中间件工厂函数
type Factory func(*configv1.Middleware) (middleware Middleware, cleanup func(), err error)

// Chain 将多个 Middleware 组合成一个链
// 执行顺序：m[0] 在最外层，最后注册的在内层
func Chain(m ...Middleware) Middleware {
    return func(next http.RoundTripper) http.RoundTripper {
        for i := len(m) - 1; i >= 0; i-- {
            next = m[i](next)
        }
        return next
    }
}
```

**注册机制：**

```go
// server/middleware/registry.go
var registry = map[string]Factory{}

func RegisterFactory(name string, f Factory) {
    registry[name] = f
}

// 各中间件包 init() 中自动注册
// server/middleware/caching/ → init() → RegisterFactory("caching", factory)
// server/middleware/recovery/ → init() → RegisterFactory("recovery", factory)
```

### 3.3 各中间件详解 / Middleware Details

#### 3.3.1 Recovery — Panic 恢复

**文件：** `server/middleware/recovery/recovery.go`

```go
func (r *recovery) RoundTrip(req *http.Request) (*http.Response, error) {
    defer func() {
        if err := recover(); err != nil {
            // 记录 panic 栈信息
            // 返回 500 响应
        }
    }()
    return r.next.RoundTrip(req)
}
```

**配置：**
- `fail_count_threshold`: 时间窗口内 panic 次数阈值
- `fail_window`: 统计窗口（秒），超阈值后熔断

#### 3.3.2 Rewrite — Header 重写

**文件：** `server/middleware/rewrite/rewrite.go`

**操作类型：**
- `set`: 覆盖设置 Header
- `add`: 追加 Header（可多次）
- `remove`: 删除 Header

**执行时机：**
- 请求方向：在调用 `next.RoundTrip(req)` 前修改 `req.Header`
- 响应方向：在获得响应后修改 `resp.Header`

#### 3.3.3 MultiRange — 多区间支持

**文件：** `server/middleware/multirange/multirange.go`

```go
type MultiRange struct {
    merge bool  // 是否合并多个 range 为单一响应
}
```

**行为：**
- `merge: false` (默认): 每个 Range 返回独立的 Part
- 处理 `Range: bytes=0-100,200-300` 格式

#### 3.3.4 Caching — 缓存核心

**文件：** `server/middleware/caching/` (15+ 文件)

这是最复杂的中间件。详见下节。

---

## 4. 缓存中间件深度分析 / Caching Middleware Deep Dive

### 4.1 文件组成 / File Structure

```
server/middleware/caching/
├── caching.go              # 主入口: RoundTrip, cacheKey, lookup
├── processor.go            # 缓存处理器: preCacheProcessor, storeProcess
├── caching_state.go        # 缓存状态管理: Caching struct
├── caching_prefetch.go     # 预取逻辑
├── caching_vary.go         # Vary 多版本处理
├── caching_fillrange.go    # Range 填充
├── caching_fuzzy_test.go   # 模糊刷新
├── caching_revalidate.go   # 异步回源校验
├── caching_chunkpart_test.go # Chunk 分块测试
├── caching_filechanged.go  # 文件变更检测
├── internal.go             # 内部工具函数
├── internal_checksum.go    # CRC/xxhash 校验
├── locker.go               # 请求合并锁
└── metrics.go              # 缓存层 Prometheus 指标
```

### 4.2 缓存 Key 计算 / Cache Key Computation

```go
// 缓存 Key 由以下因素决定:
cacheKey = hash(
    req.URL.String(),           // 完整 URL
    + include_query_in_cache_key  // 是否包含查询参数
    + vary_headers                // Vary 头值
    + vary_ignore_key             // 排除的 Vary 头列表
)
```

**配置影响：**
- `include_query_in_cache_key: true` → `/path?a=1` 和 `/path?a=2` 视为不同资源
- `vary_ignore_key: ["Cookie"]` → 忽略 Cookie 变化，避免版本爆炸

### 4.3 请求合并流程 / Request Collapsing Flow

```mermaid
sequenceDiagram
    participant R1 as Request 1
    participant R2 as Request 2
    participant R3 as Request 3
    participant SF as Singleflight
    participant Proxy as Proxy (回源)

    R1->>SF: Do(cacheKey)
    Note over SF: Key 不存在<br/>发起回源
    SF->>Proxy: 回源请求

    R2->>SF: Do(cacheKey)
    Note over SF: Key 已存在<br/>等待 R1 完成
    SF-->>R2: 阻塞等待 (collapsed_request_wait_timeout)

    R3->>SF: Do(cacheKey)
    SF-->>R3: 阻塞等待

    Proxy-->>SF: Response (TeeReader)

    SF->>SF: io.TeeReader 分叉<br/>同时写入多个等待者

    SF-->>R1: Response Body (TeeReader)
    SF-->>R2: Response Body (TeeReader)
    SF-->>R3: Response Body (TeeReader)
```

**实现：** `proxy/singleflight/`

```go
// 自定义 singleflight (非标准库)，支持:
// - io.TeeReader: 将回源响应体同时发送给所有等待者
// - 超时控制: collapsed_request_wait_timeout
// - context 取消传播
```

### 4.4 缓存写入流程 / Cache Store Flow

```mermaid
sequenceDiagram
    participant Cache as Caching MW
    participant Sel as Bucket Selector
    participant Bucket as Bucket (Disk)
    participant IDB as IndexDB
    participant SKV as SharedKV

    Cache->>Cache: 确认响应可缓存<br/>(Status, Cache-Control, TR-ERRCODE)

    Cache->>Sel: Select(objectID)
    Sel->>Sel: hashring/roundrobin 算法
    Sel->>Cache: Target Bucket

    Cache->>Bucket: WriteChunkFile(id, chunkIndex=0)
    Bucket->>Bucket: 创建 chunk 文件<br/>path: /cache1/<hash>/<chunk>.data
    Bucket-->>Cache: io.WriteCloser

    loop 写入响应体 (streaming)
        Cache->>Bucket: Write(bodyChunk)
        Note over Bucket: async_flush_chunk?<br/>异步写入
    end

    Cache->>Bucket: Close()

    Cache->>IDB: Set(key, metadata)
    Note over IDB: PebbleDB/NutsDB 持久化<br/>metadata: URL, Headers,<br/>ExpiresAt, Hash, Chunks[]

    Cache->>SKV: Set("ix/<bucketID>/<url>", hash)
    Note over SKV: 倒排索引<br/>用于高效 DIR Purge

    Cache->>SKV: Incr("if/domain/<host>", 1)
    Note over SKV: 域名计数器<br/>用于 DIR Purge 门控
```

### 4.5 异步校验 (Revalidation) / Async Revalidation

**触发条件：**
1. 缓存对象已过期 (ExpiresAt < Now)
2. Fuzzy Refresh 触发

**流程：**
```go
// server/middleware/caching/caching_revalidate.go
func (c *Caching) revalidate(req, cachedMeta) {
    // 1. 发起异步回源请求 (带 If-None-Match / If-Modified-Since)
    // 2. 源站 304 → 更新 ExpiresAt，不清除缓存内容
    // 3. 源站 200 → 替换缓存内容
    // 4. 等待期间: 继续使用过期缓存版本 (stale-while-revalidate)
}
```

### 4.6 文件变更检测 / File Change Detection

**文件：** `server/middleware/caching/caching_filechanged.go`

**行为：**
- 缓存响应时记录 `ETag` 和 `Last-Modified`
- 下次回源校验时对比，检测源站文件是否已变更
- EdgeMode: 比较 CRC checksum

---

## 5. 存储层架构 / Storage Layer Architecture

### 5.1 分层架构图 / Layered Architecture

```mermaid
graph TB
    subgraph "Interface Layer / 接口层"
        SI[Storage Interface<br/>api/defined/v1/storage/]
        BI[Bucket Interface]
        II[IndexDB Interface]
        SKI[SharedKV Interface]
        MI[Migration Interface]
    end

    subgraph "Implementation Layer / 实现层"
        NS[nativeStorage<br/>storage/storage.go]
        Migrator[Migrator<br/>storage/migrator.go]
        Selector[Bucket Selector<br/>hashring/roundrobin]
        Diraware[DirAware Wrapper<br/>storage/diraware/]

        subgraph "Bucket Implementations / 桶实现"
            Disk[Disk Bucket<br/>分块文件存储]
            Memory[Memory Bucket<br/>内存存储]
            RawDisk[RawDisk Bucket<br/>裸盘存储]
            Empty[Empty Bucket<br/>NOP]
        end

        subgraph "Index Implementations / 索引实现"
            PebbleDB[PebbleDB<br/>CockroachDB LSM-Tree]
            NutsDB[NutsDB<br/>LSM-Tree]
        end

        subgraph "SharedKV Implementations / 共享KV实现"
            MemKV[MemSharedKV<br/>内存 KV]
            StoreKV[StoreSharedKV<br/>持久化 KV]
        end
    end

    SI --> NS
    SI --> Migrator
    BI --> Disk
    BI --> Memory
    BI --> RawDisk
    BI --> Empty
    II --> PebbleDB
    II --> NutsDB
    SKI --> MemKV
    SKI --> StoreKV
    MI --> Migrator
```

### 5.2 核心接口定义 / Core Interface Definitions

#### Storage 接口

**文件：** `api/defined/v1/storage/storage.go:55-64`

```go
type Storage interface {
    io.Closer
    Selector                                    // 桶选择

    Buckets() []Bucket                          // 获取所有桶
    SharedKV() SharedKV                         // 共享 KV 存储
    PURGE(storeUrl string, typ PurgeControl) error  // 缓存清除
}
```

#### Bucket 接口

**文件：** `api/defined/v1/storage/storage.go:80-104`

```go
type Bucket interface {
    io.Closer
    Operation                                    // 对象操作 (Lookup, Store, Discard...)

    ID() string                                  // 桶 ID
    Weight() int                                 // 权重 (0-1000)
    Allow() int                                  // 允许使用比例 (0-100)
    UseAllow() bool
    Objects() uint64                             // 对象总数
    HasBad() bool                                // 是否异常
    Type() string                                // 桶类型 (disk/memory/rawdisk/empty)
    StoreType() string                           // 存储分层 (hot/cold/warm)
    Path() string                                // 存储路径
    TopK(k int) []string                         // TopK 热门 Key
}
```

#### Operation 接口

**文件：** `api/defined/v1/storage/storage.go:20-53`

```go
type Operation interface {
    Lookup(ctx, id) (*object.Metadata, error)
    Touch(ctx, id)
    Store(ctx, meta) error
    Exist(ctx, id) bool
    Remove(ctx, id) error                        // 软删除
    Discard(ctx, id) error                       // 硬删除
    DiscardWithHash(ctx, hash) error             // 按 Hash 硬删除
    DiscardWithMessage(ctx, id, msg) error       // 带消息删除
    DiscardWithMetadata(ctx, meta) error         // 按 Metadata 删除
    Iterate(ctx, fn) error
    Expired(ctx, id, md) bool
    WriteChunkFile(ctx, id, index) (io.WriteCloser, string, error)
    ReadChunkFile(ctx, id, index) (File, string, error)
    Migrate(ctx, id, dest) error                 // 迁移到目标桶
    SetMigration(m) error
}
```

#### IndexDB 接口

**文件：** `api/defined/v1/storage/indexdb.go:25-57`

```go
type IndexDB interface {
    io.Closer
    Get(ctx, key) (*object.Metadata, error)      // 获取元数据
    Set(ctx, key, val) error                     // 存储元数据
    Exist(ctx, key) bool                         // 检查存在
    Delete(ctx, key) error                       // 删除
    Iterate(ctx, prefix, fn) error               // 前缀遍历
    Expired(ctx, fn) error                       // 过期条目遍历
    GC(ctx) error                                // 垃圾回收
}
```

#### SharedKV 接口

**文件：** `api/defined/v1/storage/storage.go:126-147`

```go
type SharedKV interface {
    io.Closer
    Get(ctx, key) ([]byte, error)
    Set(ctx, key, val) error
    Incr(ctx, key, delta) (uint32, error)        // 原子递增
    Decr(ctx, key, delta) (uint32, error)        // 原子递减
    GetCounter(ctx, key) (uint32, error)
    Delete(ctx, key) error
    DropPrefix(ctx, prefix) error                // 按前缀批量删除
    Iterate(ctx, fn) error                       // 全量遍历
    IteratePrefix(ctx, prefix, fn) error         // 按前缀遍历
}
```

### 5.3 nativeStorage 实现 / nativeStorage Implementation

**文件：** `storage/storage.go`

```go
type nativeStorage struct {
    closed       bool
    mu           sync.Mutex
    log          *log.Helper

    selector     storage.Selector        // 桶选择器 (hashring/roundrobin)
    sharedkv     storage.SharedKV        // 跨桶 KV 存储
    nopBucket    storage.Bucket          // 空桶 (后备)
    memoryBucket storage.Bucket          // 内存桶 (仅限一个)
    hotBucket    []storage.Bucket        // 热数据桶
    warmlBucket  []storage.Bucket        // 暖数据桶 (主存储)
}
```

**初始化流程：**
```
New(config, logger)
  ├─ FillDefault() ← 填充配置默认值
  ├─ Migration enabled? → NewMigrator(config, logger)
  ├─ Create buckets (按配置)
  │   ├─ warm/normal → warmlBucket[]
  │   ├─ hot → hotBucket[]
  │   └─ memory → memoryBucket (唯一)
  ├─ Create selector (hashring/roundrobin)
  ├─ DirAware enabled?
  │   └─ Replace SharedKV with StoreSharedKV (持久化)
  │   └─ Wrap with diraware.New(n, checker)
  └─ Return Storage
```

### 5.4 分块文件存储 / Chunked File Storage

**磁盘桶实现：** `storage/bucket/disk/disk.go`

```
/cache1/
├── <hash_prefix>/
│   ├── <hash>/
│   │   ├── 0000000000.chunk    # 第 0 个 chunk (slice_size bytes)
│   │   ├── 0000000001.chunk    # 第 1 个 chunk
│   │   ├── 0000000002.chunk    # 第 2 个 chunk
│   │   └── ...
│   └── .indexdb/               # 该桶的 IndexDB
└── .diraware/                  # DirAware KV 存储
```

**分块策略：**
- `slice_size` (默认 1MB): 每个 chunk 文件的最大字节数
- 大文件自动跨多个 chunk
- Range 请求可以部分命中 (PART_HIT: 某些 chunk 已缓存)

### 5.5 PURGE 流程详解 / PURGE Flow Detail

```mermaid
flowchart TD
    A["PURGE Request<br/>plugin/purge/purge.go"] --> B{Source IP<br/>in allow_hosts?}
    B -- No --> C[403 Forbidden]
    B -- Yes --> D[Compute storeUrl]
    D --> E{Parse Purge-Type}

    E --> F{Dir?}

    F -- No (File) --> G{Strategy?}
    G -- Hard --> H["bucket.Discard(id)<br/>删除文件+元数据"]
    G -- Soft --> I["bucket.Lookup(id)<br/>设 ExpiresAt=过去<br/>bucket.Store(md)"]

    F -- Yes (Dir) --> J{MarkExpired?}
    J -- Yes --> K["return nil<br/>(当前空操作)"]
    J -- No --> L["遍历 Buckets<br/>SharedKV.IteratePrefix<br/>'ix/bucketID/prefix'"]

    L --> M{Has Index<br/>Hits?}
    M -- Yes --> N["DiscardWithHash(hash)<br/>Delete index mapping"]
    M -- No --> O["Fallback: Iterate IndexDB<br/>匹配 Path 前缀"]
    O --> P{Hard?}
    P -- Yes --> Q["bucket.DiscardWithMetadata"]
    P -- No --> R["Set ExpiresAt=过去<br/>bucket.Store(md)"]

    N --> S{processed == 0?}
    Q --> S
    R --> S
    S -- Yes --> T[ErrKeyNotFound → 404]
    S -- No --> U[200 OK]

    H --> U
    I --> U
```

### 5.6 冷热迁移状态机 / Tiering State Machine

```mermaid
stateDiagram-v2
    [*] --> Cold: 初始缓存
    Cold --> Warm: Promote<br/>(window 内 hits ≥ min_hits)
    Warm --> Hot: Promote<br/>(window 内 hits ≥ min_hits)
    Hot --> Warm: Demote<br/>(window 内 hits ≤ min_hits<br/>+ occupancy > threshold)
    Warm --> Cold: Demote<br/>(window 内 hits ≤ min_hits<br/>+ occupancy > threshold)

    note right of Hot: 最快存储层
    note right of Cold: 最慢存储层
```

---

## 6. 代理层 / Proxy Layer

### 6.1 架构

**文件：** `proxy/proxy.go`

```
Proxy Layer
├── Upstream Selector (omalloc/proxy)
│   ├── WRR (Weighted Round Robin)
│   └── Once (固定选择)
├── Connection Pool
│   ├── Per-address http.Client
│   ├── max_idle_conns / max_idle_conns_per_host
│   └── max_connections_per_server
├── Transport
│   ├── TCP
│   └── Unix Socket
└── Singleflight
    └── Request coalescing on cache misses
```

### 6.2 上游节点选择 / Upstream Node Selection

```go
// proxy/proxy.go
func (p *Proxy) RoundTrip(req *http.Request) (*http.Response, error) {
    // 1. 检查是否有动态 TR-UPS-ADDR 头（L1/L2 指定）
    if upsAddr := req.Header.Get("X-Ups-Addr"); upsAddr != "" {
        req.URL.Host = upsAddr
    }

    // 2. 通过 selector 选择上游节点
    node := p.selector.Select(req.Context(), req.URL)

    // 3. 从连接池获取对应地址的 http.Client
    client := p.getClient(node.Address)

    // 4. 发起请求
    return client.Do(req)
}
```

---

## 7. 插件系统 / Plugin System

### 7.1 插件接口 / Plugin Interface

**文件：** `api/defined/v1/plugin/`

```go
type Plugin interface {
    // 注册 HTTP 路由
    AddRouter(r *http.ServeMux) error
    // 注册 HTTP Handler（在中间件链之后）
    HandleFunc(pattern string, handler func(http.ResponseWriter, *http.Request))
    // 生命周期
    Start(ctx context.Context) error
    Stop(ctx context.Context) error
}
```

### 7.2 插件生命周期 / Plugin Lifecycle

```mermaid
sequenceDiagram
    participant Main as main.go
    participant Registry as Plugin Registry
    participant Plugin as Plugin Instance

    Note over Main: init() 阶段
    Main->>Registry: blank import plugins
    Note over Registry: plugin/purge/init()<br/>plugin/qs/init()<br/>plugin/verifier/init()

    Note over Main: 启动阶段
    Main->>Registry: Load configured plugins
    Registry->>Plugin: New(config, logger)
    Plugin->>Plugin: Validate config

    Main->>Plugin: AddRouter(mux)
    Plugin->>Plugin: Register HTTP endpoints

    Main->>Plugin: Start(ctx)
    Plugin->>Plugin: Initialize internal state

    Note over Plugin: 运行阶段
    Plugin->>Plugin: Handle HTTP requests

    Note over Main: 关闭阶段
    Main->>Plugin: Stop(ctx)
    Plugin->>Plugin: Cleanup resources
```

### 7.3 内置插件详情 / Built-in Plugins

#### Purge 插件 / Purge Plugin

**文件：** `plugin/purge/purge.go`

| 特性 / Feature | 详情 / Detail |
|:---|:---|
| **HTTP 方法** | 仅处理 `PURGE` 方法 |
| **IP 白名单** | `allow_hosts` — 非白名单返回 403 |
| **Purge-Type 解析** | `file` / `dir` + `,hard` 后缀 |
| **Store URL 覆盖** | 通过 `i-x-store-url` 头自定义存储 Key |
| **错误处理** | 404 (对象不存在), 500 (内部错误), 200 (成功) |

#### QS (Query Stats) 插件 / QS Plugin

**文件：** `plugin/qs/`

| 特性 / Feature | 详情 / Detail |
|:---|:---|
| **SSE 端点** | 实时推送缓存统计数据 |
| **TopK 跟踪** | 追踪热门 URL (LRU TopK) |
| **指标导出** | `/metrics` 端点 (CPU, 内存, 请求速率) |
| **RPS 平滑** | 加权平滑请求速率计算 |
| **ttop 集成** | 为 `ttop` CLI 工具提供 SSE 数据源 |

#### Verifier 插件 / Verifier Plugin

**文件：** `plugin/verifier/`

| 特性 / Feature | 详情 / Detail |
|:---|:---|
| **Hash 算法** | xxhash (v1.1, 原为 md5) |
| **上报方式** | HTTP POST 到外部 CRC 校验服务 |
| **采样比例** | `report_ratio` (0-100%) |
| **异步执行** | 不阻塞缓存响应路径 |
| **超时控制** | `timeout` 秒 |

---

## 8. 内部框架 / Internal Framework

### 8.1 App 生命周期 / App Lifecycle

**文件：** `contrib/kratos/app.go`

```go
// Kratos-inspired App 生命周期
type App struct {
    opts     options
    ctx      context.Context
    cancel   func()
    instance *registry.ServiceInstance
}

func New(opts ...Option) *App

func (a *App) Run() error {
    // 1. Execute Start hooks (before)
    // 2. Handle OS signals (SIGINT, SIGTERM, SIGUSR2, SIGHUP)
    // 3. Execute Stop hooks (after)
}
```

**启动流程 (main.go):**
```
1. flag.Parse() ← 解析命令行参数
2. config.New(file.NewSource(flagConf)) ← 加载 YAML 配置
3. c.Scan(bc) ← 解析到 Bootstrap struct
4. log.NewHelper(logger) ← 初始化日志
5. storage.New(config.Storage, logger) ← 初始化存储
6. proxy.New(config.Upstream, logger) ← 初始化代理
7. plugin.LoadPlugins(config.Plugin) ← 加载插件
8. server.New(config.Server, ...) ← 创建 HTTP 服务器
9. app.Run() ← 启动应用生命周期
   ├─ tableflip.Upgrade() ← 平滑升级 (SIGUSR2)
   └─ graceful shutdown ← 优雅关闭 (SIGINT/SIGTERM)
```

### 8.2 配置系统 / Configuration System

**文件：** `contrib/config/`

```
Config Source (file/remote)
  → Provider (Watcher)
    → Scan to Bootstrap struct
      → Inject to components
```

**特性：**
- 支持 file provider (本地 YAML 文件)
- 支持 remote provider (远程配置, 可扩展)
- Change Watcher (配置变更监听)
- 配置合并与默认值填充

### 8.3 日志系统 / Logging System

**文件：** `contrib/log/`

| 特性 / Feature | 详情 / Detail |
|:---|:---|
| **结构化日志** | Key-Value 格式 |
| **日志轮转** | lumberjack (max_size, max_backups, max_age, compress) |
| **级别过滤** | debug / info / warn / error |
| **上下文传播** | Trace ID, Request ID |
| **调用者信息** | caller: true 输出文件名和行号 |

---

## 9. 相关文档 / Related Documents

- [Tavern 项目文档 / Tavern Project](./01-project.md)
- [Tavern 功能文档 / Tavern Features](./02-features.md)
- [生态概览 / Ecosystem Overview](../ecosystem/overview.md)
- [协议规范 / Protocol Specification](../ecosystem/protocol.md)
- [PURGE 设计 / PURGE Design](../purge.md)
- [CDN 缓存分析 / CDN Cache Analysis](../cdn-cache-analysis.md)

---

*Document generated: 2026-06-09 | Source: complete source code analysis of tavern repository*
