<h1 align="center">Tavern</h1>

<p align="center">
  <strong>面向 CDN 边缘分发与源站保护的自托管 HTTP 缓存。</strong><br>
  合并重复回源、高效分发部分内容，并用一个聚焦缓存的 Go 服务运维边缘节点。
</p>

<p align="center">
  <a href="https://github.com/omalloc/tavern/actions/workflows/go.yml"><img src="https://github.com/omalloc/tavern/actions/workflows/go.yml/badge.svg?branch=main" alt="构建状态"></a>
  <a href="https://pkg.go.dev/github.com/omalloc/tavern"><img src="https://pkg.go.dev/badge/github.com/omalloc/tavern" alt="Go 文档"></a>
  <a href="https://goreportcard.com/report/github.com/omalloc/tavern"><img src="https://goreportcard.com/badge/github.com/omalloc/tavern" alt="Go Report Card"></a>
  <a href="https://codecov.io/gh/omalloc/tavern"><img src="https://codecov.io/gh/omalloc/tavern/main/graph/badge.svg" alt="代码覆盖率"></a>
  <a href="https://github.com/omalloc/tavern/blob/main/LICENSE"><img src="https://img.shields.io/github/license/omalloc/tavern" alt="许可证"></a>
</p>

<p align="center">
  <a href="docs/README.md">项目文档</a> ·
  <a href="config.example.yaml">配置示例</a> ·
  <a href="CHANGELOG.md">变更记录</a> ·
  <a href="README.md">English</a>
</p>

<p align="center">
  <a href="https://tavern.omalloc.com/">
    <img src="docs/dist/app_banner.png" alt="Tavern HTTP 缓存" width="100%">
  </a>
</p>

## 项目概览

Tavern 是一个面向 CDN 边缘缓存和源站屏蔽层的反向缓存代理。将它部署在网关或负载均衡器之后，可就近返回已缓存的响应、吸收并发缓存 MISS，并避免对象集中到期转化为源站流量尖峰。

> [!NOTE]
> Tavern 负责边缘架构中的**对象分发与缓存生命周期**，不提供 DNS、全局流量调度、API 网关或 CDN 控制面。

```text
客户端
   │
   ▼
网关 / 负载均衡
   │
   ▼
Tavern ── 缓存命中 ───────────────► 返回响应
   │
   └──── 受控回源 / 重校验 ──────────► 源站
```

Tavern 以不依赖 CGO 的 Go 二进制交付，使用 YAML 配置。既可以运行单个缓存节点，也可以把同一服务横向部署到边缘节点集群。

## 为什么选择 Tavern？

| 关注点 | Tavern 的处理方式 | 运维效果 |
|:---|:---|:---|
| **源站保护** | 合并同一对象的并发全量 MISS；把并发 Range MISS 合并为一次联合区间回源；在硬过期前刷新对象 | 减少重复回源，并让重校验流量更平滑 |
| **部分内容分发** | 缓存单区间、后缀区间、开放区间和多区间请求；复用已有切片并补齐缺失区间 | 减少安装包、软件包和媒体对象的重复传输 |
| **显式缓存生命周期** | 支持 URL/目录 `PURGE`、软过期/硬删除、`Vary` 版本上限、淘汰与存储分层 | 缓存失效与存储行为更可控 |
| **缓存节点运维** | 提供 Prometheus、健康检查、性能分析、结构化日志、终端实时视图和二进制平滑切换 | 减少运维缓存节点所需的外围组件 |

NGINX、Varnish、Squid 和 Apache Traffic Server 也以不同形式提供其中部分能力。Tavern 的定位是把这些能力整合成一个紧凑、Go 原生的边缘缓存，而不是宣称每项功能都为 Tavern 独有。

## 快速开始

### 环境要求

- Go 1.26+
- Linux 或 macOS
- 一个用于提供可缓存内容的 HTTP 源站

### 编译并运行

```bash
git clone https://github.com/omalloc/tavern.git
cd tavern

cp config.example.yaml config.yaml
# 根据当前主机设置 upstream.address、存储路径和日志路径。

make build
./bin/tavern -c config.yaml
```

在另一个终端验证本地管理端点：

```bash
curl http://localhost:8080/healthz
curl http://localhost:8080/version
curl http://localhost:8080/metrics
```

将 `upstream.address` 指向可用源站后，连续请求同一个可缓存 URL，并检查 `X-Cache`：

```bash
curl -sS -D - -o /dev/null http://localhost:8080/path/to/object
curl -sS -D - -o /dev/null http://localhost:8080/path/to/object
# 正常情况下，第一次请求为 MISS，后续请求转为 HIT。
```

示例配置仅允许本机访问管理路由和执行 `PURGE`。节点对外提供服务前，请检查 `local_api_allow_hosts`、PURGE 的 `allow_hosts`、PProf 凭据和文件系统路径。

## 适用边界

| 适合 Tavern 的场景 | 应由其他层或产品承担 |
|:---|:---|
| 部署在现有网关后的源站屏蔽层或 L2 缓存 | DNS、全局流量调度或托管式 CDN 控制面 |
| Range 请求密集的软件包、安装包、图片和媒体分发 | 身份认证、复杂路由等通用 API 网关策略 |
| 需要主动失效和本地存储控制的私有边缘网络 | 广泛的协议支持或大型运行时插件生态 |
| 需要定制中间件、存储或控制面接入的 Go 基础设施团队 | 必须直接复用 NGINX、Varnish、Squid 或 ATS 配置的替换方案 |

