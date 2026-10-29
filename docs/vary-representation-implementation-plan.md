# HTTP Vary 与资源转换缓存详细落地方案

日期：2026-10-29。

状态：实施中。P1.1—P1.3 已实现并通过定向验证；P1.4 及后续批次尚未完成。勾选框只标记已经落实的工作，v2 持久化缓存和图片转换尚未开放。

前置分析：[HTTP Vary 与资源转换缓存改造设计](./vary-representation-design.md)。前置文档说明问题和目标架构；本文进一步确定模块边界、实施批次、阶段依赖、兼容方式和验收门槛。若两份文档在实施细节上存在差异，以本文的细化决策为准。

## 1. 交付目标与实施原则

最终交付以下能力：

1. 透明代理正确处理任意合法 Vary 选择字段，包括组合字段、星号、字段缺失、条件验证及字段集合变化。
2. 同一源图片能够生成 JPEG、PNG、WebP 等不同表示；相同转换规格复用成品，不同规格复用源资源。
3. 变体及派生资源的并发填充、软/硬 PURGE、目录 PURGE、重启、淘汰和迁移具有一致的失效语义。
4. 保留现有磁盘/内存 bucket、IndexDB 及分块响应基础，避免在 Vary 修复期间引入图片编解码依赖。

实施规则：

- 按阶段一 → 阶段二 → 阶段三 → 阶段四推进，每阶段内部继续拆成小批次。
- 阶段是工程里程碑，不表示每个中间提交都可直接用于生产。阶段一的安全止损补丁可以独立交付；新的 v2 持久化缓存需经过阶段二生命周期验收后开放，转换功能需再经过阶段三验收。
- 从阶段一就确定统一的 v2 数据格式，阶段二补全协调层；不先制造另一套临时 Vary 格式再迁移一次。
- 测试围绕可观察行为建立：响应类型/字节、源站调用次数、转换次数、重启后可达性和 PURGE 后的新鲜度。避免仅验证内部函数恰好按实现返回。
- 原方案编写阶段不实现代码、不运行构建或测试；进入实施后按批次运行本方案第 9 节的定向检查。本次 P1.1/P1.2 未新增转换后端或编解码依赖。

## 2. 总体排期与变更批次

不预估未经验证的工期，以依赖和完成条件安排顺序。

| 批次 | 交付内容 | 前置依赖 | 可单独发布 |
| --- | --- | --- | --- |
| P1.1 | 冷缓存合并止损、星号/非法 Vary 旁路、缓存资格优先级 | 无 | 可以，需同步处置旧 Vary 数据 |
| P1.2 | Vary 解析、快照和无歧义指纹 | P1.1 | 随调用方集成发布 |
| P1.3 | v2 数据结构、索引读写基础和变体查询 | P1.2 | 先关闭外部流量 |
| P1.4 | 200/304 更新、版本预算和索引合并 | P1.3 | 与 P2 生命周期闭环后发布 |
| P1.5 | 透明 Vary 行为回归 | P1.1—P1.4 | 阶段验收 |
| P2.1 | 固定写入目标、代际隔离及分块状态 | P1.5 | 与后续生命周期一起发布 |
| P2.2 | 持久化控制记录、失效屏障与提交协议 | P2.1 | 同上 |
| P2.3 | 文件/目录 PURGE、源依赖失效 | P2.2 | 同上 |
| P2.4 | 固定定位、资源集合迁移及恢复 | P2.2—P2.3 | 同上 |
| P2.5 | next/origin 接通、请求状态和升级交接 | P2.1 | 可拆分审查 |
| P2.6 | 透明 v2 缓存灰度与回滚验收 | P2.1—P2.5 | 可以 |
| P3.1 | Accept 协商、路由规格和转换接口 | P2.6 | 转换默认关闭 |
| P3.2 | 源资源缓存、成品缓存和分层合并 | P3.1 | 与 P3.3—P3.4 一起发布 |
| P3.3 | 首个转换后端及 JPEG/PNG/WebP 契约 | P3.1—P3.2 | 同上 |
| P3.4 | 成品 304/HEAD/Range、失败回退和配置 | P3.2—P3.3 | 可以，按路由灰度 |
| P4.1 | 指标、运行说明及既有文档校正 | 相关能力进入可验证状态 | 随各阶段交付 |
| P4.2 | 旧缓存回收、索引维护和故障演练 | P2.6 | 可以 |
| P4.3 | 经过响应头验证的请求合并优化 | P2.6，图片场景另依赖 P3.4 | 可以，独立开关 |

## 3. 模块与文件规划

下列“新增”路径均为拟建文件，不表示当前已存在。最终允许按职责合并文件，但不能跨层反向依赖。

| 模块 | 修改/新增文件 | 职责 |
| --- | --- | --- |
| Vary 语义 | 修改 `pkg/x/http/varycontrol/vary.go`；新增 `snapshot.go`、`vary_test.go`、`snapshot_test.go` | 解析字段列表、快照、匹配与指纹，不接触存储 |
| 元数据模型 | 修改 `api/defined/v1/storage/object/object.go`；新增 `representation.go` 及测试 | v2 控制记录、变体引用、表示信息、深拷贝和编码兼容 |
| 存储契约 | 新增 `api/defined/v1/storage/representation.go` | 控制记录能力、写入票据、提交/失效语义；实现做接口合规断言 |
| 表示协调层 | 新增 `storage/representation/` | 索引、资源锁、预留、发布、依赖、失效、定位及恢复；不包含 HTTP 响应拼装 |
| Bucket/IndexDB | 修改 `storage/bucket/{disk,memory,empty}/`、必要的 `storage/indexdb/{pebble,nutsdb}/` | 无副作用控制记录写入、持久性、固定代际读写及底层对象复制/删除 |
| 缓存集成 | 修改 `server/middleware/caching/{caching,processor,internal,caching_vary,caching_revalidate,caching_filechanged,caching_fillrange}.go` | 分离查找、回源、存储和客户端响应处理 |
| 请求合并 | 修改 `server/middleware/caching/{object_flight,chunk_flight}.go`、`proxy/proxy.go`、必要的 `proxy/singleflight/` | 禁止不安全复用；后续增加响应头复用校验 |
| PURGE/迁移 | 修改 `storage/{storage,migrator}.go`、`storage/selector/`、`plugin/purge/` 的接入点 | 使用同一个协调层维护 v2 生命周期 |
| 内容协商 | 新增 `pkg/x/http/acceptcontrol/` | Accept 解析与候选格式排序，不了解缓存 |
| 转换契约 | 新增 `api/defined/v1/transform/` | TransformSpec、能力声明、输入/输出契约 |
| 转换执行 | 新增 `transform/`、`transform/backend/http/` | 后端注册、规格规范化、受限执行、HTTP worker 适配 |
| 转换入口 | 新增 `server/middleware/transform/`；修改 `server/server.go` 注册点 | 托管路由识别、请求计划，对外 Vary 生成 |
| 配置/文档 | 修改 `config.example.yaml`、必要的 `conf/`、`docs/README.md` 及功能说明 | 配置校验、迁移说明、能力状态 |

