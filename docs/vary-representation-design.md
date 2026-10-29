# HTTP Vary 与资源转换缓存改造设计

日期：2026-10-29。状态：分析与设计建议，尚未实现。

后续实施安排见 [详细落地方案](./vary-representation-implementation-plan.md)，其中包含变更批次、文件职责、存储提交协议、阶段验收和灰度回滚步骤。

## 1. 结论与范围

Tavern 已有 URL 根索引和 Vary 子对象的雏形，可以作为存储基础，但目前不适合直接承载完整的内容协商和图片转换缓存。需要先修复变体匹配、索引更新、回源合并及失效语义，再增加源资源与派生资源的关系。

本设计区分三个能力：

1. **透明 Vary 缓存**：源站返回不同表示，Tavern 按源站的 Vary 正确缓存、选择和刷新。
2. **派生表示缓存**：Tavern 或转换服务选择输出格式，等价的转换请求复用同一份成品。
3. **源资源复用**：JPEG → WebP、JPEG → PNG 共用同一份 JPEG 源资源；PNG → JPEG 共用对应 PNG 源资源。

Vary 声明影响响应选择的请求头，不负责执行转换，也不建立不同 URL 之间的资源等价关系。图片格式协商通常使用 `Vary: Accept`；gzip/br 传输内容编码使用 `Vary: Accept-Encoding`。两者同时存在时需要组合隔离。

“完整支持 Vary”包括任意合法选择头、多头组合、缺失值、星号、多种 Vary 字段集合、条件验证、Range/HEAD、并发合并、PURGE 和存储迁移。它不等于本轮一并重写所有 HTTP 缓存指令。

## 2. 当前实现与确定的问题

以下来自静态代码追踪，没有运行复现实验。文件位置和推导路径可用于后续定向测试。

### 2.1 已有基础

- `server/middleware/caching/processor.go:95`：先根据 URL 生成对象 ID，选择 bucket 并读取元数据。
- `server/middleware/caching/caching.go:85`：处理顺序为 State → Prefetch → Vary → Revalidate → FileChanged → FillRange。
- `server/middleware/caching/caching_vary.go:49`：根元数据为 `FlagVaryIndex` 时，根据请求头生成子对象 ID。
- `api/defined/v1/storage/object/object.go:35`：Metadata 支持 Vary 根/子标志和 VirtualKey。
- `storage/bucket/disk/disk.go:274`、`storage/bucket/memory/memory.go:109`：删除根索引时尝试级联删除子对象。

当前查询大致为：URL → 根索引中的统一 Vary 头集合 → 请求头字符串 → 子对象 → 新鲜度/条件验证 → 分块响应。

### 2.2 协议与索引缺陷

| 问题 | 代码证据 | 影响与修复方向 |
| --- | --- | --- |
| 未特殊处理 `Vary: *` | `caching_vary.go:125`；`varycontrol/vary.go:30` | 星号被当成普通字段，通常生成 `*=`，不同请求可能复用同一响应。建议该响应直接不入可复用缓存，保留对下游的原始 Vary。 |
| 字段名未统一大小写 | `varycontrol/vary.go:90` | `Accept` 与 `accept` 比较不同，可能被重复记录或触发不必要重建。按字段名大小写不敏感处理并验证 token。 |
| 所有字段值都按逗号拆分、排序 | `varycontrol/vary.go:113` | 对有顺序含义、引号内逗号或未知语法的字段会错误合并。未知字段采取保守匹配，只有已知语义等价的字段才规范化。 |
| 缺失与空值混同 | `varycontrol/vary.go:36` | 请求中不存在字段与存在空字段都编码为 `Field=`，不满足缺失值匹配规则。记录存在位和有序值。 |
| 字符串拼接存在歧义 | `varycontrol/vary.go:49` | `A=x&B=y&B=z` 可以来自 `A: x&B=y; B: z`，也可以来自 `A: x; B: y&B=z`。这里分号只用于描述两个独立字段。应使用带长度/类型的结构编码再哈希。 |
| 变体键被作为头列表再次解析 | `caching_vary.go:274` | `Accept=image/png,image/webp` 被 Clean 拆成 `Accept=image/png` 和 `image/webp`；清理时重建的 ID 不是真实子对象 ID，产生漏删和版本数失真。索引直接保存 VariantID。 |
| 已选中子对象后跳过 Vary 更新 | `caching_vary.go:83` | 变体重新回源后，200/304 的 Vary 改变不会经过完整的重新索引处理。 |
| 新 200 移除 Vary 未正确建模 | `caching_vary.go:155,164,242` | 无 Vary 被导向沿用旧维度的路径；“移除后降级”分支处于非空响应 Vary 路径中，无法按注释生效。区分完整 200 与 304 元数据合并。 |
| Vary 改变后混合新旧字段 | `caching_vary.go:287` 和 `:100` | 子 ID 可能按新字段集生成，根查询却使用新旧字段并集，导致查找键与存储键不一致。每个条目独立保存字段集合。 |
| 忽略配置没有生效 | `caching_vary.go:340` | `varyIgnoreKey` 只写入，从未用于匹配。不能简单把它补成任意忽略 Cookie 等字段；严格模式应遵循源站声明。 |
| 超限错误未切换为禁止写入 | `caching_vary.go:89` | 错误只记日志，继续返回响应，而此前已可能挂上写缓存 Body。需要明确 admission 决策，超限仅透传或淘汰某个变体，不向未解析的根对象写入。 |

