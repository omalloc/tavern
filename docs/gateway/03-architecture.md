# Gateway 架构文档 / Gateway Architecture Documentation

> 本文档深入描述 Gateway (OpenResty) 的内部架构、TR-LAYER 分发机制、ACL 规则引擎、负载均衡器设计和全链路数据流。

---

## 1. 整体架构 / Overall Architecture

```mermaid
graph TB
    subgraph "Nginx Master Process"
        Master[Master Process]
    end

    subgraph "Nginx Worker Process"
        subgraph "3 个 Server Block / 3 Server Blocks"
            L1_HTTP["L1 HTTP<br/>:80, :20080"]
            L1_HTTPS["L1 HTTPS<br/>:443, :20443"]
            L3_INT["L3 Internal<br/>:8000"]
        end

        subgraph "unified/ 核心分发 / Core Dispatch"
            Dispatch["dispatch.lua<br/>TR-LAYER Router"]
        end

        subgraph "lualib/ 功能模块 / Feature Modules"
            ACL["acl/<br/>Access Control"]
            Balancer["balancer.lua<br/>Load Balancer"]
            Config["config_loader.lua<br/>Config Loader"]
            Sanitize["header_sanitize.lua<br/>Security"]
            SSL["ssl_provider.lua<br/>Dynamic SSL"]
            Cache["cache_rules.lua<br/>Cache Rules"]
            Annotate["error_annotate.lua<br/>Error Annotation"]
            ErrorPage["error_page.lua<br/>Error Pages"]
            ReqID["request_id.lua<br/>Request ID"]
            XCache["xcache_parser.lua<br/>Cache Status"]
            PhaseDispatch["phase_dispatcher.lua<br/>Phase Rules"]
        end
    end

    subgraph "Shared Memory / 共享内存"
        AddrCache["addr_cache<br/>1MB"]
        ConfigStore["config_store<br/>16MB"]
        CacheRules["cache_rules<br/>1MB"]
    end

    subgraph "Per-Request Context / 请求上下文"
        Ctx["ngx.ctx<br/>layer, origin_peers,<br/>origin_servers,<br/>backend_ip, backend_port,<br/>balancer_last_key, scheme"]
    end

    Master --> L1_HTTP
    Master --> L1_HTTPS
    Master --> L3_INT

    L1_HTTP --> Dispatch
    L1_HTTPS --> Dispatch
    L3_INT --> Dispatch

    Dispatch --> ACL
    Dispatch --> Balancer
    Dispatch --> Config
    Dispatch --> Sanitize
    Dispatch --> Cache
    Dispatch --> Annotate
    Dispatch --> ErrorPage
    Dispatch --> ReqID
    Dispatch --> XCache
    Dispatch --> PhaseDispatch
    Dispatch --> SSL

    Config --> ConfigStore
    Balancer --> AddrCache
    Cache --> CacheRules

    Dispatch --> Ctx
```

---

## 2. TR-LAYER 分发机制 / TR-LAYER Dispatch Mechanism

### 2.1 核心概念 / Core Concept

Gateway 的核心设计洞察：**一个 Nginx 进程担任两个角色，通过 TR-LAYER 请求头区分。**

| 请求来源 / Request Source | TR-LAYER 值 | 内部层 / Internal Layer | 配置 Key |
|:---|:---|:---|:---|
| 客户端 (外部) | 不存在 / absent | `"1"` (L1 前端) | `"edge"` |
| Tavern L2 (缓存 MISS 回源) | `"2"` | `"3"` (L3 回源) | `"parent"` |

**为什么 `TR-LAYER: "2"` 内部是 `"3"`?**

> 采用一进制编号 (L1="1", L2=Tavern(Go), L3="3")，便于未来扩展新的逻辑层。
> `unified/dispatch.lua` 中的 `LAYER_TO_CFG_KEY` 映射负责转换。

### 2.2 层检测代码 / Layer Detection Code

**文件：** `unified/dispatch.lua:58-66`

```lua
function _M.layer()
    local h = ngx.req.get_headers()[proto.InternalLayerKey]  -- "TR-LAYER"
    if h == "2" then
        return "3"      -- L2 回源请求 → 作为 L3 处理
    end
    return "1"          -- 客户端请求 → 作为 L1 处理
end
```

---

## 3. L1 请求处理全流程 / L1 Request Processing Flow