依赖方向：HTTP 工具与模型 → 存储协调层/转换服务 → caching/transform 中间件 → server。`storage/representation` 不引用 `server/middleware/caching`，转换器不调用公开 HTTP URL 递归进入自身。

## 4. 共用数据模型与不变量

### 4.1 选择快照

拟定义以下值对象，序列化字段采用稳定标签；HTTP 工具包中的类型可被模型使用，但不能反向导入存储：

```go
type FieldValue struct {
    Name    string   // 统一为小写的合法字段名
    Present bool     // 缺失与空字段必须区分
    Values  []string // 保留顺序和字段语法
}

type Snapshot struct {
    Version uint16
    Fields  []FieldValue // 按 Name 排序；值的顺序不自动排序
}
```

字段列表解析和请求字段值解析分开处理。`Vary: Accept, Accept-Encoding` 的逗号可以拆；`Accept` 或未知字段的内容不能交给同一个 Clean 函数拆分。

指纹采用版本化、带长度的结构编码再 SHA-256；不得使用 `a=b&c=d` 拼接。索引先按指纹定位，再比较快照，指纹不作为唯一匹配证据。

### 4.2 v2 根控制记录

在现有 Metadata 外层能力上增加可选嵌套记录，避免第一步重写 IndexDB 的 `Get/Set(*Metadata)`：

```text
Metadata
  原有响应/分块字段（保留）
  record_kind: legacy | resource | representation
  resource: ResourceRecord?             # 只有控制记录拥有
  representation: RepresentationInfo?   # 只有响应对象拥有

ResourceRecord
  schema_version / revision
  resource_id / canonical_url / namespace
  incarnation                           # 控制记录重建时更换，防 ABA
  purge_epoch / revalidate_epoch
  active_location / routing_epoch
  variants[]: VariantRef

VariantRef
  selection_kind: http | managed
  vary_fields[] / selector_snapshot / selector_fingerprint
  transform_fingerprint? / selected_source_id? / selected_source_version?
  representation_id / response_date / last_access

RepresentationInfo
  resource_id / incarnation / purge_epoch / validated_epoch
  body_generation / state: filling | complete | failed
  kind: origin | derived
  source_id? / source_version? / transform_spec?
  policy_version? / encoder_version?
```

`Metadata.Clone()` 必须深拷贝所有快照、引用数组及嵌套数据。JSON 与仓库现有 CBOR codec 都需要往返测试，旧记录缺字段能解码，未知版本按 MISS 处理。

新控制记录与原响应标志分离，不复用“根 Headers + VirtualKey 字符串”的隐式协议。控制记录不能计为可响应的对象，不能被普通块合并、Touch、访问统计或旧递归 Discard 路径修改。

### 4.3 逻辑键与物理键

```text
ResourceID       = H(命名空间, 规范 URL, 上游/凭据隔离范围)
SelectorID       = H(Vary 字段集合, Snapshot)
RepresentationID = H(ResourceID, incarnation, purge_epoch,
                     SelectorID, body_generation)
DerivedID        = H(源表示 ID, 源内容版本, 完整 TransformSpec,
                     policy_version, encoder_version)
```

所有 H 输入使用结构编码。物理记录仍使用现有 object.ID 和分块路径，但由单一 key-builder 生成 v2 虚拟后缀。保留 ID.Path() 的规范 URL 供运维识别；后缀包含固定版本/种类标记和摘要，不含原始 Cookie、Accept 或任意转换参数。构造器必须确保原 URL 与内部后缀不产生边界歧义，并通过现有 ID 编解码恢复测试。

URL 规范化保留可能影响内容的转义和查询语义，不擅自排序重复查询参数；规范 URL 不包含 fragment。转换路由要求 `include_query_in_cache_key=true`，直到路由解析器显式提取且完整编码转换参数。不同 URL 的复用仅来自明确的 source 映射。

透明模式的 ResourceID 对应公开 URL，引用按 HTTP 选择快照查找。托管模式的 ResourceID 对应该路由映射后的源 URL 和隔离范围：路由只移除其明确识别的 format/quality 等参数，并将这些参数完整写入 TransformSpec；其他源查询参数原样保留。同一源的 Origin 引用和 Managed 引用放在该资源集合内。公开 URL 仍保留在请求/访问日志中，URL PURGE 必须经过相同路由解析定位整个源资源集合。

Managed 引用按“确切源表示/版本 + 规范转换规格”查询，不能继续按原始 Accept 快照各建一份成品引用。对外 Vary 是 HTTP 协商声明，内部选择键由受控算法生成；不同 Accept 若得出相同规格只占一个成品预算。源站自身的 Vary 则始终先在 Origin 引用中匹配，源表示不同不能因目标格式相同而合并。

### 4.4 写入票据与状态

```text
FillTicket
  resource_id / incarnation
  captured_purge_epoch / captured_revalidate_epoch
  captured_prefix_barriers
  expected_source_id? / expected_source_version?
  representation_id / body_generation / fixed_location
  reservation_id / lifecycle_context

unpublished → filling → complete → retired
                    ↘ failed
```

不变量：

1. 一个写入器从开始到结束只写一个 ID、位置及 body_generation。
2. 根索引引用的元数据必须已经存在；块只有在确认写入成功后才可标记可读。
3. 新源内容不能合并旧代际的 Chunks/Parts；相同字节偏移不等于相同内容。
4. 提交检查资源 incarnation、PURGE epoch 和目录屏障，任一变化则不得发布旧结果。
5. soft PURGE 前开始的验证不能把结果标记为已满足 soft PURGE 后的新鲜度要求。
6. 允许已开始的读取完成旧响应；PURGE 成功之后开始的新查找必须看到新的失效状态。
7. 填充上下文在发起上游请求前捕获；不能等响应头到达后才读取失效 epoch，否则在途旧响应可能越过 PURGE。
8. 派生提交同时验证预期源表示/版本仍有效；普通同选择项并发提交按响应 Date 等冲突规则处理，不让迟到的旧响应覆盖更新的候选。

## 5. 阶段一：修复 Vary 正确性

### P1.1 安全止损与最小回归