### 2.3 并发与生命周期缺陷

- **冷缓存可能串格式**：`caching.go:175` 用当前对象 ID 合并整个回源。冷缓存尚不知道 Vary，Accept 不同的请求共用 URL 根 ID；已知根索引但子变体 MISS 时也仍可能使用根 ID。
- **第二层合并也不区分变体**：`proxy/proxy.go:193` 的 onceKey 仅包含 Method、URL、Range；即使上层修好，代理层仍可能把不同 Accept、Accept-Encoding 或条件头的请求合并。
- **软 PURGE 未传递到子对象**：`storage/storage.go:230`、`storage/migrator.go:342` 仅修改根 ExpiresAt，而 Vary 查询选中子对象后，新鲜度检查针对子元数据。根过期不能可靠地使全部变体过期。
- **索引更新会丢失并发新增**：每个请求读、修改并写回根元数据，`processor.go:142` 未执行按资源的原子合并；不同变体并发填充可能互相覆盖 VirtualKey。
- **写入时序不利于安全扩展**：`caching.go:474` 在 Vary/后处理之前建立异步缓存写入包装；回调读取可变的 `c.id/c.md`。应先确定最终表示、缓存资格和写入目标，再创建固定目标的写入器。
- **迁移没有表示集合语义**：`disk.go:518` 仅迁移当前 ID 的块及元数据，随后调用会级联删除的 discard。迁移根索引不能因此保证子对象也迁移；迁移单个子对象又可能使根查询在原 bucket 找不到它。
- **中间件扩展点未贯通**：`caching.go:111` 接收的 `origin http.RoundTripper` 没有用于实际回源，`doProxy` 直接调用 proxyClient。仅把转换中间件放在 caching 内层不会按链路预期执行。
- 当前未发现专门的 Vary 单元测试及验收断言；mockserver 有两个示例路径，不能证明组合、刷新和并发行为正确。

## 3. 两种模式必须明确区分

### 3.1 透明代理模式

源站自己决定 JPEG/WebP/PNG，Tavern 不推测源站算法。

- 按源站响应 Vary 和实际发给源站的请求快照匹配。
- 不把所有“包含 image/webp”的 Accept 统一折叠成一个键；源站可能考虑 q、其他类型、参数或自己的选择逻辑。
- 可以做经规范证明等价的规范化；对未知字段宁可多存一个版本，也不能错误复用。
- 此模式能复用缓存成品，不能凭空让源站的多次转换共用 Tavern 内的一份原图。

### 3.2 Tavern 管理转换模式

由 Tavern 的协商器决定转换参数和输出格式，并明确控制源资源请求。