### 3.1 时序图 / Sequence Diagram

```mermaid
sequenceDiagram
    participant Client as 客户端 Client
    participant Nginx as Nginx Core
    participant SSL as ssl_provider.lua
    participant Dispatch as dispatch.lua
    participant Sanitize as header_sanitize.lua
    participant ReqID as request_id.lua
    participant Cache as cache_rules.lua
    participant Phases as phase_dispatcher.lua
    participant ACL as acl/
    participant L2 as Tavern L2 :8080

    Client->>Nginx: TLS ClientHello (SNI: cdn.example.com)

    Note over Nginx,SSL: ssl_certificate_by_lua
    Nginx->>SSL: get_cert_by_sni("cdn.example.com")
    SSL->>SSL: config_loader.get_domain_config("cdn.example.com")
    SSL->>SSL: 加载 ssl.cert + ssl.key
    SSL-->>Nginx: Certificate

    Nginx->>Client: TLS Handshake Complete
    Client->>Nginx: GET /index.html HTTP/1.1

    Note over Nginx,Dispatch: === rewrite_by_lua ===
    Nginx->>Dispatch: rewrite()
    Dispatch->>Dispatch: layer = "1" (TR-LAYER absent)
    Dispatch->>Dispatch: set_ctx("layer", "1")
    Dispatch->>ReqID: inject() → X-Request-ID: abc123
    Dispatch->>Sanitize: strip_request()
    Note over Sanitize: 剥离客户端 TR-* 头
    Dispatch->>Cache: apply(prefetch, cache_time)
    Note over Cache: 注入 X-Prefetch / X-CacheTime
    Dispatch->>Dispatch: ngx.req.set_header("TR-LAYER", "1")
    Dispatch->>Phases: run("rewrite", "edge")
    Phases->>ACL: evaluate + execute (edge.rewrite rules)

    Note over Nginx,Dispatch: === access_by_lua ===
    Nginx->>Dispatch: access()
    Note over Dispatch: GET/HEAD/PURGE → "tavern_backend"
    Dispatch->>Dispatch: ngx.var.target = "127.0.0.1:8080"
    Dispatch->>Phases: run("access", "edge")
    Phases->>ACL: evaluate + execute (edge.access rules)
    Note over ACL: allow → continue / deny → 403

    Note over Nginx,L2: === proxy_pass ===
    Nginx->>L2: GET /index.html HTTP/1.1<br/>TR-LAYER: 1<br/>X-Request-ID: abc123

    L2-->>Nginx: 200 OK<br/>X-Cache: HIT from disk<br/>TR-ERRCODE: 0

    Note over Nginx,Dispatch: === header_filter_by_lua ===
    Nginx->>Dispatch: header_filter()
    Dispatch->>Sanitize: strip_response()
    Note over Sanitize: 剥离响应 TR-* 头
    Dispatch->>Sanitize: strip_response_layer1()
    Note over Sanitize: 剥离内部 X-* 头<br/>保留 X-Cache, X-Request-ID
    Dispatch->>Phases: run("header_filter", "edge")

    Note over Nginx,Dispatch: === body_filter_by_lua ===
    Dispatch->>Phases: run("body_filter", "edge")

    Nginx->>Client: 200 OK<br/>X-Cache: HIT from disk<br/>X-Request-ID: abc123

    Note over Nginx,Dispatch: === log_by_lua ===
    Dispatch->>Dispatch: ngx.var.layer = "1"
    Dispatch->>Phases: run("log", "edge")
    Note over Phases: log_extra actions →<br/>structured log fields
```

### 3.2 5 个 Nginx Phase 处理 / 5 Nginx Phase Handlers

| Phase | 文件名 / File | 函数 / Function | L1 关键操作 |
|:---|:---|:---|:---|
| `rewrite_by_lua` | `dispatch.lua` | `rewrite()` | 注入 Request ID, 清洗请求头, 缓存规则, 设置 TR-LAYER:1 |
| `access_by_lua` | `dispatch.lua` | `access()` | 路由选择 (L2 或 L3), DNS 解析 |
| `header_filter_by_lua` | `dispatch.lua` | `header_filter()` | 清洗响应头, 解析 X-Cache |
| `body_filter_by_lua` | `dispatch.lua` | `body_filter()` | 响应体过滤 (JSON 规则) |
| `log_by_lua` | `dispatch.lua` | `log()` | 设置 layer 变量, 记录健康状态 |