- [x] 增加 `caching_vary_test.go` 和带可控响应/调用计数的 httptest 源站，配合临时磁盘或内存 bucket，无须启动常驻 Tavern。
- [x] 冷缓存、未知 Vary、Vary 子变体 MISS 不进入共享响应体的 ObjectFlight；透明代理回源同步禁用 proxy 层旧 singleflight。
- [x] 已知变体缺块的 chunk flight 同样不能仅凭旧 ID 共享新回源结果，先保守独立获取，阶段四再恢复安全合并。
- [x] 收到 `Vary: *`、非法选择字段或超过选择头预算时透传，不创建写入器，不增加根变体引用；保留源站 Vary。
- [x] 在 `ParseCacheTime`/缓存资格判断中先处理 no-store/private 和请求共享限制，再考虑 max-age 或内部 TTL。当前提前返回 max-age 的路径不能绕过禁止存储指令；请求 no-cache 不得直接新鲜命中。带 Authorization 的请求初版保守旁路，后续若开放明确允许共享的响应再独立验收。
- [x] 不立即实现任意 Vary 忽略规则。现有非空 `vary_ignore_key` 先给出一次启动弃用提示并保持严格匹配，修正文档中“已生效”的描述，示例删除该配置。
- [x] 旧 Vary 数据不再作为可靠命中来源；止损版本在重建能力上线前可直接旁路这些资源。未涉及 Vary 的普通缓存维持现有处理。

验收：同 URL 的 WebP/JPEG 请求通过 channel/barrier 同时进入源站，响应 MIME 和 Body 各自正确；`Vary: *` 连续请求均回源；`max-age=600,no-store` 不留缓存对象。此批次可以独立发布，但必须明确旧 Vary 命中暂时下降。

### P1.2 解析、快照与匹配

- [x] 用 `Parse(values ...string)` 返回字段集合、wildcard 标记和错误；大小写归一、排序去重，合法空列表等价无 Vary。
- [x] `*` 出现在列表任意位置均视为不可直接复用，不把 `*` 当请求字段查找。
- [x] `Capture(fields, header)` 保留 Present 和顺序；不修改原请求 Header。
- [x] `Match(snapshot, header)` 对未知字段采用保守相等，初版不做字段值逗号排序；合法多行组合规范化仅用于已实现且有明确语法依据的字段。
- [x] `Fingerprint(snapshot)` 使用固定版本与结构编码。透传 Accept 的指纹与本地格式协商的“输出规格”键严格区分。
- [x] 给字段数、选择快照总字节数设置有限预算；建议初始值 32 个字段、16 KiB 快照，超过预算仅旁路，不截断后继续匹配。

定向用例：字段名大小写、重复/多行 Vary、多字段组合、缺失与空字符串、有顺序值、引号内逗号、`&`/`=` 拼接碰撞、非法 token、星号和预算边界。对 Parse/Capture 的 fuzz 用例要求不 panic、不修改输入、确定性输出，不做无语义依据的归一化。

### P1.3 v2 索引查询基础

- [x] 新增上一节定义的对象模型、格式版本和独立键空间；旧 VirtualKey 仅用于旧数据识别，v2 不读写它。
- [x] 在 `storage/representation` 实现 IndexRepository 基础：Load、按资源加锁的 Update、变体预留和 Release。阶段二在同一模块补全 Store，不另建临时实现。
- [x] 根中直接记录 RepresentationID，不从请求头字符串重建物理子键。
- [x] 每个引用保留自己的字段集合。查询可按字段集合分组计算请求快照，选择候选后再检查响应新鲜度。
- [x] 多候选选择遵循有效 Vary 和响应 Date 的规则；Date 相同使用稳定次序。根控制记录的时间不能替代各响应的时间。
- [x] 查找成功后，Caching 持有独立的 resource 引用与 representation 引用；替换 c.md 不得丢掉根控制状态，也不得把响应头写入根。

过渡边界：阶段一先支持完整响应写入；206 的缓存写入、缺块补齐及未知长度响应的持久化先旁路，原请求仍正确透传。阶段二完成代际校验后恢复这些能力。阶段一允许 HEAD 命中既有表示，但 HEAD 冷 MISS 不发布“完整 Body”对象。

### P1.4 200/304 与索引更新

- [ ] 在响应体缓存包装之前确定 Vary、缓存资格、候选 ID 和预算决策；无法确定存储目标时只透传。
- [ ] 完整 200 新建响应描述，响应未带 Vary 不继承旧字段；新响应字段集合变化时只替换相关条目，不把新旧字段并集作为全部对象的查询规则。
- [ ] 304 先确定可更新的所选响应，合并允许更新的元数据；缺失字段不当作删除。304 改变 Vary 时使用本次实际验证请求重新生成选择快照，并原子替换索引引用。
- [ ] 304 没有可匹配的缓存 Body 或验证器不适用时，不制造空的 200；对缓存自身发起的验证进行一次无条件获取，设置重试上限，客户端自身的条件请求单独处理。
- [ ] 响应 Date/Age 作为响应元数据保留；至少保证该次更新不会把源已有 Age 清零并获得完整新 TTL。时钟与新鲜度计算使用可注入测试时间。
- [ ] 资源锁内重新读取最新根，合并新增、替换及删除引用；不能用请求开始时的整个根覆盖。
- [ ] `vary_limit` 同时计入 ready 与 pending 的不同选择项，重复更新已有选择不增加预算；初版超限固定透传，不做隐式全 URL 删除。
- [ ] 超限、解析错误及旁路均明确返回“不写缓存”的决策，不能只记录错误后沿用当前 c.id。

### P1.5 阶段验收门槛

- [ ] 针对两个字段的笛卡尔积验证每个表示都能二次命中且无跨组合复用。
- [ ] 按顺序测试无 Vary → Accept → Accept + Accept-Encoding → 移除 Vary，以及不同表示使用不同字段集合。
- [ ] 验证已有缓存重新回源的 200/304，不能因为 IsVaryCache 提前返回而跳过索引更新。
- [ ] 带逗号 Accept 的两个变体清理时定位真实子 ID，不出现拆碎的虚假键。
- [ ] 并发新增多个变体后，根保留全部已提交引用；超限只影响新组合。
- [ ] 未开启转换功能，不引入新编解码依赖。新的 v2 缓存继续保持受控测试状态，等待阶段二。

## 6. 阶段二：表示存储与生命周期

### P2.1 重排缓存流水线和固定写入器

将 `doProxy` 拆为职责明确的过程，保留现有 Processor 适配，逐步移除对阶段顺序的隐式依赖：

```text
PrepareUpstreamRequest
  → BeginFill / CaptureInvalidationContext
  → ExecuteUpstream
  → AnalyzeOriginResponse / MergeValidation
  → DecideAdmission / ResolveRepresentation / ReserveSelector
  → CreateFixedWriter / ConsumeBody
  → PublishReadableMetadata / CommitReference
  → ApplyClientConditionsAndRange / DecorateResponse
```

- [ ] 区分源完整响应头、持久化表示头和客户端裁剪后的响应头，避免把一次 Range 的 Content-Length 写为整个资源大小。
- [ ] `flushbufferSlice` 接收固定 WriteSession，不再在后台回调中读取可变 c.id/c.md。
- [ ] 替换来源已变化的 200 使用新的 body_generation。缺块写入若要沿用旧代际，必须验证同一表示的强验证器及 Range；不具备可靠版本证据则重新获取完整对象。
- [ ] 收到与旧表示不兼容的 200/206 不把字节补进旧文件；终止该补齐会话，以新代际重建或安全透传。
- [ ] 恢复 206/未知长度缓存：显式 filling 状态、确认块位图、完整大小和 EOF 转换。中途失败不能 HasComplete，后续只读取已确认且属于同一代际的块。
- [ ] 非正常关闭和写入失败释放预算、关闭文件/Body、撤销不可用引用；失败的请求不得删除另一个代际已发布的成品。
- [ ] 后台刷新 clone 请求和元数据，使用独立生命周期上下文，不在前台请求仍读缓存时修改它的对象。

