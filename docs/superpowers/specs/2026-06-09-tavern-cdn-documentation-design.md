# Tavern CDN 生态文档设计规格

## 概述

为 Tavern CDN 生态系统（Tavern + Gateway）生成完整的双语文档集，覆盖项目概况、功能需求、架构设计，并以 Mermaid 图表辅助说明。

## 输出位置

`tavern/docs/` 目录下。

## 文档结构

```
docs/
├── README.md                              # 文档导航索引（中文）
│
├── ecosystem/                             # 生态层（顶层视角）
│   ├── overview.md                        # Tavern CDN 生态概览 + L1/L2/L3 全链路架构
│   └── protocol.md                        # TR-* / X-* 协议规范与 Header 流转
│
├── tavern/                                # Tavern 核心
│   ├── 01-project.md                      # 项目文档：概述、技术栈、快速开始、开发指南
│   ├── 02-features.md                     # 需求/功能文档：核心缓存能力、存储特性、运维特性
│   └── 03-architecture.md                 # 架构文档：请求生命周期、中间件、存储、代理、插件、框架
│
├── gateway/                               # Gateway 配套
│   ├── 01-project.md                      # 项目文档
│   ├── 02-features.md                     # 功能文档
│   └── 03-architecture.md                 # 架构文档：TR-LAYER 分发、ACL 引擎、负载均衡、Server Block
│
└── diagrams/                              # 共享 Mermaid 图表源文件
```

## 内容要求

- 双语文档（中文为主，英文为辅）
- Mermaid 时序图、流程图、类图
- 代码路径引用格式：`file.go:line`
- 配置示例
- 接口定义

## 信息来源

- tavern 项目源代码
- tavern CLAUDE.md
- tavern README.md / README.zh-CN.md
- tavern config.example.yaml
- tavern CHANGELOG.md
- tavern docs/purge.md
- tavern docs/cdn-cache-analysis.md
- gateway README.md
- gateway DESIGN.md
- gateway CLAUDE.md
- gateway 源代码（Lua 模块）

## 实施策略

使用多 Agent 并行工作流，按文档文件分工：
- Phase 1: 平行分析（各 Agent 深度阅读对应源码模块）
- Phase 2: 平行写作（各 Agent 负责独立的文档文件）
- Phase 3: 交叉审查（Agent 之间交叉检查准确性和一致性）
- Phase 4: 汇总索引（生成 README.md 导航）
