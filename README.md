# Tavern Caching

Tavern 是一个高性能的 HTTP 缓存代理服务器，旨在利用现代化的微服务框架提供更灵活的架构、更强的扩展性以及更优秀的性能。

## ✨ 特性 (Features)

- **核心缓存能力**:
  - 支持缓存预取 (Prefetch) 与自动刷新 (Auto Refresh)
  - 支持热点迁移 (Hot Migration) 与模糊获取 (Fuzzying fetch)
  - 支持 Vary 响应头与 Range 请求 (MultiRange)
- **现代化架构**:
  - 基于 **Kratos** 框架，提供高扩展、模块复用能力
  - **插件系统 (Plugin System)**: 支持通过插件扩展核心业务逻辑
  - **存储抽象 (Storage Layer)**: 解耦存储后端，支持内存、磁盘及自定义存储实现
- **高可用与运维**:
  - **平滑升级 (Graceful Upgrade)**: 支持零停机配置重载与二进制升级
  - **故障恢复**: 内置 Panic Recovery 与错误处理机制
  - **可观测性**: 原生支持 Prometheus Metrics 监控与 PProf 性能分析
- **流量控制**:
  - 支持 Header 重写 (Rewrite)
  - 支持上游负载均衡 (基于自定义的 Selector)

## 🚀 快速开始 (Quick Start)

### 环境要求

- Go 1.24+
- Linux/macOS (Windows 下平滑重启功能可能受限)

### 1. 获取与配置

克隆仓库并准备配置文件：

```bash
git clone https://github.com/omalloc/tavern.git
cd tavern

# 使用示例配置初始化
cp config.example.yaml config.yaml
```

### 2. 运行服务

**开发模式运行:**

```bash
# 默认加载当前目录下的 config.yaml
go run main.go
```

**编译运行:**

```bash
make build
./bin/tavern -c config.yaml
```

### 3. 调试与监控

启动后，你可以通过以下方式进行监控与调试（具体端口取决于 `config.yaml` 配置）：

- **Metrics**: 访问 `/metrics` 获取 Prometheus 监控指标 (默认前缀 `tr_tavern_`)
- **PProf**: 开启调试模式后，可访问 `/debug/pprof/` 进行性能分析

## 🧩 目录结构

- `api/`: 定义协议与接口
- `conf/`: 配置定义与解析
- `plugin/`: 插件接口与实现
- `proxy/`: 核心代理转发逻辑
- `server/`: HTTP服务端实现及中间件 (Middleware)
- `storage/`: 存储引擎抽象与实现

## 📝 License

[MIT License](LICENSE)