### P2.2 存储提交、失效屏障和持久性

拟定义以下语义接口；名称可按项目习惯微调，实现必须遵守注释所述行为：

```text
RepresentationStore
  Lookup(resource, requestSelection) → selected representation
  BeginFill(resource) → FillTicket      # 在网络请求前捕获失效身份
  ReserveSelector(ticket, selector)     # 响应头可用后预留选择项预算
  OpenWriter(ticket) → fixed WriteSession
  Commit(ticket, representation) → published | superseded
  Abort(ticket)
  Invalidate(resourceOrPrefix, softOrHard)
  MigrateResource(resource, target)
  CollectUnreachable(budget)
```

控制记录需要专用 Bucket 能力 `LoadControl/StoreControl`，不能借用会合并旧块、增加域名计数或进入普通 LRU 的 Store。优先采用新增可选能力接口，disk/memory 明确实现；empty 或缺失能力返回“不支持缓存”，不使用不安全的兼容降级。

提交协议：

1. BeginFill 在网络请求前获取 incarnation、失效 epoch、前缀屏障和位置；随后释放锁。响应头确定选择项后，ReserveSelector 使用原票据预留预算并复查屏障；不能换成响应到达时的新 epoch。网络和转换期间不持有资源锁。
2. 写入固定的新代际数据；表示元数据只声明真实已可读的数据。
3. Commit 再获取资源锁，读取最新控制记录，验证票据、预期源版本和屏障；PURGE/迁移/重建/源变化已取代该票据时返回 superseded 并回收此次写入。同选择项冲突使用候选 Date 等规则，不能仅按完成先后覆盖。
4. 先存可读子对象，再更新根引用；根记录更新是逻辑发布点。并发更新在此处合并。
5. 提交后释放预留。崩溃留下的无引用对象按 GC 预算回收，不通过文件名猜测并自动挂回根。

持久性必须单独处理：当前 Pebble 可配置 NoSync，NutsDB 初始化使用 `SyncEnable=false`。因此“Set 返回成功”不能直接作为断电恢复保证。v2 控制记录开放前需落实同步写入路径：Pebble 控制写使用同步选项；NutsDB 使用经验证的同步配置/路径。若某后端不能提供控制记录持久性，则启动校验禁止其启用持久化 v2 缓存，不假装支持。

控制记录与子对象跨记录不强求同一事务：允许子先落盘、根后发布，恢复时验证引用和文件存在性；缺失子对象安全 MISS。失效屏障和成功确认的 PURGE 则必须经过持久化后再返回成功。

### P2.3 PURGE 与依赖失效

所有 v2 入口统一经过 RepresentationStore；既有 `Storage.PURGE` 保留对外接口，内部按键版本路由。PURGE 指定 URL 应覆盖该 URL 对应的全部缓存格式版本和 v2 命名空间，不能只删除当前读路径。

| 操作 | 先完成的逻辑动作 | 后续动作 |
| --- | --- | --- |
| 单资源 soft | 持久化 revalidate_epoch 增长 | 所有旧 validated_epoch 不能新鲜命中，按需验证 |
| 单资源 hard | 持久化 purge_epoch 增长并切断旧引用 | 异步删除旧子对象/块，迟到 Commit 被拒绝 |
| 目录 soft | 持久化主机/路径前缀验证屏障 | 扫描当前根更新/标记，未注册完成的旧填充也不能绕过 |
| 目录 hard | 持久化主机/路径前缀删除屏障 | 清理该目录所有当前根及对象，旧填充不能复活 |
| 删除派生成品 | 删除该规格引用或更新其代际 | 源表示和其他规格保留 |
| 源内容改变 | 更换 source_version/源代际 | 旧依赖逻辑失效，物理成品延迟回收 |

- [ ] 冷资源也有 BeginFill 和失效身份；hard PURGE 不能仅在 Lookup 命中时建立屏障。
- [ ] 目录 PURGE 必须先建立持久化前缀屏障，再扫描根；仅扫描已存在元数据不能覆盖未提交的冷缓存请求。
- [ ] 前缀比较以规范主机、路径边界和目录语义进行，不把内部成品后缀当公开 URL；明确 `/img/` 与 `/image/` 不互相覆盖。
- [ ] 第一版屏障记录不由普通 LRU/TTL 自动淘汰；阶段四再做保持语义的压缩。预算不足时显式报告操作失败，不能确认未落实的 PURGE。
- [ ] 源/成品的依赖校验至少包含 source_id、source_version、源失效状态；单靠成品 ExpiresAt 不够。
- [ ] 更新 `plugin/purge` 的结果语义与错误映射，保留既有授权规则和请求头接口；不增加未经定义的公网删除 API。

### P2.4 资源定位、迁移和恢复

细化前置设计中的“集合迁移”：控制目录和数据集合不采用相同的自动提升策略。

- ResourceRecord 固定在 home bucket 的受保护控制记录区域，记录整个资源集合的 active_location。
- 源/派生表示初版一起位于 active_location，整体迁移；控制记录自身不参与普通对象热度迁移。
- home 由稳定 ResourceID 和配置拓扑确定，不能因 UseAllow、磁盘水位或瞬时健康变化跳到另一个 home。home 不可用时旁路且不另建冲突的控制记录。
- 现有 selector 的动态候选选择只能用于新数据位置，不能承担控制记录所有权；新增稳定 owner 解析。
- bucket 成员变更需要明确的控制记录迁移/重建流程。首版不自动支持在线重分片；配置变化必须校验旧拓扑，禁止静默丢失失效记录。

集合迁移协议：

1. 在资源锁内捕获 revision 和引用快照，标记迁移预留；后续 Commit 暂不发布到旧位置，可返回 superseded/retry。
2. 复制全部可达表示及确认可读的块到目标位置，校验元数据和长度。
3. 再次持锁验证 revision、epoch 和目标完整性，通过控制记录原子切换 active_location/routing_epoch。
4. 新查询转向目标；旧读者通过读取租约继续完成。待读者释放后删除旧位置的对象。
5. 失败时不切换目录，目标半成品进入回收；不能调用“删除根并级联删除子对象”的旧迁移实现。

根淘汰必须先退役引用再删除数据，失效状态保留；表示淘汰先移除引用再删除块。重启只恢复已发布且满足代际/位置约束的引用，过期的 pending 预算不恢复为永久占用。

### P2.5 请求执行接口与进程交接

