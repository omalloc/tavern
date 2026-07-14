# Tavern Vary Variant 能力优化与图像转换扩展设计

> 状态：设计稿
>
> 日期：2026-07-15
>
> 范围：`server/middleware/caching`、`pkg/x/http/varycontrol`、缓存对象元数据与相关测试
>
> 本文只描述设计与实施顺序，不包含实现变更。

## 1. 背景与目标

Tavern 当前已经使用“根索引 + Vary 子对象”的两级结构保存同一 URL 的多个响应版本，但 variant 的身份、匹配、并发写入和生命周期仍有若干正确性缺口。与此同时，缓存层后续需要支持图像转换：例如客户端支持 WebP 时，将源图转换为 `image=webp;q=0.7` 并作为独立版本保存。

本设计有两个目标：

1. 按 HTTP 缓存语义正确选择、保存、更新、清理 `Vary` variant。
2. 将 variant 从“只由源站 `Vary` 请求头决定”扩展为“源站选择维度 + Tavern 内部表示转换维度”，为图像转换预留稳定接口和存储格式。

非目标：

- 本阶段不实现具体的 WebP/AVIF 编解码器。
- 不改变 Tavern 目前只缓存 GET/HEAD 的总体策略。
- 不在本次设计中替换所有对象 ID 的 SHA-1 实现；variant 自身使用抗歧义的版本化摘要即可。
- 不引入分布式锁；当前存储为单进程所有，本设计保证进程内一致性，并为以后 IndexDB CAS/事务扩展留接口。

## 2. 协议基线