## 功能能力

### 缓存与回源控制

- 对全量 MISS 和并发缺失区间执行合并回源
- 在硬过期前按概率异步刷新
- 基于 `ETag` / `If-None-Match` 和 `Last-Modified` / `If-Modified-Since` 的条件重校验
- 通过请求头触发缓存预取
- 为源站维护独立连接池，支持 TCP 与 Unix Socket

### HTTP 内容分发

- 单区间、后缀区间、开放区间和多区间响应
- 部分命中与可配置的缺失区间填充
- 支持 `Vary` 多版本，可限制版本数量并忽略指定键
- 可选择是否把查询参数纳入缓存键
- 通过声明式规则重写请求头和响应头
- 对象切片存储，支持渐进式读写

### 失效与存储

- 按 URL 或目录前缀执行 `PURGE`
- 软失效后重校验，或硬删除立即移除
- 缓存失效请求来源白名单
- 磁盘与内存 Bucket，一致性哈希或轮询分配
- FIFO、LRU 和 LFU 淘汰策略
- 可选的热、温、冷自动升降级
- 通过 PebbleDB 或 NutsDB 持久化对象元数据

### 扩展点

- 可组合的 `http.RoundTripper` 中间件
- 内置缓存失效、查询统计和完整性校验插件
- 可替换的存储 Bucket、选择器和索引后端
- 通过 Go 接口和 `init()` 在编译期注册；扩展会链接到二进制中，而不是作为运行时插件加载

## 配置

Tavern 使用一个 YAML 配置文件。下面展示主要请求与存储链路；全部配置项请参见 [`config.example.yaml`](config.example.yaml)。

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

### 失效缓存对象

```bash
# 标记单个对象过期，下次请求时进行重校验。
curl -X PURGE http://localhost:8080/assets/app.js

# 立即删除单个对象。
curl -X PURGE -H 'Purge-Type: file,hard' \
  http://localhost:8080/assets/app.js

# 标记路径下的所有对象过期。
curl -X PURGE -H 'Purge-Type: dir' \
  http://localhost:8080/assets/
```

`PURGE` 请求必须来自插件 `allow_hosts` 中配置的地址。

## 运维接口

| 接口 | 用途 |
|:---|:---|
| `/metrics` | Prometheus 缓存、代理、服务端和存储指标 |
| `/healthz` / `/version` | 负载均衡健康检查与构建信息 |
| `/debug/pprof/` | Go 运行时性能分析，支持可选 Basic Auth |
| `ttop` | 通过 SSE 实时查看流量、状态码、资源用量与热点 URL |
| `tq` | 查询访问日志，包括加密访问日志 |
| `SIGUSR2` | 通过 `tableflip` 执行二进制平滑升级与监听器切换 |

访问日志采用结构化格式，并可选择加密落盘。管理 HTTP 端点由 `local_api_allow_hosts` 控制访问。

## 架构

Tavern 使用 `http.RoundTripper` 中间件组合请求链路。上游代理是最内层 Transport，缓存与分发行为可以独立替换。

```text
HTTP Server
├── 本地端点: metrics, health, version, PProf
├── 插件: purge, query stats, integrity verifier
└── 请求管道
    ├── Recovery
    ├── Rewrite
    ├── MultiRange
    ├── Caching
    └── Upstream proxy
        ├── 连接池
        ├── TCP / Unix Socket 传输
        └── 请求合并

Caching
└── Storage selector
    ├── 磁盘 / 内存 Bucket
    ├── 可选的热 / 温 / 冷迁移
    ├── PURGE 与计数器共享状态
    └── 持久化对象元数据
```

## 开发

| 命令 | 用途 |
|:---|:---|
| `make build` | 编译静态的 `bin/tavern` 服务端二进制 |
| `make toolchain` | 编译 `bin/tq` 和 `bin/ttop` |
| `make check` | 运行 `go vet` 与 `staticcheck` |
| `make generate` | 重新生成协议常量 |
| `go test -count=1 -v ./storage/...` | 运行可独立执行的包测试 |
| `go test -count=1 -v ./...` | 运行全部测试；集成测试需要先启动 Tavern |

欢迎提交 Issue 和 Pull Request。请保持改动聚焦，在受影响的包中补充测试，并在提交 PR 前运行相关包测试。

## 文档

| 文档 | 说明 |
|:---|:---|
| [文档索引](docs/README.md) | 项目与生态文档入口 |
| [生态概览](docs/ecosystem/overview.md) | 多层 CDN 拓扑与完整请求生命周期 |
| [功能参考](docs/tavern/02-features.md) | 缓存、存储、PURGE 与运维行为 |
| [架构设计](docs/tavern/03-architecture.md) | 中间件、代理、存储与插件内部实现 |
| [PURGE 设计](docs/purge.md) | 缓存失效协议与实现 |
| [Grafana 仪表板](docs/Grafana-Prometheus-Dashboard.json) | Prometheus 仪表板模板 |

## 生态

- **Tavern Gateway** — 基于 OpenResty 的 L1/L3 网关，与作为 L2 缓存的 Tavern 配合使用
- **[CRC-Center](https://github.com/omalloc/trust-receive)** — `verifier` 插件使用的外部缓存文件完整性校验服务

## 许可证

Tavern 按照 [MIT License](LICENSE) 的条款开源。