- [ ] caching 使用注入的 next/origin RoundTripper 执行回源，不再在 doProxy 内直接绕过中间件链调用全局 proxy。
- [ ] server 构造底层代理适配器，统一处理 upstream 选择、指标和默认不安全合并禁用。普通 GET/HEAD 和非缓存请求都通过同一注入入口。
- [ ] `cloneRequest` 保留调用方 Context/cancel/deadline，选择 peer 时基于原 Context 派生；请求和响应头 rewrite 与表示匹配边界明确。
- [ ] 将处理请求的网络 Context 与已接受缓存填充/转换任务的受限生命周期分开；客户端取消释放其读取，不让永不结束的后台任务持有预留。
- [ ] 对 `main.go` SIGUSR2 当前“先 Close storage 再 Upgrade”路径做交接检查：停止新填充、等待/取消旧写入会话、关闭旧存储，之后才允许新写入者使用同一库；旧进程退出期间只服务可安全完成的读取或旁路。
- [ ] 一份存储目录只允许一个写入所有者。若无法证明现有交接保证单写，则实现所有权锁/停写屏障；不能用进程内资源锁承诺跨进程原子性。

### P2.6 阶段验收门槛

- [ ] 对每个提交步骤注入失败，重启后无悬空可命中引用；未完成块不被当完整表示读取。
- [ ] 填充/304/后台刷新与 soft/hard PURGE 交错，旧票据不能把资源恢复为新鲜状态。
- [ ] 目录 PURGE 覆盖尚未提交的冷资源；多个字段组合均失效。
- [ ] 磁盘和内存后端完成行为契约测试，持久化后端完成重启恢复及同步写配置测试。
- [ ] 迁移失败保持源可用；成功后目标包含全部表示；旧读者释放前不删除其块。
- [ ] 接通 next 后用一个记录调用的 RoundTripper 证明回源经过它，非缓存方法也不绕过。
- [ ] SIGUSR2 活跃填充交接不出现向已关闭 DB 写入，不出现两个写入者共同更新根。

完成后可开放透明 v2 缓存。保守禁用的共享回源不作为“缺少正确性”的阻塞项，但必须记录对源站负载的影响；优化放在 P4.3。

## 7. 阶段三：图片转换与复用

### P3.1 协商与转换契约

入口采用 transform 计划中间件，在 caching 之前识别托管路由并生成类型化计划；caching 读取计划后调用表示提供者。不要仅把转换器放在 caching 的 next 内部，否则成品命中身份和源缓存依赖无法完整表达。

```text
外部请求 → rewrite → transform-plan → multirange/caching
                                      ├─ 透明源站提供者 → next → upstream
                                      └─ 托管图片提供者
                                           → 独立源表示缓存 → upstream
                                           → 转换执行器 → 表示存储
```

中间件确切顺序在配置校验中固定；多 Range 拆分前必须已经确定目标表示，不能对源图偏移做转换。

拟定义：

```text
TransformSpec
  schema_version
  output_mime
  quality                    # 按编码器契约，默认值显式展开
  lossless / compression_level
  alpha_policy / background
  animation_policy / orientation_policy / color_policy

BackendCapabilities
  input_formats / output_formats
  policy_version / encoder_version

Transformer
  Capabilities()
  Transform(ctx, completeSource, normalizedSpec) → completeOutput
```

- [ ] 实现 Accept 的 q、具体媒体范围、通配符和支持集合；q=0 的具体排除不能被宽泛范围覆盖。
- [ ] 缺失 Accept、`*/*` 默认保留原格式；等权按稳定配置优先级选择，避免依靠请求字符串顺序产生意外输出。
- [ ] 显式 format 与自动协商分开：显式值进入 TransformSpec，同时确认输出符合客户端可接受范围；无法满足返回 406，未知 format 为参数错误。
- [ ] 初版只开放格式转换，不同时引入任意尺寸/裁剪接口；质量只接受固定配置或有界参数。后续尺寸策略扩展必须换规格版本。
- [ ] JPEG/WebP 的质量范围由后端契约校验；PNG 使用明确的无损/压缩级别，不把同一个 quality 数字直接解释成 PNG 压缩级别。规范化规格移除不适用的默认项；显式不适用参数返回错误。
- [ ] PNG 转 JPEG 默认对透明输入拒绝有损透明度处理，只有显式配置 flatten/background 才允许；动画源默认保留原图或明确拒绝转换，不默默抽取首帧。
- [ ] 策略和能力变化使新 DerivedID 与旧结果隔离；请求中的伪内部选择头被忽略，不作为受信任计划。

### P3.2 源资源与派生成品路径

- [ ] source 由明确路由映射得到；初版采用同源 URL 的格式参数映射，移除的参数必须全部进入规范规格，其他 query 保留。带重复/冲突的转换参数先报错，不以不同解析顺序生成不同源。其他别名须显式配置，禁止仅按扩展名推断等价。
- [ ] 源请求使用固定且与后端解码能力匹配的 Accept，优先请求 identity 内容编码。实际源响应仍遵循其 Vary，不假设源站必然照单返回。
- [ ] 若源站返回压缩内容，先在有界解压路径确认编码，转换输入为完整图片字节；源缓存保留自己的响应语义和验证器。
- [ ] 先解析新鲜源元数据以确定 source_version 和规格可行性；成品 HIT 不读取源 Body，不解码、不调用 worker。
- [ ] 源过期先重验证：304 延续源内容版本，更新依赖有效期；200 内容变化则新版本。无可靠版本证明时宁可换代重建。
- [ ] 源获取按实际等价源请求合并，源图完成后不同目标任务读取同一份源文件/数据；相同 DerivedID 使用独立转换 flight，确保只转换一次。
- [ ] 后端转换期间不持资源锁，成品完整且验收通过后再 Commit；PURGE 或源切换使旧转换提交失效。
- [ ] 源缓存资格与成品资格都满足共享策略；不能把 private/no-store 的源图转为 public 成品。

### P3.3 首个后端：HTTP 转换 worker 适配

项目保持 `CGO_ENABLED=0`。首个生产适配选用 HTTP worker 契约，实际 worker 地址由部署配置提供；不在此次计划中假定已有可用服务，也不承诺某个尚未验证的纯 Go WebP 编码库。

- [ ] 定义后端版本化契约：`POST /v1/transform`，Body 为完整源图片字节，Content-Type 为已确认输入 MIME；`X-Tavern-Transform-Spec` 为规范 JSON 规格的 base64url 编码，`X-Tavern-Transform-Version: 1` 标识协议。以上头只由适配器生成，不透传客户端同名头；不让 worker 任意抓取客户端给出的 URL。
- [ ] worker 用 200 返回成品字节、实际 Content-Type 和 `X-Tavern-Encoder-Version`；适配器校验输出类型与规格一致、版本匹配配置，验证可解码性/像素约束，并拒绝错误页或不完整输出。非 200 为转换错误，不作为图片缓存。
- [ ] worker 声明的 JPEG/PNG/WebP 输入输出矩阵通过真实图片契约测试后，配置才允许开启对应组合。
- [ ] 输出先写临时文件并计算摘要/长度，完成后发布到表示存储；失败或取消清理临时文件，释放源读取和转换预留。
- [ ] 设置输入字节、解码像素、输出字节、并发、排队和超时预算；默认建议作为初始配置值，需在代表性图片上校准，不能直接当性能保证。
- [ ] 用测试后端做稳定的路由/缓存集成测试；真实 worker 用独立显式契约检查，不让全部 Go 单元测试依赖外部服务。