设计以 [RFC 9111 §4.1](https://www.rfc-editor.org/rfc/rfc9111.html#section-4.1) 和 [RFC 9110 §12.5.5](https://www.rfc-editor.org/rfc/rfc9110.html#section-12.5.5) 为准：

- 缓存首先按方法和目标 URI 找候选响应，再按候选响应自身的 `Vary` selecting header fields 匹配原始请求。
- Header 名大小写不敏感。
- 缺失字段只能匹配同样缺失的字段；“缺失”和“存在但值为空”不能混为一类。
- 只有字段规范明确允许时，才可做语义归一化。不能对所有逗号列表统一排序。
- `Vary: *` 永远不能在不回源验证的情况下匹配，应视为不可复用响应。
- 多个候选都匹配时优先选择最新响应；有明确偏好排序语义时可以进一步择优。

由此得到核心原则：**`Vary` schema 属于每个已存响应，而不是整个 URL 永久唯一的属性。**

## 3. 当前实现与问题

### 3.1 当前数据路径

当前实现位于：

- `server/middleware/caching/caching_vary.go`
- `pkg/x/http/varycontrol/vary.go`
- `api/defined/v1/storage/object/object.go`
- `storage/bucket/{disk,memory}`

根对象使用 `FlagVaryIndex`，在 `Headers["Vary"]` 中保存唯一 Vary 字段集合，在 `VirtualKey []string` 中保存所有子对象的字符串后缀。子对象使用 `FlagVaryCache`。请求命中根对象后，根据根对象的 `Vary` 和当前请求头重新生成后缀，再直接查找子对象。

### 3.2 已确认的缺口

| 类别 | 当前行为 | 风险 |
| --- | --- | --- |
| 忽略项 | `vary_ignore_key` 被读入 `VaryProcessor`，但未参与清洗与匹配 | 配置不生效 |
| Header 名 | 清洗未统一大小写 | `User-Agent` 与 `user-agent` 可能形成不同 schema |
| Header 值 | 所有逗号分隔值都会排序 | 对顺序有语义的字段可能错误合并 |
| 缺失字段 | 缺失值序列化为空字符串 | 容易与显式空值混淆 |
| 序列化 | 使用 `key=value&...` 裸拼接 | Header 值包含 `&`、`=` 时存在歧义和碰撞 |
| 通配符 | 没有单独拒绝 `Vary: *` | 可能错误缓存并复用本应始终不匹配的响应 |
| Schema | 根索引只保存一套 `Vary` | 源站不同响应返回不同 Vary 集合时会整体重建或选错 |
| 元数据 | `VirtualKey` 同时承担身份、枚举和删除依据 | 无版本、无 schema、无转换维度，难以演进 |
| 升降级 | Vary 消失时倾向于把整个 URL 降级为普通对象 | 不能与仍然有效的其他 Vary 响应正确共存 |
| 并发更新 | 根索引为 read-modify-write，缺少基于 base key 的串行化 | 并发新增 variant 会丢引用或留下孤儿对象 |
| 请求合并 | 未知 Vary 时按 base object ID 合并回源 | 不同请求头的请求可能错误共享同一个源站响应 |
| 数量上限 | 达到 `vary_limit` 后返回错误；上层会吞掉该错误继续响应 | 可能以错误身份写缓存，且没有明确的 admission/eviction 语义 |
| 生命周期 | 根索引与子对象 TTL、访问时间没有一致规则 | 根先淘汰会留下孤儿，子先淘汰会留下悬空引用 |
| 隐私 | `VirtualKey` 可能直接包含 Cookie 等原始请求值 | 索引数据与日志可能泄漏敏感信息 |
| 可测试性 | main 分支没有 Vary 单元测试 | 边界行为无法稳定回归 |

另外，当前缓存写入包装在 `Processor.PostRequest` 之前建立。未来如果转换处理器在 `PostRequest` 中包装响应 Body，客户端可能得到转换后内容，但缓存层实际保存的是转换前内容。因此图像转换不能只作为现有 Processor 链中的普通后处理器插入。

## 4. 目标模型

### 4.1 三层身份

一个缓存版本由三层信息决定：

```text
BaseKey
  = method-class + scheme + authority + path [+ query]

OriginSelector
  = 当前候选响应声明的 Vary fields
  + 该响应产生时原始请求中这些 fields 的规范化摘要

TransformDimensions
  = Tavern 对最终表示执行的内部转换结果
  例如 image=webp;q=0.7

VariantIdentity
  = versioned_hash(BaseKey, OriginSelector, TransformDimensions)
```

说明：

- `OriginSelector` 表达源站如何选择表示。
- `TransformDimensions` 表达 Tavern 如何把源站表示转换成最终缓存表示。
- `image` 不是伪造的 HTTP 请求头，也不会写进 `Vary` 响应头；它是 Tavern 内部命名空间。
- 示例中的 `q=0.7` 定义为编码质量参数，不是客户端 `Accept` 的 qvalue。
- 若图像转换由请求 `Accept` 决定，最终响应仍需包含 `Vary: Accept`，但内部只保存解析后的转换结果，避免把大量语义等价的 `Accept` 字符串拆成不同图像版本。

### 4.2 每个 variant 自带 schema

根索引不再保存全局唯一 `Vary` 集合。每个 `VariantRef` 都记录自己的 schema：

```go
// 设计示意，字段名可在实现时按项目风格调整。
type VariantIndex struct {
    Version  uint8
    Revision uint64
    Variants []VariantRef
}

type VariantRef struct {
    ObjectKey      string
    Schema         VarySchema
    SelectorDigest string
    Dimensions     []VariantDimension
    StoredAtUnix   int64
    ResponseDate   int64
}

type VarySchema struct {
    ID                   string
    Fields               []string
    NormalizationVersion uint16
    PolicyFingerprint    string
}

type VariantDimension struct {
    Namespace string // 例如 "image"
    Value     string // 例如 "webp;q=0.7"
}
```

`object.Metadata` 增加可选字段：

```go
VariantIndex *VariantIndex `json:"variant_index,omitempty"`
Variant      *VariantMeta  `json:"variant,omitempty"`
```

现有 `VirtualKey` 在迁移期只用于读取和清理 v1 数据，v2 写入不再依赖它。

### 4.3 不保存原始 selecting header 值

根索引只保存 selector 的加盐摘要，不保存 Cookie、Authorization 等原始值。摘要输入采用无歧义的长度前缀编码：

```text
schema-version
field-count
repeat(
  field-name-length, field-name,
  present-bit,
  value-length, normalized-value
)
```

再使用 SHA-256/HMAC-SHA-256 生成 `SelectorDigest`：

- 若使用普通 SHA-256，低熵 Cookie/实验分组仍可能被离线枚举。
- 推荐使用进程配置密钥派生的 HMAC-SHA-256；密钥变更通过 `PolicyFingerprint` 触发自然 miss 和旧版本淘汰。
- 不把规范化明文写入日志；Debug 日志只输出 schema ID、variant token 的短摘要和维度。

### 4.4 版本化 ObjectKey

子对象后缀使用版本化、不透明、可安全拼接的 token，例如：

```text
vk2.<schema-id>.<selector-digest>.<dimension-digest>
```

完整维度保存在 `VariantRef.Dimensions`，token 中只放摘要。这样：

- 不受 `&`、`=`、逗号和非 ASCII 值影响。
- 删除子对象仍可直接通过 `ObjectKey` 重建 `object.NewVirtualID(basePath, objectKey)`。
- 以后改变规范化或转换算法时，通过 `vk3` 或 `NormalizationVersion` 隔离，不会误命中旧对象。

## 5. Vary 规范化与匹配

### 5.1 Schema 清洗

对 `Vary` 字段名执行：

1. 按 HTTP list 语法拆分。
2. trim OWS。
3. 转为统一小写形式进行比较和摘要。
4. 大小写不敏感去重并排序。
5. 在任何 ignore 规则之前识别 `*`；`*` 不允许被 ignore。
6. 根据配置过滤 `vary_ignore_key`。

`vary_ignore_key` 本质上是可能破坏协议正确性的运维覆盖项，应保留但明确标为 unsafe：忽略 Cookie 或 Authorization 可能导致跨用户内容泄漏。实现需要：

- 配置项自身大小写不敏感。
- 启动时记录一次 warning，列出被忽略的字段名。
- 只影响 Tavern 内部匹配，不能从转发给客户端的源站 `Vary` 响应头中删除这些字段。

### 5.2 值规范化

提供字段级 normalizer registry：

```go
type HeaderNormalizer interface {
    Normalize(values []string) (normalized []byte, valid bool)
}
```

默认 normalizer 采取保守策略：

- 保留多个 field line 的原始顺序。
- 只合并规范允许的 OWS，不对逗号成员排序。
- 用独立 `present` 位区分缺失与空值。
- 非法值不做激进修复，以规范化后的原始字节参与摘要。

首期只为有明确规范和测试覆盖的字段增加专用 normalizer，例如 `Accept-Encoding`。专用实现必须保留 qvalue 的语义，不能像简单字符串处理那样直接去掉所有 `;q=`。`Accept`、`Accept-Language`、`Cookie`、`User-Agent` 初期均走保守路径，后续再按字段规范扩展。

### 5.3 候选选择

请求命中 `VariantIndex` 后：

1. 由转换规划器得到当前请求期望的 `TransformDimensions`。
2. 按维度过滤候选 `VariantRef`。
3. 对每一种候选 schema 仅计算一次当前请求的 selector digest。
4. digest 相同的候选视为匹配。
5. 如果多个候选匹配：优先有效 `Vary` schema，再选择 `ResponseDate/StoredAtUnix` 最新者。
6. 查找其子对象；若子对象不存在，清理悬空引用并继续检查下一个候选。
7. 没有候选时 MISS。

空 schema 是合法的，表示该响应没有源站 `Vary`。当同一 URL 已经是 variant index 时，无 Vary 响应也作为一个子对象保存，而不是立刻销毁整个 index。这样可遵循 RFC 对“有 Vary 和无 Vary 响应并存”的选择建议。

`Vary: *` 的响应不加入 index，也不写普通缓存；仅透传给客户端。

## 6. 写入与状态迁移

### 6.1 状态机

```text
ABSENT
  ├─ response without Vary/transform ──> NORMAL
  ├─ response with Vary ───────────────> VARIANT_INDEX + child
  └─ transformed response ─────────────> VARIANT_INDEX + child

NORMAL
  ├─ ordinary refresh ─────────────────> NORMAL
  ├─ response with Vary ───────────────> VARIANT_INDEX + child
  └─ transform becomes applicable ─────> VARIANT_INDEX + child

VARIANT_INDEX
  ├─ matching response schema ─────────> add/replace child
  ├─ different Vary schema ────────────> add child with new schema
  ├─ response without Vary ────────────> add empty-schema child
  └─ no live children after cleanup ───> delete index; next request rebuilds
```

不同 schema 不再触发“先删除全部旧 variant 再重建”。每个已存响应按自己的 `Vary` 继续参与匹配，直到过期、被重验证替换或淘汰。

### 6.2 提交顺序

为避免根索引引用不存在的对象，采用“子对象先提交、索引后发布”：

1. 计算最终 schema、selector digest、转换维度和子对象 ID。
2. 响应 Body 经最终转换后写入子对象的 chunk。
3. Body 正常 EOF 后保存完整子对象元数据。
4. 使用不可变 base ID 获取 index update lock。
5. 在锁内重新读取最新根索引，合并/替换 `VariantRef`，执行上限检查。
6. 保存根索引。
7. 释放锁。

如果 Tavern 仍希望发布部分对象以支持边下边缓存，根引用可以提前发布，但必须在引用中增加 `State: writing/ready` 和写入租约，并保证读取端不会把失败的 writing 对象当成完整命中。首期推荐先采用 EOF 后发布，降低一致性复杂度。

根索引更新不能复用当前会随 variant 改变的 `Caching.id` 作为锁键，应固定使用 `baseID.HashStr()`。建议封装：

```go
type VariantIndexStore interface {
    Lookup(ctx context.Context, baseID *object.ID, plan VariantPlan) (*object.Metadata, error)
    Update(ctx context.Context, baseID *object.ID, fn func(*VariantIndex) error) error
    Remove(ctx context.Context, baseID *object.ID, objectKey string) error
}
```

以后 IndexDB 支持 CAS/事务时，只替换 `Update` 实现。

### 6.3 304 与 Vary 变化

- 304 未携带 `Vary`：保留被验证 variant 原有 schema，不得据此降级为无 Vary。
- 304 携带相同 schema：更新 freshness 和允许更新的响应头。
- 304 携带不同 schema：在当前请求下重新计算 selector，以新身份重新索引同一个表示；旧引用在成功发布新引用后删除。
- 200 返回不同 schema：作为新响应、新 variant 提交；不原地覆盖旧身份。

## 7. 并发请求合并

现有 object collapsed forwarding 的 key 不能始终使用 base ID：未知 `Vary` 时，不同请求头可能得到不同表示。

新的 flight key 规则：

1. 已有 variant index 且能形成确定计划：使用最终 variant token。
2. 没有索引或 schema 未知：使用 `base ID + conservative request fingerprint + transform plan fingerprint`。
3. conservative fingerprint 至少覆盖所有端到端请求头的名称、presence 和保守规范化值；排除 hop-by-hop、追踪头和 Tavern 内部头。
4. 不同 fingerprint 不合并。宁可少合并，也不能错误共享响应。

若源站响应最终产生了不同的 variant token，不影响本次响应，但后续请求会使用正式 token 合并。

## 8. 数量限制、淘汰与清理

### 8.1 `vary_limit`

定义为一个 base key 下所有 origin schema 与 transform dimension 组合的总 variant 数上限。

默认策略：

- 新 key 已存在：替换，不占新额度。
- 有悬空引用：优先清理悬空引用。
- 达到上限：淘汰最久未访问的子对象（LRU）。
- 淘汰失败：本次新响应不入缓存，但正常透传；绝不能退回 base ID 写入。
- 配置 `vary_limit <= 0` 视为禁用 variant 缓存，启动时给出明确 warning。

首期可在达到上限时扫描最多 `vary_limit` 个子对象的 `LastRefUnix`，避免每次 HIT 都写根索引。后续如需更低延迟，可将采样访问时间异步批量回写到根引用。

### 8.2 TTL 与删除

- freshness 属于子响应，各 variant 独立计算 `ExpiresAt`。
- 根索引不是可直接服务的 HTTP 响应，不参与普通 freshness 判断。
- 根索引只要还有子引用就保留；其 GC 时间应为所有子对象最大保留时间加清理宽限期。
- 删除根索引时按 `VariantRef.ObjectKey` 递归删除所有子对象。
- 删除子对象时同步移除根引用；异常退出留下的悬空引用在 lookup/GC 时惰性修复。
- GC 需要同时识别“无根子对象”和“无子根索引”。

## 9. 图像转换扩展点

### 9.1 规划与转换接口

图像转换需要在缓存查找前完成“期望版本规划”，在存储包装前完成“最终响应转换”。建议新增独立于现有 Processor 的表示管线：

```go
type RepresentationTransformer interface {
    Name() string

    // 请求阶段：根据请求、配置和已知索引信息选择期望维度。
    Plan(req *http.Request, index *VariantIndex) (TransformPlan, error)

    // 响应头阶段：确认 Content-Type 等前置条件，并最终确定维度。
    Finalize(req *http.Request, resp *http.Response, plan TransformPlan) (TransformPlan, error)

    // Body 阶段：在缓存写入包装器之外、之前执行转换。
    Transform(ctx context.Context, resp *http.Response, plan TransformPlan) error
}

type TransformPlan struct {
    Applicable       bool
    Dimension        VariantDimension
    SelectingHeaders []string
    PolicyVersion    string
}
```

图像实现示例：

```text
request Accept: image/avif,image/webp,*/*;q=0.8
server policy: webp quality=0.7
plan dimension: image=webp;q=0.7
selecting header: Accept
policy version: image-v1/libwebp-<encoder-version>
```

`PolicyVersion` 必须参与 dimension fingerprint。编码器版本、质量、色彩空间、元数据保留策略等任何会改变输出字节或语义的配置发生变化时，都应自然生成新 variant，而不是覆盖旧身份。

### 9.2 正确的响应管线顺序

目标顺序：

```text
origin response
  -> cacheability / Vary 分析
  -> transformer Finalize
  -> transformer 修改响应头并包装 Body
  -> 计算最终 variant identity
  -> cache Savepart 包装“转换后的 Body”
  -> client
```

即缓存和客户端看到同一份最终表示。不能采用：

```text
origin Body -> cache Savepart -> image transform -> client
```

否则缓存内容、`Content-Type`、ETag 和 variant identity 会互相不一致。

### 9.3 图像响应头规则

转换成功时至少处理：

- 设置目标 `Content-Type`，例如 `image/webp`。
- 将转换规划所依赖的请求字段并入客户端可见 `Vary`，通常为 `Accept`。
- 未知转换后长度时删除源站 `Content-Length`，由流结束时写入缓存元数据。
- 删除或重建源站强 ETag；推荐生成由“源 validator + transform fingerprint”派生的弱 ETag。
- 保留源 `Last-Modified` 仅表示源资源修改时间，不作为转换字节强校验器。
- 删除不再有效的 `Content-MD5`、`Digest`/`Content-Digest` 等实体摘要。
- 明确处理 `Content-Encoding`：图像格式转换与 HTTP content coding 是两个不同维度。

### 9.4 Range、HEAD 与失败降级

- 冷缓存 Range 请求首期不做图像转换，直接走 origin/bypass；已完整缓存的转换 variant 可以正常提供 Range。
- HEAD 可以命中已有转换 variant 的元数据，但不触发新的无 Body 转换写入。
- 转换失败时返回源站原图是否允许缓存，必须由明确策略决定；若缓存，应使用 `image=origin`，绝不能写到预定的 `image=webp;q=0.7` 身份下。
- 若 URL 是否为图像在 lookup 前未知，转换器可以返回 provisional plan；响应确认不是图像时落回无转换维度。该 provisional fingerprint 只影响本次请求合并，不作为最终缓存身份。

## 10. 配置建议

保持现有配置兼容，并新增版本化 variant 与转换配置：

```yaml
middleware:
  caching:
    vary_limit: 100
    vary_ignore_key: []        # unsafe override；默认空
    variant_index_version: 2
    variant_eviction: lru

    transforms:
      image:
        enabled: false
        formats: [avif, webp]
        quality:
          avif: 0.6
          webp: 0.7
        # 建议先用路由/Content-Type 规则限定候选，减少 provisional plan。
        path_patterns: []
```

在图像转换尚未实现时，只需要定义内部接口、dimension 数据结构和 `variant_index_version`，`transforms.image.enabled` 保持 false。

## 11. 可观测性

新增指标建议：

```text
tavern_cache_variant_lookup_total{result=hit|miss|dangling|star,schema,transform}
tavern_cache_variant_count{bucket}
tavern_cache_variant_admission_total{result=stored|replaced|bypass|evicted}
tavern_cache_variant_eviction_total{reason=limit|expired|dangling|policy_change}
tavern_cache_variant_index_update_total{result=success|conflict|error}
tavern_cache_transform_total{name,format,result}
tavern_cache_transform_duration_seconds{name,format}
```

日志不得输出原始 selecting header 值。问题定位使用 request ID、base hash、schema ID、selector digest 前 8～12 位、transform dimension。

## 12. 迁移与发布

v1 的 `VirtualKey` 字符串无法可靠推导 v2 schema、presence 和规范化版本，不应静默转换。

采用惰性失效：

1. 新代码能识别 v1 `FlagVaryIndex + VirtualKey`。
2. v1 根索引命中时，可以在短期兼容读取；一旦需要新增/更新 variant，则先使用现有递归删除逻辑清理整个 v1 family，再按 v2 回源重建。
3. v2 token 使用独立 `vk2.` 前缀，绝不与旧后缀混写。
4. 普通非 Vary 缓存对象继续兼容，不做全盘 purge。
5. 发布前确认 tableflip 升级过程中旧进程已关闭 storage；若未来出现多个版本同时写同一 IndexDB，必须先增加跨进程 CAS/租约。

更稳妥的两阶段发布方式：

- 阶段 A：发布可读 v1/v2、默认仍写 v1 的兼容代码和监控。
- 阶段 B：全部节点升级后启用 `variant_index_version: 2`。

如果每个 Tavern 节点使用独立缓存盘，也可以直接启用 v2 惰性重建。

## 13. 测试计划

### 13.1 `varycontrol` 单元测试

- Header 名大小写不敏感、去重、稳定排序。
- 多行 `Vary` 与逗号列表。
- `Vary: *`、`*, Accept`。
- missing 与 empty 不同。
- 值包含 `&`、`=`、逗号和 Unicode 时无碰撞。
- 默认 normalizer 不改变有序字段语义。
- `Accept-Encoding` qvalue、wildcard、identity、非法值。
- ignore key 大小写不敏感且不能忽略 `*`。
- selector digest 和 dimension token 的 golden test。

### 13.2 `VaryProcessor`/VariantIndex 单元测试

- 普通对象首次升级成 index + child。
- 相同 schema/selector 命中，相同 schema/不同 selector miss。
- 同 URL 多个 schema 共存并选择最新候选。
- 有 Vary 与无 Vary 子响应共存。
- 304 无 Vary 不降级；304 schema 改变时重新索引。
- `Vary: *` 不存储。
- `vary_limit` 替换、LRU 淘汰、淘汰失败 bypass。
- 悬空引用修复和孤儿清理。
- v1 index 惰性失效与递归删除。
- Cookie 等原始值不出现在元数据和日志中。

### 13.3 并发与集成测试

- `go test -race` 下并发新增两个 variant，根索引不丢引用。
- 未知 Vary 时不同请求头不 collapsed；相同 fingerprint 正常 collapsed。
- variant 写入失败不会发布根引用。
- disk/memory bucket 删除根索引会清理所有 v2 子对象。
- chunked、Range、异步 flush、revalidate 分别覆盖 Vary variant。
- mockserver 增加动态 Vary、`Vary: *`、missing/empty、多个 schema 场景。
- 图像接口阶段用无损 fake transformer 验证“先转换、后缓存”和失败落回身份。

## 14. 分阶段实施

### 阶段 1：协议正确性与测试基线

- 为 `varycontrol` 增加完整单元测试。
- 修复字段名、presence、通配符、抗歧义序列化和字段级 normalizer。
- 让 `vary_ignore_key` 真正生效并增加 unsafe warning。
- 对达到上限和处理失败明确返回 `bypass-store`，禁止写入错误 ID。

### 阶段 2：VariantIndex v2

- 增加 v2 元数据和 token。
- 将根索引改为 per-variant schema 候选集合。
- 增加 base-key index lock、正确提交顺序、LRU 与悬空引用修复。
- 修复 collapsed forwarding flight key。
- 完成 v1 惰性迁移和 bucket 递归删除适配。

### 阶段 3：表示转换预留

- 引入 `RepresentationTransformer`、`TransformPlan` 和 `VariantDimension`。
- 重排响应管线，保证 transformer 位于 Savepart 之前。
- 加入 fake transformer 测试，默认不开启任何真实转换。

### 阶段 4：图像转换实现

- 增加 Accept 协商、WebP/AVIF encoder、质量与 policy fingerprint。
- 完成响应头、Range/HEAD、资源限制、超时和失败降级策略。
- 压测 CPU、内存、首字节延迟、命中率与 variant 数量。

## 15. 验收标准

满足以下条件后可认为 Vary 优化完成：

1. RFC 9111 §4.1 的关键匹配规则均有自动化测试。
2. 同一 URL 可安全共存多个 schema 和多个 selector variant。
3. `Vary: *` 永不被直接复用。
4. 并发新增 variant 不丢索引、不错误 collapsed、不留下可命中的错误身份。
5. 达到 `vary_limit` 时只发生受控淘汰或本次 bypass，不污染普通 base cache。
6. 元数据和日志不保存 selecting header 明文。
7. v1 数据可安全惰性清理，普通缓存不受影响。
8. fake image transformer 能以 `image=webp;q=0.7` 形成独立 variant，且缓存字节与客户端字节一致。

## 16. 设计决策摘要

- 选择“每个 variant 自带 Vary schema”，不选择“每个 URL 只有一个 Vary schema”。
- 选择“源站 selector + 内部 transform dimensions”双层版本键，不把 image 伪装成请求头。
- 选择“摘要保存 selecting values”，不在索引中保存敏感明文。
- 选择“字段级保守规范化”，不对所有逗号值统一排序或去掉 qvalue。
- 选择“子对象先提交、根索引后发布”，避免悬空可见引用。
- 选择“未知 Vary 时保守拆分 collapsed flight”，正确性优先于额外合并率。
- 选择“v2 token + 惰性失效”，不对歧义的 v1 `VirtualKey` 做猜测式迁移。
