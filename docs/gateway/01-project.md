# Gateway 项目文档 / Gateway Project Documentation

> **Gateway** 是 Tavern CDN 生态中的 OpenResty 网关组件。单个 Nginx 实例同时承担 **L1 (前端流量入口)** 和 **L3 (回源上游)** 两个角色，
> 通过 `TR-LAYER` 请求头进行逻辑分离。

---

## 1. 项目概要 / Project Summary

| 属性 / Property | 值 / Value |
|:---|:---|
| **项目名称 / Name** | tavern-gateway |
| **语言 / Language** | Lua (OpenResty) |
| **运行时 / Runtime** | OpenResty ≥ 1.21.4 |
| **构建 / Build** | N/A (解释执行) |
| **测试框架 / Test Framework** | Test::Nginx::Socket |
| **定位 / Role** | CDN L1 (前端) + L3 (回源) 网关 |
| **生态 / Ecosystem** | [Tavern CDN](../ecosystem/overview.md) |

### 架构定位 / Architecture Positioning

```
Client → Gateway :80/:443 (L1) → Tavern :8080 (L2) → Gateway :8000 (L3) → Origin
         └── Rewrite, Auth,        └── HIT: 响应       └── 剥离 TR-*,
             Cache Rules               MISS: 回源          负载均衡,
                                                           错误标注
```

---

## 2. 技术栈 / Tech Stack

| 组件 / Component | 技术 / Technology | 用途 / Purpose |
|:---|:---|:---|
| **Web 服务器** | OpenResty (Nginx + LuaJIT) | 主运行时 |
| **脚本语言** | Lua / LuaJIT | 业务逻辑 |
| **测试** | Test::Nginx::Socket | 集成测试 |
| **Lint** | luacheck | 代码静态检查 |
| **配置** | JSON (每域名一文件) + nginx.conf | 域名规则配置 |
| **容器化** | Docker (docker-compose.yaml) | 开发环境 |

---

## 3. 快速开始 / Quick Start

### 环境要求 / Prerequisites

- **OpenResty** ≥ 1.21.4
- Tavern (L2) 运行在 `:8080`
- 源站服务（或使用 Docker Compose 启动 Mock）

### Docker 方式 (推荐) / Docker (Recommended)

```bash
# 启动 gateway + tavern + mock-origin
docker compose up -d

# 测试
curl -H "Host: cdn.example.com" http://localhost:20080/index.html
```

### 裸机方式 / Bare Metal

```bash
# 启动统一网关 (L1 :80/:20080 + :8000 回源)
openresty -p $PWD -c conf/gateway.conf

# 使用自定义规则目录
GATEWAY_CONFIG_URL=file:///path/to/rules/ openresty -p $PWD -c conf/gateway.conf

# 远程拉取配置 (每 30s 刷新)
GATEWAY_CONFIG_URL=https://api.example.com/config openresty -p $PWD -c conf/gateway.conf
```

---

## 4. 目录结构 / Directory Structure

```
gateway/
├── README.md                        # 快速开始与用户指南
├── DESIGN.md                        # 完整架构设计与请求生命周期
├── CLAUDE.md                        # AI 助手指引
├── Makefile                         # test, lint, vendor, reload, clean
├── docker-compose.yaml              # 全栈: gateway + tavern + mock-origin
│
├── vendor/
│   └── protocol.lua                 # 从 Tavern 自动生成 (TR-* 常量)
│
├── unified/
│   ├── dispatch.lua                 # TR-LAYER 路由器 → 所有 nginx phases
│   └── balancer.lua                 # L3 源站 WRR + 故障转移 (balancer_by_lua)
│
├── lualib/
│   ├── acl/                         # ACL 规则引擎
│   │   ├── init.lua                 #   入口: evaluate + execute
│   │   ├── condition.lua            #   条件评估器 (11 变量 × 13 运算符)
│   │   ├── operator.lua             #   AND/OR 组合器 (支持嵌套组)
│   │   └── action.lua              #   动作执行器 (11 种动作)
│   ├── cache_rules.lua              # X-Prefetch / X-CacheTime 注入
│   ├── config_loader.lua            # 域名 JSON 配置加载器 (目录扫描 + Hash 校验)
│   ├── error_annotate.lua           # TR-ERRCODE 错误标注 (允许缓存错误)
│   ├── error_page.lua               # 域名自定义错误页
│   ├── header_sanitize.lua          # TR-* 头安全清洗 (入口 + 出口)
│   ├── phase_dispatcher.lua         # 按阶段运行 JSON 规则
│   ├── request_id.lua               # X-Request-ID 注入与转发
│   ├── ssl_provider.lua             # SNI 动态 SSL 证书 (ssl_certificate_by_lua)
│   └── xcache_parser.lua            # X-Cache → $tr_cache_status (访问日志)
│
├── conf/
│   ├── gateway.conf                 # 统一 nginx 配置 (3 个 server 块)
│   ├── tavern.docker.yaml           # Docker 环境 Tavern L2 配置
│   ├── ssl/
│   │   ├── fallback.crt             # 自签名占位证书
│   │   └── fallback.key             # 占位私钥
│   └── rules/
│       ├── cdn.example.com.json     # 完整域名配置示例
│       └── static.example.com.json  # 最小域名配置示例
│
└── t/                                # Test::Nginx 集成测试
    ├── test_unified.t               # TR-LAYER 分发 + 配置合并 + Hash API
    ├── test_layer3.t                # 错误标注 + 负载均衡
    └── util/
        └── mock_origin.conf         # Mock 源站配置
```

---

## 5. 开发指南 / Development Guide

### 构建与测试 / Build & Test

```bash
# 运行所有集成测试
make test
# 等价于: TEST_NGINX_BINARY=/usr/local/openresty/nginx/sbin/nginx prove -r t/

# Lint Lua 代码
make lint

# 同步协议常量 (从 Tavern)
make vendor-protocol

# 优雅重载
make reload

# 清理 (PID, 日志, server root)
make clean
```

### 协议同步 / Protocol Sync

```bash
# 当 Tavern 的 internal/protocol/protocol.conf 更新后:
make vendor-protocol

# 这会重新生成 vendor/protocol.lua
```

### 测试策略 / Test Strategy

- **集成测试**: `t/test_unified.t` (分发、配置、Header 清洗)
- **L3 测试**: `t/test_layer3.t` (错误标注、负载均衡、故障追踪)
- **Mock Origin**: `t/util/mock_origin.conf` (模拟源站)

### 代码规范 / Code Conventions

- **模块模式**: 每个 Lua 文件返回一个 module table `_M`
- **require 加载**: 使用 `require("lualib.xxx")` 路径
- **错误处理**: 使用 `pcall` 包装配置规则执行，错误只记录不影响请求
- **共享状态**: 通过 `ngx.ctx` 在阶段间传递，`ngx.shared.DICT` 跨请求共享

---

## 6. 相关文档 / Related Documents

- [Gateway 功能文档 / Gateway Features](./02-features.md)
- [Gateway 架构文档 / Gateway Architecture](./03-architecture.md)
- [生态概览 / Ecosystem Overview](../ecosystem/overview.md)
- [协议规范 / Protocol Specification](../ecosystem/protocol.md)

---

*Document generated: 2026-06-09 | Source: gateway README, DESIGN, CLAUDE.md, source code*