- 对外按客户端原始请求选择表示，对下游输出 `Vary: Accept` 等真实选择维度。
- 对内以完整转换规格和源版本作为成品缓存身份。
- 多个不同 Accept 在协商结果相同时可复用成品；例如都选中 `image/webp, quality=82`。
- 内部规范化结果使用类型化上下文传递，不能信任客户端提供的 `X-Image-Format` 等伪内部头。
- 如果源资源本身受 `Accept-Language`、Cookie 等影响，先按源站 Vary 选出确切的源表示；不能只凭 URL 把它们共用。

## 4. 目标数据模型

建议增加 RepresentationStore 协调层，继续利用现有 bucket、IndexDB 和分块文件。将索引职责与响应对象职责分开。

```text
ResourceIndex
  schema_version
  resource_id
  purge_epoch / revalidate_epoch
  entries[]
    vary_fields[]
    selector_fingerprint
    selector_snapshot { field_name, present, ordered_values[] }
    representation_id
    response_date
    storage_location（只有允许独立迁移时才需要）

RepresentationMetadata
  representation_id / resource_id
  kind: origin | derived
  response_headers / status / freshness / validators
  body_generation / chunks / size
  source_representation_id（derived）
  source_version（derived）
  transform_spec / transform_policy_version / encoder_version（derived）
  validated_epoch / purge_epoch
```

每个条目保存自己的 Vary 集合，不能用所有响应的字段并集替代。索引可按字段集合分组，一次计算组内请求指纹，再直接查找条目；上限限制组和条目数，避免每次扫描大量磁盘对象。

有多个可匹配响应时遵循 RFC 的有效 Vary 与最新响应选择规则；没有 Vary 的旧默认响应不能抢先遮蔽已有协商版本。源站完整 200 的 Vary 改变/移除，更新对应响应条目，不能自动替所有其他表示声明新的全局字段集合。

指纹使用版本化的 CBOR 或其他无歧义结构编码。字段名规范化；字段缺失、空值、多行值分开表达；未知字段保留顺序和内部语法。缓存键和日志不直接嵌入 Cookie 等原始选择值。需要恢复请求快照时，仅在缓存策略允许的范围内受控保存，不把摘要当成完整的重验证请求。

建议键空间：

```text
ResourceID = H(namespace, scheme, authority, escaped_path, relevant_query)
OriginVariantKey = H("origin-v2", ResourceID, vary_fields, selector)
OriginRepresentationID = H(OriginVariantKey, body_generation)
DerivedID = H("derived-v2", source_representation_id, source_version,
              normalized_transform_spec, policy_version, encoder_version)
```

全部参数使用结构编码，不直接连接字符串。源表示的 body_generation 在内容变化时更换，用于避免不同版本分块混合；它与 PURGE epoch 是不同概念。

Query 的语义不能靠 Vary 补救。显式 `?format=webp`、`width`、`quality` 等必须进入 URL 缓存键或经过明确解析进入转换规格；忽略整个 query 的旧配置不能默默适用于转换路由。不同路径、域名的图片只有在配置建立同一 source_id 映射后才可共享源资源，不自动删除后缀或查询参数。

## 5. 请求处理与存储发布

### 5.1 透明缓存

1. 生成 ResourceID，读取 ResourceIndex。
2. 按各条目的选择字段匹配请求，选出表示，然后检查 freshness 和失效 epoch。
3. HIT 只对选中的表示执行条件请求与 Range，不能使用其他格式的 ETag 或字节偏移。
4. MISS/过期时构造实际 upstream request，并保留匹配所需快照。
5. 接收响应头，处理完整 200、304 或部分响应；先完成 Vary、缓存资格、表示 ID 和代际决策。
6. 创建绑定固定 ID/代际的写入器，再读取响应体。
7. 发布可读取的表示后原子更新索引；失败时不发布成功条目。未完成对象继续使用已有分块能力时，必须明确标记 incomplete 并只开放已确认的块。

304 更新适合该验证请求的表示；缺失字段不等于删除旧元数据，新增/改变 Vary 后根据本次验证请求重新索引。完整 200 不带 Vary 则不继承旧响应的选择维度。ETag 是不透明、大小写敏感的验证器，不能因格式转换沿用源图强 ETag。

