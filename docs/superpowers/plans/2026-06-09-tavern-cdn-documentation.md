# Tavern CDN 生态文档实施计划

> **For agentic workers:** 使用 Workflow (ultracode) 的 4 阶段流水线实施：分析 → 写作 → 交叉审查 → 汇总索引。

**目标：** 为 Tavern CDN 生态系统生成 10 篇完整双语文档 + 1 个导航索引，覆盖项目概况、功能需求和架构设计。

**架构：** 4 阶段 Workflow — Phase 1 并行深度分析源码模块，Phase 2 并行写作各文档，Phase 3 交叉审查准确性和一致性，Phase 4 生成导航索引 README.md。

**技术栈：** Markdown + Mermaid 图表，中英双语。

---

## 文件结构

```
docs/
├── README.md                    # 创建 - 文档导航索引
├── ecosystem/
│   ├── overview.md              # 创建 - CDN 生态全链路概览
│   └── protocol.md              # 创建 - TR-*/X-* 协议规范
├── tavern/
│   ├── 01-project.md            # 创建 - Tavern 项目文档
│   ├── 02-features.md           # 创建 - Tavern 功能需求文档
│   └── 03-architecture.md       # 创建 - Tavern 架构文档
├── gateway/
│   ├── 01-project.md            # 创建 - Gateway 项目文档
│   ├── 02-features.md           # 创建 - Gateway 功能文档
│   └── 03-architecture.md       # 创建 - Gateway 架构文档
└── diagrams/
```

**全部新文件。** 无现有文件修改。

---

## Phase 1: 深度源码分析

### Task 1.1: 分析 Tavern 核心源码

**Agent:** tavern-core-analyst

**分析范围：**
- `main.go` — 启动流程、tableflip 升级、生命周期
- `conf/conf.go` — 完整配置结构
- `server/server.go` + `server/middleware/` — HTTP 服务器和中间件链
- `proxy/` — 上游代理、请求合并 (singleflight)
- `api/defined/v1/` — 所有接口定义
- `contrib/` — kratos/log/config/transport 框架
- `pkg/` — 所有工具包

**输出格式（JSON Schema）：**
```json
{
  "modules": [{
    "name": "string",
    "path": "string",
    "description_cn": "string",
    "description_en": "string",
    "key_types": ["string"],
    "key_functions": ["string"],
    "interfaces": ["string"],
    "dependencies": ["string"]
  }],
  "data_flow": "string (mermaid sequence diagram source)",
  "component_graph": "string (mermaid graph source)"
}
```

### Task 1.2: 分析 Tavern 存储层

**Agent:** tavern-storage-analyst

**分析范围：**
- `storage/storage.go` — Storage 接口实现
- `storage/bucket/disk/` — 磁盘桶实现
- `storage/bucket/memory/` — 内存桶实现
- `storage/indexdb/` — PebbleDB/NutsDB 索引
- `storage/selector/` — hashring/roundrobin 选择器
- `storage/sharedkv/` — 共享 KV 存储
- `storage/diraware/` — 目录感知路由
- `storage/migrator.go` — 冷热迁移

**输出格式：** 同上 schema

### Task 1.3: 分析 Tavern 插件 + 中间件

**Agent:** tavern-plugin-analyst

**分析范围：**
- `plugin/purge/` — PURGE 缓存清除
- `plugin/qs/` — 查询统计 + SSE
- `plugin/verifier/` — CRC 校验
- `server/middleware/caching/` — 缓存核心逻辑
- `server/middleware/rewrite/` — 头部重写
- `server/middleware/multirange/` — 多区间请求
- `server/middleware/recovery/` — Panic 恢复
- `server/mod/wrap.go` — 请求填充/追踪

**输出格式：** 同上 schema

### Task 1.4: 分析 Gateway 源码

**Agent:** gateway-analyst

**分析范围：**
- `unified/dispatch.lua` — TR-LAYER 分发
- `unified/balancer.lua` — 负载均衡
- `lualib/acl/` — ACL 规则引擎
- `lualib/config_loader.lua` — 域名配置加载
- `lualib/cache_rules.lua` — 缓存规则注入
- `lualib/header_sanitize.lua` — Header 清洗
- `lualib/error_annotate.lua` — 错误标注
- `lualib/ssl_provider.lua` — 动态 SSL
- `lualib/phase_dispatcher.lua` — 阶段规则运行器
- `lualib/request_id.lua` — 请求 ID
- `lualib/xcache_parser.lua` — X-Cache 解析
- `lualib/error_page.lua` — 错误页
- `conf/gateway.conf` — Nginx 配置
- `vendor/protocol.lua` — 协议常量

**输出格式：** 同上 schema

---

## Phase 2: 并行文档写作

### Task 2.1: 生态概览文档

**文件：** `docs/ecosystem/overview.md`

**输入：** 所有 Phase 1 分析结果

**内容要点：**
- CDN 三层架构图 (Mermaid graph)
- L1 → L2 → L3 全链路请求流程图 (Mermaid sequence)
- Tavern 与 Gateway 职责分工
- Docker Compose 部署拓扑
- 对比经典 CDN 方案 (Squid/ATS)

### Task 2.2: 协议规范文档

**文件：** `docs/ecosystem/protocol.md`

**输入：** Tavern Agent 1.1 + Gateway Agent 1.4