---

## 4. L3 回源处理全流程 / L3 Origin Fetch Flow

```mermaid
sequenceDiagram
    participant L2 as Tavern L2 (回源)
    participant Nginx as Nginx Core
    participant Dispatch as dispatch.lua
    participant Sanitize as header_sanitize.lua
    participant Config as config_loader.lua
    participant DNS as resty.dns.resolver
    participant Phases as phase_dispatcher.lua
    participant Balancer as unified/balancer.lua
    participant Annotate as error_annotate.lua
    participant Origin as 源站 Origin

    L2->>Nginx: GET /index.html HTTP/1.1<br/>TR-LAYER: 2<br/>X-Request-ID: abc123

    Note over Nginx,Dispatch: === rewrite_by_lua ===
    Nginx->>Dispatch: rewrite()
    Dispatch->>Dispatch: layer = "3" (TR-LAYER == "2")
    Dispatch->>Dispatch: set_ctx("layer", "3")
    Dispatch->>ReqID: inject() → 复用 L1 Request ID

    Note over Dispatch,Sanitize: 关键安全步骤
    Dispatch->>Sanitize: 剥离所有 TR-* 请求头
    Note over Sanitize: for _, h in ipairs(INTERNAL_HEADERS):<br/>  ngx.req.clear_header(h)

    Dispatch->>Phases: run("rewrite", "parent")

    Note over Nginx,Dispatch: === access_by_lua ===
    Nginx->>Dispatch: access()
    Dispatch->>Dispatch: ngx.var.target = "origin_pool"

    Dispatch->>Config: get_domain_config(ngx.var.host)
    Config-->>Dispatch: { origin: { servers: [...] } }

    Dispatch->>Dispatch: stash scheme → ngx.ctx.scheme

    loop 对每个 origin server
        Dispatch->>DNS: query(hostname)
        DNS-->>Dispatch: IP address
        Dispatch->>Dispatch: origin_peers[hostname] = {ip, port}
    end

    Dispatch->>Dispatch: set_ctx("origin_peers", origin_peers)
    Dispatch->>Dispatch: set_ctx("origin_servers", origin_servers)

    Dispatch->>Phases: run("access", "parent")

    Note over Nginx,Balancer: === balancer_by_lua ===
    Nginx->>Balancer: balance()
    Balancer->>Balancer: 检查 TR-UPS-ADDR 动态覆盖
    Balancer->>Balancer: WRR 选择 Main 组服务器
    alt Main 组全部 DOWN
        Balancer->>Balancer: WRR 选择 Slave 组服务器
    end

    Note over Nginx,Origin: === proxy_pass ===
    Nginx->>Origin: GET /index.html<br/>(无 TR-* 头)

    alt 源站 ≥500
        Origin-->>Nginx: 502 Bad Gateway
        Note over Nginx,Annotate: header_filter_by_lua
        Annotate->>Annotate: status >= 500 → TR-ERRCODE: 1
    else 源站正常
        Origin-->>Nginx: 200 OK
    end

    Nginx->>Dispatch: header_filter()
    Dispatch->>Annotate: annotate().  (设置 TR-ERRCODE)
    Dispatch->>Phases: run("header_filter", "parent")

    Nginx->>Dispatch: body_filter()
    Dispatch->>Phases: run("body_filter", "parent")

    Nginx-->>L2: 200 OK<br/>TR-ERRCODE: 0/1

    Note over Nginx,Balancer: === log_by_lua ===
    Dispatch->>Dispatch: ngx.var.layer = "3"
    Dispatch->>Balancer: register_success() or register_failure()
    Note over Balancer: 更新失败/成功计数<br/>管理服务器 UP/DOWN 状态
    Dispatch->>Phases: run("log", "parent")
```

---

## 5. ACL 规则引擎 / ACL Rule Engine

### 5.1 三层架构 / Three-Layer Architecture