### 5.2 转换缓存

1. 解析自动协商或显式输出参数，形成待完成的 TransformSpec。
2. 在需要时加载源表示的元数据，确定输入能力、透明度/动画策略、源版本；只要这些信息可用，无须读取源图 Body 即可定位成品。
3. 成品 HIT：直接发送该成品的头和字节，不解码、不转换。
4. 成品 MISS：读取新鲜的源资源；源资源 MISS 才回源。
5. 对该 DerivedID 合并转换任务，解码源图、编码目标格式、完整落盘后发布成品。
6. 生成正确的 Content-Type、Content-Length、表示验证器和 Vary，再响应客户端。

转换缓存初版推荐完整成品后发布，避免半成品被其他请求读取。可以使用受限临时文件，不要求把所有字节放进内存。源缓存、转换器和最终表示存储分别承担职责。

HTTP RoundTripper 扩展应先接通 next/origin。通用缓存只依赖请求执行接口；源图获取走独立的源表示缓存调用路径，避免用相同 URL 递归进入转换中间件。转换提供者产生与普通回源响应相同的表示描述和 Body，交给统一存储层管理。

## 6. 图片协商和复用实例

假定输出接口是 `GET /images/a.jpg`，源表示 S1 为 JPEG，版本为 v7。

| 请求 | 选择结果 | 内部复用 |
| --- | --- | --- |
| `Accept: image/webp,image/jpeg;q=0.8` | WebP | S1/v7 + WebP + quality 82 |
| `Accept: image/webp,image/png;q=0.5` | WebP | 同一成品，仅适用于 Tavern 管理协商模式 |
| `Accept: image/png` | PNG | S1/v7 + PNG，复用源 JPEG |
| `Accept: image/jpeg` | 原始 JPEG 或显式 JPEG 转换结果 | 无转换时可引用源 Body，响应元数据仍独立 |

另一张 PNG 源图 S2 可以生成 S2/v3 + JPEG；源图身份独立，不能因为输出都为 JPEG 就共享字节。

选择器必须处理 q=0、媒体范围具体程度、通配符、参数和服务端支持集合。例如 `image/webp;q=0,image/*;q=1` 不能通过通配符重新允许 WebP。缺失 Accept 和 `*/*` 的选择策略需稳定配置，建议默认保留原格式，避免无明确偏好时额外转换。

无法满足 Accept 时，建议返回 406；转换异常只有在源格式也可被客户端接受、且符合路由约定时才回退到源图。显式 `format=webp` 是否允许回退必须由接口契约决定，不能把 JPEG Body 存进 WebP 成品键。

TransformSpec 至少包含输出 MIME、质量及影响字节的策略；未来增加尺寸、裁剪、方向、色彩空间、透明度背景、动画处理时必须同步扩展规格和键。默认值也要展开编码，策略/编码器版本变化必须换键。

PNG → JPEG 的透明度需要明确背景色或拒绝转换；动画源图需要明确保留动画或拒绝/显式首帧策略。JPEG → PNG 可以转换，但不能恢复已经丢失的质量，因此不建议自动优先选择 PNG；显式请求可支持。

## 7. HTTP 行为约束

