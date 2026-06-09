# Tavern CDN 文档 / Tavern CDN Documentation

欢迎来到 **Tavern CDN** 项目文档！这里提供了完整的生态系统文档，涵盖 Tavern (Go 缓存代理) 和 Gateway (OpenResty 网关) 两个核心组件。

Welcome to the **Tavern CDN** documentation! This directory contains the complete ecosystem documentation covering both the Tavern (Go cache proxy) and Gateway (OpenResty gateway) components.

---

## 📖 推荐阅读顺序 / Recommended Reading Order

### 新手入门 / Getting Started

1. **[生态概览 / Ecosystem Overview](./ecosystem/overview.md)** — 了解三层 CDN 架构和全链路请求流程 (15 min)
2. **[协议规范 / Protocol Specification](./ecosystem/protocol.md)** — 理解内部 TR-*/X-* 协议头 (10 min)
3. **[Tavern 项目文档 / Tavern Project](./tavern/01-project.md)** — Tavern 是什么、怎么运行 (15 min)
4. **[Gateway 项目文档 / Gateway Project](./gateway/01-project.md)** — Gateway 是什么、怎么运行 (15 min)

### 深入理解 / Deep Dive

5. **[Tavern 功能文档 / Tavern Features](./tavern/02-features.md)** — 全部缓存和存储能力 (20 min)
6. **[Tavern 架构文档 / Tavern Architecture](./tavern/03-architecture.md)** — 中间件链、存储层、代理层、插件系统 (30 min)
7. **[Gateway 功能文档 / Gateway Features](./gateway/02-features.md)** — L1/L3 全部功能能力 (15 min)
8. **[Gateway 架构文档 / Gateway Architecture](./gateway/03-architecture.md)** — TR-LAYER 分发、ACL 引擎、负载均衡 (25 min)

---

## 📂 文档结构 / Document Structure

```
docs/
├── README.md                           ← 你在这里 / You are here
│
├── ecosystem/                           # 🌐 生态层 — 全局视角
│   ├── overview.md                      #    生态概览: L1/L2/L3 全链路架构、部署拓扑
│   └── protocol.md                      #    协议规范: TR-*/X-* 头定义、安全清洗、同步机制
│
├── tavern/                              # 🗄️ Tavern 核心 (Go)
│   ├── 01-project.md                    #    项目文档: 概述、技术栈、快速开始、目录结构
│   ├── 02-features.md                   #    功能文档: 缓存能力、存储特性、运维治理
│   └── 03-architecture.md               #    架构文档: 请求生命周期、中间件、代理层、插件
│
└── gateway/                             # 🚪 Gateway 配套 (OpenResty)
    ├── 01-project.md                    #    项目文档: 概述、技术栈、快速开始、目录结构
    ├── 02-features.md                   #    功能文档: L1/L3 功能、ACL、负载均衡
    └── 03-architecture.md               #    架构文档: TR-LAYER 分发、规则引擎、配置加载
```

---

## 🎯 文档地图 / Document Map

### 我想了解... / I want to understand...

| 问题 / Question | 文档 / Document |
|:---|:---|
| Tavern CDN 整体是什么？ | [生态概览](./ecosystem/overview.md) |
| 请求从客户端到源站经历了什么？ | [生态概览 - 全链路时序图](./ecosystem/overview.md#2-全链路请求流程--full-request-lifecycle) |
| TR-* 和 X-* 头都是什么？ | [协议规范](./ecosystem/protocol.md) |
| Tavern 怎么编译和运行？ | [Tavern 项目文档 - 快速开始](./tavern/01-project.md#3-快速开始--quick-start) |
| Tavern 有哪些缓存能力？ | [Tavern 功能文档](./tavern/02-features.md) |
| 中间件链是怎么工作的？ | [Tavern 架构文档 - 中间件链](./tavern/03-architecture.md#3-中间件链--middleware-chain) |
| 缓存怎么存取？ | [Tavern 架构文档 - 缓存中间件](./tavern/03-architecture.md#4-缓存中间件深度分析--caching-middleware-deep-dive) |
| 存储层架构是什么？ | [Tavern 架构文档 - 存储层](./tavern/03-architecture.md#5-存储层架构--storage-layer-architecture) |
| PURGE 怎么实现？ | [PURGE 设计文档](./purge.md) + [Tavern 架构 - PURGE](./tavern/03-architecture.md#55-purge-流程详解--purge-flow-detail) |
| Gateway 怎么配置？ | [Gateway 项目文档 - 快速开始](./gateway/01-project.md#3-快速开始--quick-start) |
| L1 和 L3 怎么在一个 Nginx 里工作？ | [Gateway 架构文档 - TR-LAYER 分发](./gateway/03-architecture.md#2-tr-layer-分发机制--tr-layer-dispatch-mechanism) |
| ACL 规则怎么写？ | [Gateway 功能文档 - ACL](./gateway/02-features.md#4-acl-规则引擎--acl-rule-engine) |
| 源站负载均衡怎么配置？ | [Gateway 功能文档 - 负载均衡](./gateway/02-features.md#31-源站负载均衡--origin-load-balancing) |
| Tavern 和 Squid/ATS 有什么区别？ | [CDN 缓存分析](./cdn-cache-analysis.md) |

---

## 🔗 外部资源 / External Resources

| 资源 / Resource | 链接 / Link |
|:---|:---|
| **Tavern GitHub** | [github.com/omalloc/tavern](https://github.com/omalloc/tavern) |
| **Gateway GitHub** | 内部仓库 (tavern-gateway) |
| **CRC 校验服务** | [github.com/omalloc/trust-receive](https://github.com/omalloc/trust-receive) |
| **Grafana Dashboard** | [Grafana-Prometheus-Dashboard.json](./Grafana-Prometheus-Dashboard.json) |
| **Tavern 官网** | [tavern.omalloc.com](https://tavern.omalloc.com/) |

---

## 📋 其他文档 / Other Documents

| 文档 / Document | 说明 / Description |
|:---|:---|
| [PURGE 设计 / PURGE Design](./purge.md) | PURGE 缓存清除的完整设计、配置和流程 |
| [CDN 缓存分析 / CDN Cache Analysis](./cdn-cache-analysis.md) | Tavern vs Squid vs ATS 对比分析 |
| [系统设计规格 / System Design Spec](./superpowers/specs/2026-06-09-tavern-cdn-documentation-design.md) | 本文档集的设计规格 |
| [实施计划 / Implementation Plan](./superpowers/plans/2026-06-09-tavern-cdn-documentation.md) | 文档集实施的计划 |

---

*文档生成时间: 2026-06-09 | Documentation generated: 2026-06-09*