```mermaid
graph TB
    subgraph "ACL Entry / 入口"
        Init["init.lua<br/>evaluate_rule(rule, ctx)<br/>execute_action(action, ctx)"]
    end

    subgraph "Condition Layer / 条件层"
        Cond["condition.lua<br/>evaluate(conditions, ctx)<br/>11 vars × 13 ops"]
    end

    subgraph "Operator Layer / 组合层"
        Op["operator.lua<br/>combine(conditions, op)<br/>AND / OR + 嵌套组"]
    end

    subgraph "Action Layer / 动作层"
        Act["action.lua<br/>execute(action, ctx)<br/>11 action types"]
    end

    Init --> Cond
    Init --> Op
    Cond --> Op
    Op --> Act

    subgraph "Execution Context / 执行上下文"
        Ctx["ngx.ctx<br/>ngx.var<br/>ngx.req<br/>ngx.header"]
    end

    Cond --> Ctx
    Act --> Ctx
```

### 5.2 规则执行流程 / Rule Execution Flow

```mermaid
flowchart TD
    A["phase_dispatcher.run(phase, cfg_key)"] --> B["加载 domain JSON config"]
    B --> C{"edge.<phase><br/>or parent.<phase><br/>has rules?"}
    C -- No --> D[Return]
    C -- Yes --> E["遍历 rules[]"]

    E --> F{"rule.op?"}
    F -- and (default) --> G["所有 if 条件<br/>必须满足"]
    F -- or --> H["任意 if 条件<br/>满足即可"]
    F -- nested --> I["递归处理<br/>rule.rules[]"]

    G --> J["operator.evaluate(conditions)"]
    H --> J
    I --> J

    J --> K{"所有条件满足?<br/>All conditions met?"}

    K -- Yes --> L["action.execute(action)"]
    L --> M{"terminal action?<br/>(allow/deny)?"}
    M -- Yes --> N[停止后续规则]
    M -- No --> O[继续下一条规则]

    K -- No --> O
    O --> P{"还有规则?<br/>More rules?"}
    P -- Yes --> E
    P -- No --> D
```

### 5.3 条件评估细节 / Condition Evaluation Detail

**文件：** `lualib/acl/condition.lua`

```lua
-- 支持 11 种变量类型
local evaluators = {
    uri           = function(ctx) return ngx.var.uri end,
    method        = function(ctx) return ngx.req.get_method() end,
    status        = function(ctx) return tostring(ngx.status) end,
    scheme        = function(ctx) return ngx.var.scheme end,
    remote_addr   = function(ctx) return ngx.var.remote_addr end,
    header_<name> = function(ctx, name) return ngx.req.get_headers()[name] end,
    arg_<name>    = function(ctx, name) return ngx.var["arg_" .. name] end,
    cookie_<name> = function(ctx, name) return ngx.var["cookie_" .. name] end,
}

-- 支持 13 种运算符
local operators = {
    eq       = function(a, b) return a == b end,
    ne       = function(a, b) return a ~= b end,
    regex    = function(a, b) return ngx.re.match(a, b) ~= nil end,
    prefix   = function(a, b) return string.sub(a, 1, #b) == b end,
    suffix   = function(a, b) return string.sub(a, -#b) == b end,
    contains = function(a, b) return string.find(a, b, 1, true) ~= nil end,
    gt  = function(a, b) return tonumber(a) >  tonumber(b) end,
    gte = function(a, b) return tonumber(a) >= tonumber(b) end,
    lt  = function(a, b) return tonumber(a) <  tonumber(b) end,
    lte = function(a, b) return tonumber(a) <= tonumber(b) end,
    cidr = function(a, b) --[[ IP range check ]] end,
}
```

---

## 6. 负载均衡器 / Load Balancer

### 6.1 架构设计 / Architecture

**文件：** `unified/balancer.lua`

```mermaid
flowchart TD
    Start["balancer.balance()<br/>called from balancer_by_lua"] --> Override{TR-UPS-ADDR<br/>or backend_ip in ctx?}

    Override -- Yes --> Direct["set_current_peer(ip, port)<br/>Dynamic Override"]
    Direct --> Done["Return"]

    Override -- No --> MainGroup{"Main 组<br/>有可用服务器?"}

    MainGroup -- Yes --> MainWRR["WRR 选择 Main 服务器"]
    MainWRR --> CheckMain{"选中的 Main<br/>是否 DOWN?"}
    CheckMain -- Yes --> NextMain["尝试下一个 Main"]
    NextMain --> MainGroup
    CheckMain -- No --> SetPeer["set_current_peer(ip, port)"]

    MainGroup -- No (所有 Main DOWN) --> SlaveGroup{"Slave 组<br/>有可用服务器?"}

    SlaveGroup -- Yes --> SlaveWRR["WRR 选择 Slave 服务器"]
    SlaveWRR --> CheckSlave{"选中的 Slave<br/>是否 DOWN?"}
    CheckSlave -- Yes --> NextSlave["尝试下一个 Slave"]
    NextSlave --> SlaveGroup
    CheckSlave -- No --> SetPeer

    SlaveGroup -- No (全部 DOWN) --> Fail["ngx.exit(502)"]

    SetPeer --> Protocol{"Protocol?"}
    Protocol -- "http" --> Done
    Protocol -- "https" --> TLS["Enable upstream TLS<br/>proxy_ssl: on"]
    Protocol -- "follow" --> Mirror["Mirror client scheme<br/>ctx.scheme == 'https'?"]
    Mirror --> Done
    TLS --> Done
```

