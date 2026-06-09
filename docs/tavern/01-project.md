# Tavern 项目文档 / Tavern Project Documentation

> **Tavern** 是一个基于 Go 语言实现的高性能 HTTP 缓存代理服务器 (CDN L2 Cache)。
> 它采用 LSM-Tree (PebbleDB) 作为缓存元数据索引，突破传统 CDN 软件中海量小文件场景下内存瓶颈的限制。

---

## 1. 项目概要 / Project Summary

| 属性 / Property | 值 / Value |
|:---|:---|
| **项目名称 / Name** | Tavern |
| **Go Module** | `github.com/omalloc/tavern` |
| **语言 / Language** | Go 1.25+ |
| **构建模式 / Build Mode** | CGO_ENABLED=0 (静态链接) |
| **许可证 / License** | MIT |
| **CI/CD** | GitHub Actions (`.github/workflows/go.yml`) |
| **Docker** | `Dockerfile` (多阶段构建) |
| **定位 / Role** | CDN L2 缓存代理 (HTTP Cache Proxy) |
| **生态 / Ecosystem** | [Tavern CDN](../ecosystem/overview.md) |

### 核心设计理念 / Core Design Principles

1. **LSM-Tree 索引 / LSM-Tree Index**: 使用 PebbleDB (CockroachDB 的 LSM-Tree 引擎) 作为缓存元数据索引 (`IndexDB`)，使得缓存能支撑远超物理内存的海量对象存储。

2. **洋葱模型中间件 / Onion Model Middleware**: HTTP 处理器通过 `func(http.RoundTripper) http.RoundTripper` 函数式包装链实现可组合的中间件管道。

3. **接口驱动 / Interface-Driven**: 所有关键组件（存储、插件、中间件）通过接口定义在 `api/defined/v1/` 中，实现与接口分离。