后续若加入本地 JPEG/PNG 或纯 Go WebP 后端，复用同一接口和成品键，独立审核依赖、编码能力、性能和静态构建；后端变化要体现在 encoder_version。

### P3.4 HTTP 响应、失败语义和配置

| 情况 | 确定行为 |
| --- | --- |
| 成品 HIT | 返回该成品 MIME、长度、验证器和 Body；不执行转换 |
| 成品 ETag 匹配 | 针对所选成品返回无 Body 的 304，不转发源图 ETag |
| HEAD HIT | 返回同表示的头，无 Body |
| HEAD 冷请求 | 初版允许内部 GET/转换后仅返回头；该行为受同一任务预算限制 |
| Range HIT | 对最终编码字节计算 206/416 和 If-Range |
| Range 冷请求 | 内部先获取完整源图和完整成品，再截取目标字节；不转发成品 Range 为源 Range |
| Accept 不接受任何可用输出 | 406，响应不作为正常图片成品缓存 |
| 转换失败 | 默认错误响应；只有配置允许且原格式被客户端接受才回退源图 |
| 回退源图 | 写入其真实原样表示身份，不写入目标 WebP/PNG/JPEG 键 |
| 请求 no-transform | 走原样表示路径；与显式目标格式冲突且不能满足时返回 406 |
| 响应 no-transform | 禁止转换该源，符合请求时返回原样表示 |

补充约束：

- [ ] 图片 MIME 变化使用独立表示头；更新 http.Response.ContentLength，移除源 Content-Range，重新生成成品强 ETag，清理不再成立的摘要/编码字段。
- [ ] 自动协商响应带 `Vary: Accept`，与已有相关选择字段合并。若请求 Cache-Control 中 no-transform 影响返回原图/成品，则该托管路由统一增加 `Vary: Cache-Control`；所有分支一致，避免仅给旁路响应加头而留下下游旧命中。
- [ ] gzip/br 若由另外的受控层处理，其版本维度仍包含 Accept-Encoding；已经压缩的表示不能仅更换 MIME 就当成图片转换结果。
- [ ] 源新鲜度决定依赖是否有效；延长源有效期后可继续用相同成品，不因每次请求重设成品 TTL。源验证器与成品验证器分别维护。
- [ ] ETag 作为不透明大小写敏感值比较，修复既有 FileChangedProcessor 对 ETag 转小写/EqualFold 的相关路径。
- [ ] 冷 HEAD、Range 和普通 GET 选择同一 DerivedID，不因方法/范围重复转换；对外响应拼装在共享转换结束后分别执行。

拟议配置（以下字段均为新方案，不是当前可用配置）：

```yaml
server:
  middleware:
    - name: transform
      options:
        enabled: false
        routes:
          - path_prefix: /images/
            mode: auto
            formats: [image/webp, image/jpeg, image/png]
            default_format: source
            quality: 82
            alpha_policy: reject
            animation_policy: preserve_source
            fallback_to_source: false
            policy_version: images-v1
        backend:
          name: http
          endpoint: http://127.0.0.1:9000/v1/transform
          encoder_version: worker-v1
        limits:
          max_input_bytes: 33554432
          max_output_bytes: 67108864
          max_pixels: 40000000
          concurrency: 2
          queue_size: 32
          timeout: 5s
    - name: multirange
    - name: caching
      options:
        include_query_in_cache_key: true
        vary_limit: 100
        representation:
          enabled: false
          namespace: http-v2
          max_vary_fields: 32
          max_selector_bytes: 16384
```

启动校验：转换开启要求 representation 开启、后端契约可用、所选格式有能力声明、关键版本非空、预算为正数、路由参数完整进入键。示例默认关闭；不自动把仓库现有 config.yaml 切换为未部署的 worker。

### 阶段三验收门槛

使用小尺寸但真实可解码、内容可区分的 JPEG、PNG、带透明度 PNG 和可识别动画样本。对每一输出至少验证 MIME、文件格式特征和解码后的尺寸/像素语义，不能只检查 Content-Type。

- [ ] JPEG→WebP、JPEG→PNG、PNG→JPEG 正常转换；透明度和动画策略分别测试。
- [ ] 同源 20 个相同规格并发请求：源请求一次，转换一次，全部响应字节一致。
- [ ] 同源两个目标格式并发：源请求一次，每个 DerivedID 转换一次。
- [ ] 不同原始 Accept 最终规格相同复用成品；q/参数改变最终规格时隔离。
- [ ] 内容版本、quality、背景色、policy_version 或 encoder_version 变化均不会误命中旧成品。
- [ ] 源图 304 不重复转换；源内容变化、soft/hard/目录 PURGE 后不新鲜命中旧依赖。
- [ ] 成品 304/HEAD/Range/If-Range 与普通 GET 一致，冷 Range 不把目标偏移传给源图。
- [ ] no-transform 与回退路径不会污染目标键或下游协商缓存。
- [ ] worker 超时、错误 MIME、超预算、断流和取消均不发布成品，不泄露任务预留。

## 8. 阶段四：观测、维护和优化

### P4.1 指标和文档

基础错误计数随前面阶段同步加入，第四阶段完成面板和运行手册。

| 指标（拟议） | 有界标签 | 用途 |
| --- | --- | --- |
| `vary_lookup_total` | hit/miss/bypass、reason | 观察字段匹配、星号和预算旁路 |
| `representation_commit_total` | published/superseded/failed | 观察提交冲突及迟到写入 |
| `representation_pending` | origin/derived | 检查预留泄漏 |
| `representation_invalidate_total` | soft/hard、file/dir | 检查 PURGE 使用与错误 |
| `source_fetch_total` | hit/revalidate/miss | 验证源资源复用 |
| `transform_total` | source_mime、target_mime、result | 验证转换复用与失败 |
| `transform_duration_seconds` | backend、result | 耗时分布 |
| `transform_queue_depth` | backend | 排队/预算 |
| `representation_gc_total` | collected/skipped/failed | 回收与故障 |

不以任意 URL、Cookie、Accept 原文、动态版本字符串或完整规格作为标签；日志记录稳定 ID、选择字段名、旁路原因和有限规格摘要，调试也不直接打印敏感选择值。

更新 README 中 Vary 的实际能力状态、弃用 vary_ignore_key 的说明、升级后的冷缓存影响、worker 契约及 JPEG/PNG/WebP 支持矩阵。只有真实后端通过契约验收的组合才标为支持。