### 6.2 WRR 算法 / Weighted Round Robin

```
示例: Main 组 [A(w=3), B(w=2), C(w=1)]
分配序列: A, A, A, B, B, C, A, A, A, B, B, C, ...

实现:
- 维护每个服务器的当前权重
- 每次选择权重最大的服务器
- 选择后减去总权重，下一轮恢复
```

### 6.3 故障追踪状态机 / Failure Tracking State Machine

```mermaid
stateDiagram-v2
    [*] --> UP: 初始状态

    UP --> UP: 成功响应<br/>(失败计数=0)
    UP --> DOWN: 连续失败 ≥ max_fails<br/>(记录 down_time)

    DOWN --> DOWN: 仍在 fail_timeout 内<br/>(跳过选择)
    DOWN --> UP: fail_timeout 过期<br/>+ 首次请求成功<br/>(失败计数=0)
    DOWN --> DOWN: fail_timeout 过期<br/>+ 首次请求失败<br/>(重置 down_time)
```

**记录时机：** `log_by_lua` 阶段 — 此时 `ngx.var.upstream_status` 已知。

**Per-worker, in-memory only** — 故障状态不在 worker 间共享。

---

## 7. 配置加载器 / Config Loader

### 7.1 架构 / Architecture

**文件：** `lualib/config_loader.lua`

```mermaid
flowchart TD
    Start["config_loader initializes<br/>init_by_lua"] --> Source{GATEWAY_CONFIG_URL?}

    Source -- "file:///path/" --> Local["目录扫描<br/>scan_directory()"]
    Local --> LoadLocal["遍历 *.json 文件"]
    LoadLocal --> VerifyLocal["验证 hash"]
    VerifyLocal --> StoreLocal["存入 config_store"]

    Source -- "https://..." --> Remote["HTTP GET<br/>fetch_remote()"]
    Remote --> ParseRemote["解析 JSON 响应"]
    ParseRemote --> VerifyRemote["验证 hash"]
    VerifyRemote --> StoreRemote["存入 config_store"]
    Remote --> Timer["ngx.timer.every(30s)<br/>定时刷新"]

    Source -- Not set --> Noop["不加载域名配置"]

    StoreLocal --> Ready["get_domain_config(host)<br/>可用"]
    StoreRemote --> Ready
```

### 7.2 共享内存存储结构 / Shared Memory Storage Structure

```
config_store (16MB):
  domain:cdn.example.com    → JSON config string
  hash:cdn.example.com      → MD5 hash
  domain:static.example.com → JSON config string
  hash:static.example.com   → MD5 hash
  __domains__               → ["cdn.example.com", "static.example.com"]
  __version__               → 42
  __updated__               → 1717948800 (unix timestamp)
  __source__                → "file:///path/to/rules/"
```

### 7.3 API

```lua
-- 获取域名完整配置
local cfg = config_loader.get_domain_config("cdn.example.com")
-- → { id = "cdn.example.com", origin = {...}, edge = {...}, ... }

-- 轻量级变更检测 (不解码完整 JSON)
local id, hash = config_loader.get_domain_hash("cdn.example.com")
-- → "cdn.example.com", "1a3fc2d96f49972e0e9d1f0d8048c8c6"
```

---

## 8. 请求上下文数据流 / Request Context Data Flow

### 8.1 ngx.ctx 使用 / ngx.ctx Usage

`ngx.ctx` 在每个请求的各个 nginx phase 之间传递状态：