4. **Kratos 风格生命周期 / Kratos-Style Lifecycle**: 借鉴 [Kratos](https://github.com/go-kratos/kratos) 的 `App` 生命周期管理（Start/Stop hooks + 信号处理）。

5. **平滑升级 / Graceful Upgrade**: 通过 [Cloudflare tableflip](https://github.com/cloudflare/tableflip) 实现零停机二进制热升级。

---

## 2. 技术栈 / Tech Stack

| 组件 / Component | 技术 / Technology | 版本 / Version | 用途 / Purpose |
|:---|:---|:---|:---|
| **语言 / Language** | Go | ≥ 1.25 | 主语言 |
| **HTTP 框架 / HTTP Framework** | Kratos (inspired) | — | `contrib/` 内部实现 |
| **LSM-Tree 引擎 / LSM Engine** | PebbleDB | — | 缓存元数据索引 |
| **LSM-Tree 引擎 / LSM Engine** | NutsDB | — | 备选缓存元数据索引 |
| **配置 / Config** | YAML (gopkg.in/yaml.v3) | — | 单文件配置 |
| **日志 / Logging** | lumberjack | v2.0+ | 日志轮转 |
| **平滑升级 / Graceful Upgr.** | tableflip | v1.0+ | 零停机升级 |
| **负载均衡 / Load Balancing** | omalloc/proxy | — | 上游节点选择 |
| **编码 / Encoding** | JSON, Brotli, Gzip | — | 内容编码支持 |
| **指标 / Metrics** | Prometheus client_golang | — | 可观测性 |
| **容器化 / Containerization** | Docker (多阶段) | — | 部署 |

### 关键依赖 / Key Dependencies (from go.mod)

```
github.com/cloudflare/tableflip          # 平滑升级
github.com/cockroachdb/pebble            # LSM-Tree 索引引擎
github.com/nutsdb/nutsdb                 # 备选 LSM-Tree 引擎
github.com/omalloc/proxy                 # 上游代理选择器
github.com/go-kratos/kratos/v2           # 架构灵感来源
github.com/prometheus/client_golang      # 监控指标
github.com/valyala/fasthttp              # 高性能 HTTP (部分使用)
gopkg.in/natefinch/lumberjack.v2         # 日志轮转
gopkg.in/yaml.v3                         # YAML 解析
```

---

## 3. 快速开始 / Quick Start

### 环境要求 / Prerequisites

- **Go** ≥ 1.24
- **Linux** / **macOS** (Windows 下 tableflip 功能受限)
- **CGO_ENABLED=0** (不需要 CGO)

### 获取与构建 / Clone & Build

```bash
git clone https://github.com/omalloc/tavern.git
cd tavern

# 安装依赖 + 工具
make init

# 编译
make build
# → bin/tavern (静态二进制)

# 编译 CLI 工具
make toolchain
# → bin/tq, bin/ttop
```

### 配置与运行 / Configure & Run

```bash
# 使用示例配置初始化
cp config.example.yaml config.yaml

# 开发模式
go run main.go -c config.yaml

# 生产模式
./bin/tavern -c /etc/tavern/config.yaml
```

### 调试与监控 / Debug & Monitor

| 端点 / Endpoint | 用途 / Purpose | 访问条件 / Access |
|:---|:---|:---|
| `/metrics` | Prometheus 指标 (前缀 `tr_tavern_`) | local IP |
| `/healthz` | 健康检查 | local IP |
| `/version` | 版本信息 | local IP |
| `/debug/pprof/` | Go PProf 性能分析 | Basic Auth |

---

## 4. 目录结构 / Directory Structure

```
tavern/
├── main.go                        # 入口: 配置加载、App 创建、tableflip 升级
├── Makefile                       # 构建、测试、工具链
├── Dockerfile                     # 多阶段 Docker 构建
├── config.example.yaml            # 完整配置示例
├── go.mod / go.sum               # Go 模块依赖
├── CLAUDE.md                      # AI 助手指引
│
├── conf/                          # 配置定义 (Bootstrap struct)
│   └── conf.go                    # Logger, Server, Upstream, Storage, Plugin 配置结构体
│
├── server/                        # HTTP 服务器
│   ├── server.go                  # HTTPServer 实现
│   ├── mod/wrap.go                # 请求填充、追踪注入、响应记录
│   └── middleware/                 # 中间件系统
│       ├── middleware.go           # Middleware 类型 & Chain 函数
│       ├── registry.go            # 中间件注册表 (name → Factory)
│       ├── caching/               # 核心缓存中间件 (15+ 文件)
│       ├── recovery/              # Panic 恢复中间件
│       ├── rewrite/               # Header 重写中间件
│       └── multirange/            # Multi-Range 支持中间件
│
├── proxy/                         # 上游代理
│   ├── proxy.go                   # 反向代理、客户端池、节点选择
│   ├── global.go                  # 全局代理状态
│   ├── metrics.go                 # 代理层 Prometheus 指标
│   └── singleflight/              # 请求合并 (Request Coalescing)
│
├── storage/                       # 存储引擎
│   ├── storage.go                 # nativeStorage: PURGE、Select、Close
│   ├── registry.go                # 存储后端注册
│   ├── builder.go                 # Bucket 构造器
│   ├── global.go                  # 全局配置
│   ├── migrator.go               # 冷热迁移 (Promote/Demote)
│   ├── bucket/                    # 存储桶实现
│   │   ├── disk/                  # 磁盘桶 (分块文件存储)
│   │   ├── memory/                # 内存桶
│   │   ├── rawdisk/               # 裸盘桶
│   │   └── empty/                 # 空桶 (NOP)
│   ├── indexdb/                   # LSM-Tree 索引后端
│   ├── selector/                  # Bucket 选择器 (hashring/roundrobin)
│   ├── sharedkv/                  # 跨桶共享 KV 存储
│   └── diraware/                  # 目录感知缓存路由
│
├── plugin/                        # 插件系统
│   ├── purge/                     # PURGE 缓存清除插件
│   ├── qs/                        # Query Stats / SSE 实时监控插件
│   └── verifier/                  # CRC 文件完整性校验插件
│
├── api/defined/v1/                # 接口定义 (Contracts)
│   ├── storage/                   # Storage, Bucket, IndexDB, SharedKV 接口
│   │   └── object/                # ID, Metadata, IDHash 类型
│   ├── middleware/                 # 中间件配置 & 接口
│   └── plugin/                    # Plugin 接口
│
├── contrib/                       # 内部框架 (Kratos-inspired)
│   ├── kratos/app.go              # App 生命周期 (Start/Stop hooks)
│   ├── log/                       # 结构化日志 (lumberjack 集成)
│   ├── config/                    # YAML 配置加载 (file/remote providers)
│   ├── transport/                 # HTTP 传输层接口
│   └── container/list/            # 泛型双向链表
│
├── internal/                      # 内部实现
│   └── protocol/                  # 协议常量 (go generate)
│
├── pkg/                           # 工具包
│   ├── encoding/                  # 内容编码 (brotli, gzip, json)
│   ├── errors/                    # HTTP-visible 错误处理
│   ├── pathtrie/                  # 路径 Trie 匹配
│   ├── algorithm/                 # 通用算法
│   ├── metrics/                   # Prometheus 工具
│   ├── traces/                    # 请求追踪
│   ├── e2e/                       # E2E 测试帮助
│   ├── iobuf/                     # I/O 缓冲
│   ├── mapstruct/                 # Map→Struct 解码
│   └── x/                         # 扩展 stdlib
│       └── runtime/               # 构建信息
│
├── cmd/                           # CLI 工具
│   ├── top/                       # ttop: 实时缓存状态监控 (TUI)
│   └── tq/                        # tq: 命令行查询工具
│
├── tests/                         # 集成测试 (需要运行中的 Tavern)
│   ├── config.test.yaml           # 测试配置
│   ├── all-features/              # 全特性测试
│   └── mockserver/                # Mock 源站
│
├── docs/                          # 文档
│   ├── ecosystem/                 # CDN 生态文档
│   ├── tavern/                    # Tavern 文档 (本文档所在目录)
│   ├── gateway/                   # Gateway 文档
│   ├── purge.md                   # PURGE 设计文档
│   ├── cdn-cache-analysis.md      # CDN 缓存对比分析
│   └── Grafana-Prometheus-Dashboard.json  # Grafana 仪表盘
│
└── .build/systemd/                # systemd 服务文件
```

---

## 5. 开发指南 / Development Guide

### 构建与测试 / Build & Test

```bash
# 所有检查
make check           # go vet + staticcheck

# 编译
make build           # → bin/tavern
make toolchain       # → bin/tq, bin/ttop

# 单元测试 (不需要运行中的服务)
go test -count=1 -v ./storage/...
go test -count=1 -v ./server/...

# 全部测试 (需要先启动 tavern)
go test -count=1 -v ./...

# 生成协议常量
make generate
```

### CI/CD 流程 / CI Pipeline

```
make build → start ./bin/tavern -c ./tests/config.test.yaml → go test ./...
```

1. 编译静态二进制
2. 使用测试配置启动 Tavern
3. 运行全部单元测试 + 集成测试

### 代码规范 / Code Conventions

- **接口合规断言 / Interface Compliance**:
  ```go
  var _ storage.Storage = (*nativeStorage)(nil)
  ```
  每个具体类型在包级别断言其实现了接口。

- **自注册模式 / Self-Registration**:
  ```go
  func init() {
      middleware.RegisterFactory("caching", factory)
  }
  ```
  通过 `init()` 函数在包被导入时自动注册。

- **构造函数模式 / Constructor Pattern**:
  ```go
  func New(config *conf.X, logger log.Logger) (Type, error)
  ```
  配置指针 + Logger 注入，返回实例和错误。

- **日志 / Logging**:
  ```go
  log.NewHelper(logger).Infof("key: %s", value)
  ```
  使用 `log.Helper` 进行结构化 KV 日志。不使用全局日志。

- **测试 / Testing**:
  - 白盒测试: `package foo` (与源码同包)
  - 黑盒测试: `package foo_test` (与源码异包)
  - 集成测试: `tests/` (需要运行中的 Tavern)

### 扩展点 / Extension Points

| 扩展类型 / Extension | 创建路径 / Path | 注册方式 / Registration |
|:---|:---|:---|
| **中间件 / Middleware** | `server/middleware/<name>/` | `middleware.RegisterFactory("name", factory)` in `init()` |
| **插件 / Plugin** | `plugin/<name>/` | `plugin.RegisterFactory("name", factory)` in `init()` + blank import in `main.go` |
| **存储桶 / Bucket** | `storage/bucket/<type>/` | 实现 `storage.Bucket` 接口 |
| **索引数据库 / IndexDB** | `storage/indexdb/<type>/` | `indexdb.RegisterFactory("type", factory)` in `init()` |

---

## 6. 相关文档 / Related Documents

- [Tavern 功能文档 / Tavern Features](./02-features.md)
- [Tavern 架构文档 / Tavern Architecture](./03-architecture.md)
- [生态概览 / Ecosystem Overview](../ecosystem/overview.md)
- [协议规范 / Protocol Specification](../ecosystem/protocol.md)

---

*Document generated: 2026-06-09 | Source: tavern README, CLAUDE.md, go.mod, main.go, source code*