### P4.2 回收、升级与故障演练

- [ ] v1 与 v2 物理键空间隔离，v2 不回退读取旧 Vary 数据；旧文件按限速任务回收。
- [ ] 先撤销引用再回收数据；只在确认没有读取租约、填充票据、迁移快照引用时删除对象。
- [ ] 失效墓碑/目录屏障压缩必须保留所有可达记录与在途票据的比较语义；不能以“过了几分钟”作为唯一删除条件。
- [ ] 验证 worker 不可用、磁盘满、home bucket 不可用、控制写失败、重启和升级中断；缓存失败以透传/明确转换错误退出，不放宽格式要求。
- [ ] 定向基准记录源请求数、转换数、成品命中率、控制记录 I/O 与内存，不预设性能倍数。固定样本和环境后才给出发布阈值。

### P4.3 安全恢复请求合并

透明模式 flight 等待者保存自己的实际请求快照。leader 响应头到达后执行 `CanReuseResponse(leaderRequest, followerRequest, response)`：

1. 确认响应允许共享，`Vary: *` 或无法理解的复用条件直接拒绝。
2. 按本次响应 Vary 而非旧索引比较请求，检查 Method、Range、条件头及身份隔离范围。
3. 只给匹配者注册 Body 接收者；不匹配者独立回源，不先发 leader 的头再改主意。
4. 每个等待者独立拼装 Age、缓存状态和客户端条件响应；响应 Header/Body 状态不共享可变对象。
5. 对缺块共享，额外确认 body_generation/强验证器一致；Range 联合优化独立验证。

第一版可只恢复完全等价且通过响应头验证的合并。优化开启前后运行同一套串响应回归，再评估源负载收益，不能以减少调用数替代正确性验收。

## 9. 定向验证清单

本节命令是实施时的计划，不表示本轮执行过。测试名为拟定名称，建测试时以表中的行为契约为准。

| 批次/模块 | 优先验证命令 |
| --- | --- |
| P1.2 Vary 工具 | `go test ./pkg/x/http/varycontrol -run 'Test(ParseVary|Snapshot|VaryMatch|Fingerprint)' -count=1` |
| P1.3 索引、预留和控制记录 | `go test -race ./storage/representation -count=1` |
| P1.3 元数据/键 | `go test ./api/defined/v1/storage/object -run 'Test(Representation|ResourceRecord|StructuredKey)' -count=1` |
| P1.1/P1.4 缓存行为 | `go test ./server/middleware/caching -run 'TestVary|TestCacheAdmission' -count=1` |
| 请求合并改动 | `go test -race ./server/middleware/caching ./proxy ./proxy/singleflight -run 'Test.*(Flight|Vary|Collapse)' -count=1` |
| P2 协调层 | `go test -race ./storage/representation -run 'Test.*(Commit|Purge|Reservation|Generation|Recovery|Migration)' -count=1` |
| P2 受影响存储契约 | `go test ./storage/bucket/disk ./storage/bucket/memory ./storage/bucket/empty ./storage/indexdb/pebble ./storage/indexdb/nutsdb` |
| P2 PURGE 接入 | `go test ./storage ./plugin/purge -run 'Test.*(Purge|Representation)' -count=1` |
| P3 Accept | `go test ./pkg/x/http/acceptcontrol -count=1` |
| P3 转换服务 | `go test -race ./transform/... -run 'Test.*(Transform|Source|Derived|Flight|Backend)' -count=1` |
| P3 HTTP 集成 | `go test ./server/middleware/transform ./server/middleware/caching -run 'TestImage|TestDerived|TestTransform' -count=1` |

执行规则：

- 不为文档修改运行 Go 测试；实现后只运行该批次影响的命令。
- 并发相关批次使用相应 package 的 race，其余不默认加 race。
- 变更公共存储接口时，额外编译/测试所有实际实现者和直接调用者；先用引用搜索列出范围，不机械扩大到全仓库。
- 测试使用临时目录、可控时间和 channel/barrier 同步，避免固定 sleep 导致偶发通过；同源并发复用测试使用可阻塞源站确保请求确实重叠。
- 存储“故障后恢复”区分正常 Close、进程异常退出及持久化屏障验证，不用正常 Close 的测试宣称断电安全。
- 现有 CI 要求 `make build`、启动测试 Tavern、`go test -count=1 -v ./...`：实现提交进入 CI 时按原工作流执行，不能将其表述为本地每个批次的默认验证；不得在本次文档任务执行这些命令。
- 已通过检查不重复运行，除非对应源码、依赖或运行配置随后改变。

## 10. 灰度、回滚和完成定义

### 10.1 发布顺序

1. 发布 P1.1 止损补丁，监控源负载和旁路原因。
2. P1.2—P2.6 合并期间保持 v2 开关关闭，通过测试配置验证新格式与生命周期。
3. 对小范围流量开启透明 v2 命名空间，确认旧数据不混读、PURGE 覆盖全部版本。
4. 图片能力先部署并验收 worker，再开启单一路由；先自动协商及固定质量，后开放经校验的显式参数。
5. 阶段四的合并优化独立灰度，异常时先关闭优化，保持正确的表示存储路径。

开关最终粒度由当前配置系统实现能力决定；若没有在线按主机/路径灰度，则使用独立实例/配置部署，不能假设存在热更新或流量分组能力。

### 10.2 回滚

- 关闭转换仅停止新转换，普通透明代理继续运行；显式转换路由按契约返回错误或允许的源图回退。
- 关闭 v2 后不读取 v2 数据，也不重新启用已知不安全的旧 Vary 命中；旧 Vary 维持旁路，普通旧缓存是否可用单独判断。
- 回滚开关与回滚二进制分开：早于 P1.1 的二进制不理解新命名空间及失效记录，不允许直接复用正在运行的 v2 缓存目录。需要独立目录或完成停写、清理和失效迁移后回滚。
- 控制格式不做原地降级重写，不删除可用于恢复的旧命名空间；PURGE 屏障不能因为关闭某个缓存版本而丢失。

### 10.3 最终完成定义

- [ ] 阶段一和二的透明 Vary 行为、并发、失效、重启、迁移验收全部通过。
- [ ] JPEG→WebP、JPEG→PNG、PNG→JPEG 的真实后端契约通过，质量/透明度/动画策略有确定行为。
- [ ] 同源同规格复用成品，同源不同规格复用源资源，实测调用计数符合预期。
- [ ] 每种表示的 304、HEAD、Range、验证器和内容编码正确，不发生跨表示字节混合。
- [ ] 文件/目录 PURGE 成功后，新请求不能命中新鲜的旧表示；在途旧写入不能复活缓存。
- [ ] 配置、支持矩阵、弃用说明、指标及回滚流程与实现一致，必要 CI 通过。

## 11. 实施时需要核实的外部条件