- 下游 Vary 保留所有实际影响对外表示选择的客户端请求维度。转换层新增 Accept 时与原有相关维度合并、去重，不能覆盖 Accept-Encoding、Origin 等已有约束。
- 原图和每种成品独立维护响应头。转换后更新 Content-Type、长度及 http.Response 对应字段，删除源 Content-Range，重算或移除摘要与强验证器；内容编码变化时同步调整 Content-Encoding。
- 成品强 ETag 可按最终编码字节计算。源站重验证使用源图验证器，客户端重验证使用所选成品验证器；源图 304 不能直接作为成品客户端的 304。
- HEAD 命中已有成品时返回该表示的头且无 Body。HEAD 冷请求不得把源 JPEG 的长度伪装成 WebP 长度；可按策略内部执行一次 GET/转换，或省略只能生成后获知的字段。
- Range 针对最终编码后的字节。成品未生成时必须先获取完整源图并转换，不能把 WebP 的 Range 转发为 JPEG 的 Range；成品完成后复用现有分块/多 Range 响应，检查该成品的 If-Range。
- 遵守请求和响应的 `Cache-Control: no-transform`。请求含该指令时不能命中需要转换的派生成品；其原样响应也不能污染自动转换选择。建议该请求走独立的原样缓存路径，并确保下游能区分相应响应，必要时不缓存对外响应。
- 派生新鲜度不超过可依赖的源新鲜度；源过期时先验证依赖，源 304 可延长有效依赖，源变化则切换 source_version。保留 Age 等信息计算源的剩余新鲜度，不把每次生成派生资源视为获得新的完整源 TTL。
- 共享缓存资格先于 Vary 匹配。private/no-store、带 Authorization 的共享限制仍需遵守；增加 Vary 不能替代这些限制。

## 8. 并发、失效与迁移

### 8.1 分层合并

**未知 Vary 的透明回源**：初版建议不共享响应体。后续可先收到 leader 的响应头，再逐个验证 follower 是否满足 Vary 和其他复用条件；不匹配、星号或不可共享者各自回源。即使预先使用全部请求头指纹，也不能据此共享 `Vary: *` 响应。

**已知 Vary 的透明回源**：用完整请求等价条件构造 flight key，并在响应头到达后再次校验，因为源站可能改变 Vary。不得仅凭旧的 VariantID 就认为可共享。Method、Range、条件头、共享缓存资格等也须兼容。

**受控源图/转换**：源资源获取按确切源请求合并；转换按 DerivedID 合并。同源不同目标可共享源字节，转换任务分别执行。同目标不同 Accept 若由本地算法确定为等价，可以共享同一成品。

同时改造或收敛 proxy.singleflight 与 caching 的 object/chunk flight，避免上层隔离后在下层再次合并。异步刷新使用独立请求状态及元数据快照，不复用前台可变 Caching 对象。

### 8.2 索引发布与 PURGE

- 对同 ResourceID 的索引更新串行化，持锁后重新读取并合并；不能整份覆盖旧快照。版本数预算同时计入正在填充的预留项，超限透传或按变体 LRU 淘汰。
- 普通内容块可以复用存储结构，但写入器持有固定表示 ID 和 body_generation；旧版本块不能被 Store 的“合并旧块”逻辑并入新版本。
- 先写子对象，再发布根索引引用。中断最多留下不可达对象，由 GC 回收；索引不能指向尚不可安全读取的数据。
- soft PURGE 提升 revalidate_epoch，所有变体和派生依赖在使用前比较 validated_epoch，不能只看子对象 ExpiresAt。
- hard PURGE 先持久化新的 purge_epoch/墓碑并移除索引可达性，再清理旧对象。进行中的旧请求提交前再次检查 epoch，防止删除后回填复活。
- 删除源图使依赖它的派生表示逻辑失效；删除某个派生成品不必删除源图和其他格式。目录 PURGE、前缀匹配和域名统计也需区分资源与内部表示键。
- 进程内资源锁只保证单写入进程。若 tableflip 重叠期允许同时写同一存储，必须先确认交接约束，或采用存储级原子更新/互斥；不能把进程内锁当成跨进程事务。

### 8.3 bucket 定位与迁移

初版建议以 ResourceID 固定资源集合的 bucket 定位，源表示、派生表示和索引按集合迁移；复制全部可达表示后再切换目录，最后清理旧位置。禁止当前“移动根索引，然后递归删除旧子对象”的路径用于新索引。

若后续需要单个热门成品独立提升，则索引增加明确位置及代际，并让查询/删除通过 RepresentationStore 解析位置，不能继续假设 root 与 child 同 bucket。bucket 变更、淘汰根索引和重启重建同样必须保留可达性或安全地转为 MISS。

## 9. 转换后端与运行限制

仓库构建要求 `CGO_ENABLED=0`，当前未发现图片转换实现。不要在 Vary 修复阶段直接引入需要本地 C 运行时的编解码依赖。