**内容要点：**
- TR-* 内部协议 Header 完整规范表
- X-* 缓存控制 Header 规范
- Header 安全边界（TR-* 清洗规则）
- 协议常量生成流程 (protocol.conf → Go/Lua)
- 请求中各 Header 的流转示意图 (Mermaid)

### Task 2.3: Tavern 项目文档

**文件：** `docs/tavern/01-project.md`

**输入：** Tavern Agent 1.1

**内容要点：**
- 项目概述、定位
- 技术栈 (Go 1.25+, PebbleDB, Kratos, Tableflip)
- 快速开始
- 编译与构建
- 目录结构详解
- 开发指南（扩展点、接口约定、测试策略）
- CI/CD 流程

### Task 2.4: Tavern 功能文档

**文件：** `docs/tavern/02-features.md`

**输入：** Tavern Agents 1.1 + 1.2 + 1.3

**内容要点：**
- 核心缓存能力矩阵（功能状态表）
  - 预取、推送、模糊刷新、自动刷新、Vary 缓存
  - 请求合并、Range 支持、CRC 校验
- 存储特性
  - 多级存储（Disk/Memory/RawDisk）
  - 淘汰策略 (FIFO/LRU/LFU)
  - 冷热迁移 (Promote/Demote)
  - 目录感知 (DirAware)
- 运维特性
  - 平滑升级、故障恢复
  - Prometheus 监控指标
  - PProf 性能分析
  - 访问日志加密

### Task 2.5: Tavern 架构文档

**文件：** `docs/tavern/03-architecture.md`

**输入：** Tavern Agents 1.1 + 1.2 + 1.3

**内容要点：**
- 整体架构图 (Mermaid)
- 请求生命周期（全链路时序图）
- 中间件链详解（洋葱模型 + 每个中间件的时序图）
- 存储层架构
  - IndexDB 接口与实现
  - Bucket 抽象与选择器
  - SharedKV 设计
  - 迁移器设计
- 代理层（请求合并、回源流程）
- 插件系统（接口、注册、生命周期）
- 内部框架（Kratos App 生命周期）
- 配置系统（YAML 加载、热更新）

### Task 2.6: Gateway 项目文档

**文件：** `docs/gateway/01-project.md`

**输入：** Gateway Agent 1.4

**内容要点：**
- 项目概述、在 CDN 中的定位
- 技术栈 (OpenResty, Lua, Nginx)
- 快速开始（Docker + 裸机）
- 目录结构详解
- 开发指南
- 测试策略 (Test::Nginx)

### Task 2.7: Gateway 功能文档

**文件：** `docs/gateway/02-features.md`

**输入：** Gateway Agent 1.4

**内容要点：**
- 三层 Server Block 设计
- TR-LAYER 逻辑分层
- 域名配置管理（JSON 格式、Hash 校验、远程加载）
- ACL 规则引擎能力矩阵
  - 条件变量 × 运算符
  - 动作类型
- 源站负载均衡
  - WRR + 故障转移
  - Main/Slave 分组
- SSL 动态证书 (SNI)
- Header 安全清洗
- 错误标注与缓存
- 访问日志

### Task 2.8: Gateway 架构文档

**文件：** `docs/gateway/03-architecture.md`

**输入：** Gateway Agent 1.4

**内容要点：**
- TR-LAYER 分发机制（全套时序图）
  - L1 请求处理流程
  - L3 回源处理流程
- ACL 规则引擎架构
  - 条件评估 → 运算符组合 → 动作执行
  - 代码路径图
- 源站负载均衡器
  - balancer_by_lua 流程
  - 故障追踪状态机
- 配置加载器
  - 目录扫描/远程拉取/定时刷新
- Header 清洗安全边界
- 请求上下文 (ngx.ctx) 数据流
- 共享内存 (lua_shared_dict) 使用

---

## Phase 3: 交叉审查

### Task 3.1: 生态文档审查

**Agent:** ecosystem-reviewer

**任务：** 审查 `ecosystem/overview.md` 和 `ecosystem/protocol.md`

**检查项：**
- 架构描述与源码一致
- 协议 Header 名称、方向、用途准确
- Mermaid 图正确渲染
- 中英文内容一致

### Task 3.2: Tavern 文档审查

**Agent:** tavern-reviewer

**任务：** 审查 `tavern/01-project.md`, `tavern/02-features.md`, `tavern/03-architecture.md`

**检查项：**
- 接口名称、文件路径、行号准确
- 功能列表与 CHANGELOG 和 README 一致
- Mermaid 图正确
- 配置示例可运行
- 中英文一致

### Task 3.3: Gateway 文档审查

**Agent:** gateway-reviewer

**任务：** 审查 `gateway/01-project.md`, `gateway/02-features.md`, `gateway/03-architecture.md`

**检查项：**
- Lua 模块名、函数名、文件路径准确
- 功能列表与 README/DESIGN 一致
- Mermaid 图正确
- 配置示例可运行
- 中英文一致

---

## Phase 4: 汇总索引

### Task 4.1: 生成导航索引

**文件：** `docs/README.md`

**内容：**
- 文档目录树
- 各文档简介（中英双语，1-2 句）
- 推荐阅读顺序
- 相关链接（GitHub、CRC-Center）

---

## 实施说明

- 所有 Task 由 Workflow 并行调度
- Phase 1 完成后进入 Phase 2（分析结果作为写作输入）
- Phase 2 完成后进入 Phase 3（文档完成后再审查）
- Phase 3 完成后进入 Phase 4（生成索引）
- 每个文档生成后直接保存到对应路径
- 最终 git commit 提交所有文档