这些事项不阻碍设计落盘，进入对应批次时再验证，不把未知条件假设为已满足：

| 条件 | 验证批次 | 无法满足时的明确处理 |
| --- | --- | --- |
| 持久化后端的控制写同步能力 | P2.2 | 禁止该后端开启持久化 v2；不降低 PURGE 保证 |
| 单写存储与 SIGUSR2 交接 | P2.5 | 补所有权锁/停写协议后才能灰度 |
| JPEG/PNG/WebP worker 的部署地址及编码能力 | P3.3 | 保持转换关闭；透明 Vary 缓存可以独立交付 |
| 代表性图片与性能目标 | P3.3/P4.2 | 先做有界功能验收，性能阈值根据实测确定 |
| 是否需要跨不同公开 URL 的源图别名 | P3.2 | 初版只支持明确路由映射，不自动合并 URL |

标准语义沿用前置文档引用的 RFC 9110/9111；本文中的接口、预算默认值、HTTP worker、home bucket 及批次拆分是本项目的实施决策，不代表当前实现或协议规定。

## 12. 实施记录

### 2026-10-29：P1.1、P1.2

- 禁用缓存入口的 ObjectFlight、ChunkFlight 和旧 proxy singleflight 共享；并发相同 URL 的不同 Accept 请求独立回源，保留各自 MIME 和字节。
- 在 body 包装前决定缓存资格：所有非空 Vary（含星号、非法列表和超预算）暂时透传；旧 Vary 索引与子表示旁路。普通无 Vary 缓存仍可二次命中。
- no-store/private/no-cache 优先于标准和内部 TTL；请求 Authorization、no-store/no-cache、max-age=0 保守旁路。旧版本误存的禁止共享对象也不再命中。
- 200/304 引入 Vary 时不写旧普通键；缓存自身验证得到不可复用的 304 时，撤销旧对象并只重试一次。Range 旁路保留裁剪语义并同步 ContentLength；补齐响应若改变共享资格则中止补齐，避免拼入旧表示。
- Vary 工具提供 Parse/Capture/Match/Fingerprint；字段值保持原顺序和语法，缺失与空值区分；默认 32 字段、16 KiB 快照；使用版本化长度编码和 SHA-256。
- 非空 vary_ignore_key 初始化时警告一次且不生效，已从示例中移除；功能文档和中英文 README 已反映当前旁路与合并暂停状态。
- race 回归发现默认上游随机选择器使用非并发安全的 rand.Rand；代理只在选节点及更新节点时加锁，不串行网络请求。

验证结果：

- Vary 解析、快照、匹配、指纹与预算定向测试通过。
- Cache-Control 禁用指令优先级和带空格/引号的 max-age 定向测试通过。
- 使用 Go 1.26.0，缓存 Vary/资格、200/304、Range 定向 race 测试通过；受影响代理和 singleflight 包编译通过，既有 Flight/Collapse 回归通过。
- 解析器、快照各运行 3 秒 fuzz，分别检查约 15.6 万和 15.2 万次输入，无失败。
- git diff --check 通过。未运行全仓构建、全仓测试或外部 worker 契约测试。

环境说明：go.mod 声明 Go 1.26，本机默认 Go 1.27 被现有 cockroachdb/swiss 依赖的构建约束排除。缓存测试通过 GOTOOLCHAIN=go1.26.0 执行；未为此修改依赖或 go.mod。

P1.1/P1.2 交付时尚待实现的 v2 模型、索引与预留，已由下面的 P1.3 批次接续；P1.4/P1.5 及 P2 生命周期验收完成前继续旁路外部 Vary 流量。


### 2026-10-29：P1.3

- Metadata 增加 resource/representation 记录类型及统一 v2 嵌套模型，保留 legacy 解码。深拷贝覆盖 ID、快照、引用、源 ID、转换规格和位图，保留 nil 状态；未知记录类型/版本和 filling/failed 状态不能被当作完整响应。
- ResourceKey 对 namespace、规范 URL 和隔离范围进行长度编码后计算 SHA-256。规范化去除 fragment，保留转义路径、重复查询参数顺序和空查询分隔符。物理 ID 后缀只包含固定版本、种类和摘要，并对 URL/后缀采用独立的边界编码；JSON/CBOR 解码恢复相同物理 ID。
- IndexRepository 提供 Load、资源锁内重新读取并 Update、Reserve 和幂等 Release。不同 ready/pending 选择项共用预算，同一选择项多个写入者各持租约但只占一个选择预算；控制写失败、取消和未知版本不确认发布，也不覆盖旧失效状态。
- HTTP 变体引用记录自己的字段集合、完整快照和实际表示 ID；按字段集合分组捕获请求，指纹筛选后核对完整快照。候选按各响应 Date 排序，同 Date 以 ID 稳定排序，选定后再判断过期及 validated_epoch；不以较老的新鲜表示掩盖较新的过期表示。
- 根发布前要求子元数据已经存在，且资源、incarnation、purge_epoch、body_generation、完整块位图及 Vary 相符。此批只接受完整、长度已知的 200 描述；部分响应、未知长度、错误块偏移和旧 chunked 标志均拒绝。
- disk/memory 提前加入本批所需的可选 LoadControl/StoreControl 基础能力；控制写不进入 LRU、计数或普通目录索引。普通 Store、Discard、Remove、迁移及块读写保护控制记录，磁盘加载 LRU 时跳过它们；普通响应 Store 不为 v2 合并旧位图。empty 不声明控制能力。
- caching 通过内部注入接入受控只读查询，分别保存 resourceMD、variantRef 和响应 md。命中可读取 GET/HEAD/Range，保留源 Date/Age；选择项 MISS、过期、冷 HEAD 和条件请求先透传，不进入旧写入器。已选表示缺块时回退原请求，不把新字节补进旧代际。

验证结果（Go 1.26.0）：

- v2 元数据、托管/派生字段的 JSON/CBOR 往返、旧格式解码、深拷贝及结构化键测试通过。
- IndexRepository 的字段笛卡尔积、字段集合变化、Date 稳定选择、并发追加、ready/pending 预算、取消/写失败、未知版本和损坏/缺失子对象测试通过，包含 race。
- 磁盘和内存控制记录保护契约通过；caching 受控读取及旧 Vary/缓存资格回归通过，包含 race。
- 受影响 bucket、Pebble/NutsDB 和目录包装调用者编译通过；原有磁盘读写、内存读写、提升/降级定向回归通过。

发布边界：没有新增可从 YAML/JSON 打开的 v2 开关；外部 Vary 流量仍采用 P1.1 旁路。当前 ControlStore 基础能力不承诺同步落盘或跨进程原子性，预留也不是回源前捕获的 FillTicket。P2 仍须完成持久化屏障、固定写入、失效、迁移、恢复和单写交接，之后才能开放透明 v2 缓存。

下一批为 P1.4：完整 200/304 更新、缓存资格决策、索引替换与提交时的预算处理。