| 字段 / Field | 设置阶段 / Set Phase | 使用阶段 / Use Phase | 来源 / Source |
|:---|:---|:---|:---|
| `layer` | `rewrite` | All phases | `dispatch.lua` |
| `scheme` | `access` | `balancer` | `dispatch.lua` (L3) |
| `origin_peers` | `access` | `balancer` | `dispatch.lua` (L3) |
| `origin_servers` | `access` | `balancer` | `dispatch.lua` (L3) |
| `backend_ip` | `access` (fallback) | `balancer` | `dispatch.lua` (L3 fallback) |
| `backend_port` | `access` (fallback) | `balancer` | `dispatch.lua` (L3 fallback) |
| `balancer_last_key` | `balancer` | `log` | `balancer.lua` |

### 8.2 数据流图 / Data Flow Diagram

```mermaid
sequenceDiagram
    participant rewrite as rewrite_by_lua
    participant access as access_by_lua
    participant balancer as balancer_by_lua
    participant header_filter as header_filter_by_lua
    participant log as log_by_lua
    participant ctx as ngx.ctx

    rewrite->>ctx: set("layer", "1" or "3")
    rewrite->>ctx: set("scheme", ...) (L3)

    access->>ctx: get("layer")
    access->>ctx: set("origin_peers", ...) (L3)
    access->>ctx: set("origin_servers", ...) (L3)
    access->>ctx: set("backend_ip", ...) (L3 fallback)

    balancer->>ctx: get("backend_ip") -- dynamic override
    balancer->>ctx: get("origin_peers")
    balancer->>ctx: get("origin_servers")
    balancer->>ctx: get("scheme") -- protocol follow
    balancer->>ctx: set("balancer_last_key", ...)

    header_filter->>ctx: get("layer")

    log->>ctx: get("layer") → set ngx.var.layer
    log->>ctx: get("balancer_last_key") → register success/failure
```

### 8.3 跨模块调用关系 / Cross-Module Call Graph

```
dispatch.lua
├── require("vendor.protocol")        → 协议常量
├── require("lualib.header_sanitize") → Header 清洗
├── require("lualib.request_id")      → Request ID
├── require("lualib.cache_rules")     → 缓存规则
├── require("lualib.error_annotate")  → 错误标注 (L3)
├── require("lualib.phase_dispatcher") → 阶段规则
├── require("lualib.config_loader")   → 域名配置 (L3: get_domain_config)
└── require("lualib.xcache_parser")   → X-Cache 解析 (L1)

phase_dispatcher.lua
└── require("lualib.acl")            → ACL 引擎

balancer.lua (independent, called from balancer_by_lua)
├── ngx.ctx  ← origin_peers, origin_servers, backend_ip, scheme
└── ngx.shared.addr_cache ← tavern, layer3
```

---

## 9. 错误处理 / Error Handling

| 组件 / Component | 机制 / Mechanism |
|:---|:---|
| **L1 → L2 错误** | `proxy_intercept_errors` + 域名配置 `error_page` |
| **L2 → L3 错误** | `TR-ERRCODE: 1` 标注允许 Tavern 缓存错误响应 |
| **源站 5xx** | `error_annotate.annotate()` — `status >= 500` → 设置 `TR-ERRCODE: 1` |
| **自定义错误页** | `error_page.lua` — 渲染域名 HTML 错误页，内置通用 fallback |
| **负载均衡故障** | 每源站追踪连续失败次数; `max_fails` + `fail_timeout` |
| **配置规则异常** | `pcall` 包装 — 规则执行错误只记录日志，不影响请求处理 |

### pcall 安全包装 / pcall Safety Wrapper

```lua
-- dispatch.lua 中的错误安全包装
local function spcall(phase_name, fn, ...)
    local ok, err = pcall(fn, ...)
    if not ok then
        ngx.log(ngx.ERR, "dispatch: error in ", phase_name,
                " phase: ", tostring(err))
    end
end
```

---

## 10. 相关文档 / Related Documents

- [Gateway 项目文档 / Gateway Project](./01-project.md)
- [Gateway 功能文档 / Gateway Features](./02-features.md)
- [生态概览 / Ecosystem Overview](../ecosystem/overview.md)
- [协议规范 / Protocol Specification](../ecosystem/protocol.md)
- [Gateway DESIGN (原始设计) / Gateway DESIGN](../../../gateway/DESIGN.md)

---

*Document generated: 2026-06-09 | Source: gateway DESIGN.md, dispatch.lua, balancer.lua, lualib/*.lua analysis*