建议定义独立转换器接口，接收源描述、可读取的完整源和规范化 TransformSpec，返回最终表示描述与 Body。首个实现可适配已有图片服务或独立转换 worker；后续评估纯 Go 实现能否满足 JPEG/PNG/WebP 的编码能力、质量与吞吐。具体依赖需另行验证，本文不作能力或性能承诺。

转换设置并发数、队列、超时、最大输入字节和解码像素预算；失败不发布成品。非图片响应、错误页和未经允许的状态不进入转换器。避免对已生成成品再次做损失转换，派生尽量从稳定源资源生成。

## 10. 建议实施顺序与验收

### 阶段一：Vary 正确性

重构 varycontrol 的解析和选择快照；修复星号、缺失/空值、字段大小写、逗号键、超限写入和两层 collapsed forwarding。为索引引入版本格式，明确 200/304 的更新流程。停止把文档中的“完整支持”当成当前已验证状态。

### 阶段二：表示存储与生命周期

引入 RepresentationStore、固定写入上下文和索引合并提交，完成 epoch PURGE、源代际隔离及集合迁移；接通 next/origin。普通资源也使用统一表示模型，默认只有一个无 Vary 表示。

### 阶段三：图片转换

先实现 Accept 协商、JPEG/PNG/WebP 规格、源图缓存与转换器适配；接入成品缓存、验证器、HEAD 和 Range。由外部服务转换也可使用同一表示模型。

### 阶段四：观测与迁移优化

增加源命中、成品命中、源回源、转换次数/耗时、选择结果、超限 bypass、索引冲突和不可达对象回收指标。指标标签不包含任意 URL 或 Accept 原文，避免高基数。

旧元数据使用不同键空间/格式版本；旧 Vary 缓存无法可靠还原选择快照和损坏的 VirtualKey，建议首次访问安全 MISS 并重建，旧文件延迟回收。已有普通缓存是否复用由格式迁移检查决定，不对损坏的旧 Vary 数据做推测修复。

| 验收组 | 必须覆盖 |
| --- | --- |
| Vary 基础 | 多行、大小写、重复字段、多字段组合、缺失/空值、星号、未知字段与引号/分隔符 |
| Vary 变化 | 200 改变/移除 Vary、304 更新/新增选择字段、多个字段集合共存、默认响应与变体选择 |
| 并发 | 冷缓存不同 Accept/Accept-Encoding 不串响应；并发新增不丢索引；刷新/PURGE 与迟到写入竞争 |
| 图片复用 | JPEG→WebP/PNG、PNG→JPEG；等价 Accept 只转换一次；同源不同格式源回源一次；q=0 不返回被拒类型 |
| HTTP | 成品 304/HEAD/Range/If-Range、no-transform、gzip/br 与图片格式组合、源 304/源变化、转换失败与不接受源格式 |
| 生命周期 | soft/hard/目录 PURGE、重启、淘汰、迁移、填充中失败与超限，不出现跨版本块混合或缓存复活 |

遵循仓库验证策略：实现后优先运行受影响 package 的定向测试；并发模块改动使用对应范围的 race；存储接口/跨 package 改造再按影响扩大验证。此设计阶段不运行构建或全仓库测试。

## 11. 标准依据

- [RFC 9111 §4.1：Vary 匹配与表示选择](https://www.rfc-editor.org/rfc/rfc9111.html#section-4.1)
- [RFC 9111 §4 与 §4.3.4：响应复用、请求合并及 304 更新](https://www.rfc-editor.org/rfc/rfc9111.html#section-4)
- [RFC 9110 §12.5.1：Accept](https://www.rfc-editor.org/rfc/rfc9110.html#section-12.5.1)
- [RFC 9110 §12.5.5：Vary](https://www.rfc-editor.org/rfc/rfc9110.html#section-12.5.5)
- [RFC 9110 §7.7：消息转换](https://www.rfc-editor.org/rfc/rfc9110.html#section-7.7)

这些规范用于约束协议行为；RepresentationStore、源/派生对象分层、键空间、迁移和实施顺序是针对本项目的设计建议。
