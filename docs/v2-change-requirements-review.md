# v2 系列变更：需求目的、实现动机与最终审查结论

审查日期：2026-11-02（Asia/Shanghai）。

**最终验收结论：验收均不通过。**

本文覆盖整个 v2 系列，包括透明 Vary 缓存、表示存储与失效协调、图片转换、请求合并、公开配置，以及后续 memory、默认控制目录、批量写回和源站身份修正。本文是需求与实现的审查记录，不是新的实施授权，也不表示已完成整改。

## 1. 结论与证据边界

### 1.1 最终结论

本轮允许大幅调整流程，但必须满足原有 CDN 业务要求，并提供可阅读、可维护的实现。当前系列未满足这些交付条件：

1. 普通缓存路径与图片转换路径混杂；状态含义、对象归属和关闭责任难以辨认。
2. v2 独立路径绕过原有处理器，未完整承接预取、补块、错误码缓存和校验通知。
3. Range 缺块退化为全量回源；小范围请求和中断请求难以积累缓存。
4. 缓存命中和启动路径增加了随整个对象、缓存总量增长的检查工作。
5. 刷新历史容量、存储拓扑和混合存储被加入新的限制，无法覆盖持续运行的 CDN 运维场景。
6. 原有业务用例仍失败；定向单测和内部不变量验证被错误扩大为整体交付结论。
7. 数据库同步提交等实现机制曾被当作业务必须条件；当前虽已加入批量写回，也没有因此通过性能、兼容性和整体业务验收。

本轮用户给出的“验收均不通过”适用于整个系列。它不等于每个内部函数都没有价值，也不等于所有测试均执行失败。单项机制可保留，整套交付仍被拒收。

### 1.2 基线与审查对象

| 项目 | 本次采用的范围 |
| --- | --- |
| 改造前基线 | f14725a，位于本系列首个 Vary 修改之前；只用作差异基线，不代表已重新证明基线没有缺陷 |
| 已提交系列 | 93119b8 至 4862788，共 18 个提交 |
| 当前源码 | HEAD 4862788 加本地未提交修改及新增文件；不能仅用 HEAD 哈希代表本次测试源码 |
| 已有设计材料 | [原始设计](vary-representation-design.md)、[实施方案](vary-representation-implementation-plan.md)、[历史验收记录](vary-representation-acceptance.md) |
| 本轮环境 | darwin/arm64，实际 Go 工具链 go1.27.3；go.mod 声明 go 1.26.0 |
| 本轮工作 | 对照源码、历史、已有测试日志，并归档证据；编写本文时未再次运行构建或 Go 测试 |

本文所述“当时为何这样改”，依据设计说明、提交说明和代码行为重建；未有直接记录的部分明确作为工程动机分析。**写进方案的技术选择，不能据此推定为用户已经认可的业务需求。**

### 1.3 证据等级

- **运行复现**：本次会话实际执行的测试或诊断探针，有归档输出。
- **静态确认**：调用关系、条件分支、资源释放或复杂度可以从当前源码确认；不伪装为生产性能测量。
- **历史记录**：旧文档或提交记录写明曾验证；本次没有独立重跑，不将其视为当前验收通过。
- **待验证**：缺少生产规模、故障矩阵或业务兼容证据，不能填写“已完成”。

## 2. 原始业务目标与不可丢失的能力

原始设计要求透明 Vary 正确性、源图复用和派生成品复用，并保留既有 Bucket、IndexDB 与分块响应基础。可重构流程不等于可以删减这些基础能力。

| 业务约束 | 必须达到的行为 | 当前审查结论 |
| --- | --- | --- |
| 公开 URL 身份 | 切源、主备和负载均衡保持同一缓存身份；明确的 namespace、隔离配置及 Vary 仍有效 | 地址绑定已局部修正；整个系列未通过 |
| 首包和流式传输 | 不为缓存提交或响应合并无条件等待整文件读完 | Range 裁剪和可共享冷响应存在全量读取路径；无完整首包基准 |
| 按需回源 | 小 Range 优先只取所需分片及配置允许的补齐范围 | v2 已有对象缺块时直接整文件回源 |
| 渐进缓存 | 下载中断后可按版本证明保留并复用已确认分片 | v2 未发布填充在中断时整体清理 |
| 过期重验证 | 复用正确表示的验证器；源未变化时复用缓存字节 | 普通 GET 有分支；带 Range 的请求被排除 |
| 负缓存 | 显式错误码缓存开关及 TTL 有效，不能默默失效 | v2 准入只接受 200/206，既有用例失败 |
| 预取 | X-Prefetch 原有语义得到保留或有经确认的替代 | v2 绕过旧预取流程 |
| 持续刷新 | URL/目录刷新不会因运行期间不同 URL 累积而很快耗尽不可回收容量 | 固定屏障目录容量不满足该要求 |
| 存储运维 | 支持实际分层、换盘和扩缩容策略；故障盘处理边界明确 | 混合存储拒绝，拓扑变更要求离线迁移或重建 |
| 数据库写入 | 普通缓存写入服从缓存软件的吞吐和延迟要求，可靠失效单独设计 | 当前有批量写回；历史同步方案的范围曾扩大，性能尚未验收 |
| 可维护性 | 领域、入口、状态、资源所有者和结束条件可直接辨认 | 未满足 |

## 3. 每项需求：目的、改法、动机和结论

以下 R01—R24 是审查用编号。前 18 项对应原阶段需求，后 6 项覆盖扩展、兼容性和本轮明确的交付要求。它们不新增业务授权。

### R01 透明 Vary 解析与选择正确性

- **目的**：JPEG/WebP、gzip/identity 及其他响应选择维度不能串内容；字段缺失与空值、多行和组合字段需正确区分。
- **原问题**：旧逻辑把字段值按逗号拆分排序、用字符串拼接键，对星号、字段变化及 VirtualKey 清理处理不可靠。
- **改法与动机**：新增结构化 Snapshot、字段解析、选择指纹；每个引用保存自己的 Vary 字段集合，用摘要定位后再比较完整快照，消除歧义。
- **主要位置**：[varycontrol](../pkg/x/http/varycontrol/)、[表示模型](../api/defined/v1/storage/object/representation.go)、[索引匹配](../storage/representation/index.go)。
- **结论**：纠正选择语义的目的合理，应保留；不能以此要求普通缓存放弃原有 Range、重验证和防击穿能力。历史解析/选择用例属于局部证据，不构成当前整体验收。

### R02 缓存资格与禁止共享的响应

- **目的**：private、no-store、Vary 星号或无法安全选择的内容不能被错误共享；内部 TTL 不能随意覆盖禁止存储约束。
- **改法与动机**：在写缓存之前统一做准入；带 Authorization、no-cache/max-age=0 及客户端条件的请求被保守旁路，以简化首版复用证明。
- **主要位置**：[caching_vary.go](../server/middleware/caching/caching_vary.go)、[representation_freshness.go](../server/middleware/caching/representation_freshness.go)、[header.go](../pkg/x/http/header.go)。
- **偏离**：禁止存储、需要重验证、需要执行客户端条件是不同语义。当前把多种情况归并成“既不读也不写”，省掉了协议处理，却加重源站压力。
- **证据与结论**：E06 复现新鲜条件请求仍回源，以及 no-cache 取到新内容后普通请求仍返回旧缓存。响应 no-cache 的存储不足也已观察到，但不能将所有历史限制都归因于 v2 新增。应拆分策略，当前未通过。

### R03 资源索引与具体响应分离

- **目的**：一个 URL 管理多个可独立选择的响应；索引更新不能丢失其他并发新增表示。
- **改法与动机**：引入 ResourceRecord、VariantRef 和 RepresentationInfo；控制根只管理引用、版本、位置，具体响应管理头、长度与分片。
- **主要位置**：[对象模型](../api/defined/v1/storage/object/representation.go)、[index.go](../storage/representation/index.go)、[publication.go](../storage/representation/publication.go)。
- **偏离**：不同记录继续共用 Metadata，并通过大量可选字段区分；caching 同时使用 md、rootmd、resourceMD，逻辑对象和物理对象的含义不直观。
- **结论**：索引与响应分离的需求保留；类型和状态边界需要重整，当前结构未通过可维护性验收。

### R04 200/304 更新及变体预算

- **目的**：200 改变或移除 Vary、304 更新头部时，仍能选择正确响应；并发填充不越过版本预算，不覆盖较新数据。
- **改法与动机**：对资源更新加锁；增加引用 revision、预留项和替换预留；304 合并后重新验证选择、Age 与依赖。
- **主要位置**：[representation_origin.go](../server/middleware/caching/representation_origin.go)、[reservation.go](../storage/representation/reservation.go)、[publication.go](../storage/representation/publication.go)。
- **偏离**：缓存重验证只对完整对象且客户端无 Range 的分支进行；正确更新元数据的实现没有覆盖既有 Range 重验证用途。
- **证据与结论**：原 TestRevalidateDoubleSkip、TestRangeExpiredRefresh 仍失败；固定 peer 和使用合法 ETag 的对照也没有消除 Range 排除条件。内部索引并发机制有必要，需求整体未通过。

### R05 固定填充目标与内容代际隔离

- **目的**：异步写入期间不能因可变 c.id/c.md 改写其他响应；新旧版本字节不能混进同一对象。
- **改法与动机**：旧路径引入 writeSession；v2 引入固定 ID、Bucket、Metadata 快照和 body_generation 的 representationWriter。
- **主要位置**：[write_session.go](../server/middleware/caching/write_session.go)、[representation_writer.go](../server/middleware/caching/representation_writer.go)、[legacy_range.go](../server/middleware/caching/legacy_range.go)。
- **偏离**：v2 采用不续写已发布代际、EOF 才发布、中断整体撤销的规则。强版本校验下按需补块的合理目标，被简化成整文件替换。
- **证据与结论**：E06 复现缺块时 Range 被删除；已有取消测试确认已写完整块也不发布。固定写入目标应保留，普通 CDN 填充策略必须重做；不能回退为无版本证明的拼块。

### R06 持久化与失效屏障

- **目的**：PURGE 成功后，旧在途请求和重启前的旧内容不能重新成为新鲜缓存。
- **改法与动机**：Capture 记录 incarnation 和失效 epoch，Reserve/Publish 再次校验；早期方案增加同步子元数据、根记录、分片和目录的提交顺序，以获得严格持久发布。
- **主要位置**：[durability.go](../storage/representation/durability.go)、[publication.go](../storage/representation/publication.go)、[磁盘能力](../storage/bucket/disk/representation.go)。
- **偏离**：防止失效历史回退，被扩大成普通缓存每次发布都必须同步封存的设计前提。缓存允许丢失近期内容与可靠失效需要分别建模。
- **当前状态与结论**：公开 Manager 已使用 WithWriteback，普通数据/根发布不再按早期方式强制同步；PURGE 仍有同步屏障。严格模式接口仍保留。不能把历史问题写成“当前全部直写”，也不能因加入写回就宣称性能合格。当前未通过。

### R07 URL/目录 PURGE 与派生依赖失效

- **目的**：soft 失效全部变体、hard 阻断旧填充；目录刷新覆盖尚未建立的资源；源变化后派生成品不能继续新鲜命中。
- **改法与动机**：用资源 epoch、持久目录/精确 URL 屏障及源版本依赖统一判断，避免扫描不完整导致漏失效。
- **主要位置**：[invalidation.go](../storage/representation/invalidation.go)、[dependency.go](../storage/representation/dependency.go)、[purge 插件](../plugin/purge/purge.go)、[managed_purge.go](../server/middleware/caching/managed_purge.go)。
- **偏离**：不同精确 URL 的失效历史进入默认 4096 条、1 MiB 的固定目录；现有压缩不能清理彼此独立的精确 URL 记录。受控元数据预算变成了持续刷新的容量瓶颈。
- **证据与结论**：E06 用缩小到 2 条的同一机制验证第 3 个不同 URL 失败且压缩删除 0 条；默认 4096 来自源码静态确认，未跑 4097 次生产规模实验。安全失效目标保留，容量和回收设计未通过。

### R08 固定定位、集合迁移和分层存储

- **目的**：资源根和表示在迁移、淘汰中仍能正确定位；旧读者在迁移后可以读完。
- **改法与动机**：Manager 固定控制所有者，资源记录 ActiveLocation，集合复制后切换位置；引入读/写/迁移保护。
- **主要位置**：[manager.go](../storage/representation/manager.go)、[migration.go](../storage/representation/migration.go)、[read_lease.go](../storage/representation/read_lease.go)、[migrator.go](../storage/migrator.go)。
- **偏离**：拓扑指纹将新增、移除或替换 Bucket 拒绝为启动错误，缺少可执行的正常扩缩容流程；v2 集合迁移也未完整接回既有自动 Promote/Demote。
- **证据与结论**：E06 的既有测试确认缩容拒绝。固定控制所有权不应等同于永远固定磁盘集合；分层和运维兼容未通过。

### R09 接通 next/origin 与中间件顺序

- **目的**：缓存真正使用注入的 RoundTripper，使 rewrite、转换和测试替换能够按链路执行。
- **改法与动机**：缓存统一从 origin 执行回源；选择字段 rewrite 要在缓存查找之前完成，避免 HIT/MISS 使用不同选择条件。
- **主要位置**：[proxy/transport.go](../proxy/transport.go)、[caching.go](../server/middleware/caching/caching.go)、[server.go](../server/server.go)。
- **偏离**：接通请求执行接口时，普通 v2 同时另开准入和响应路径；原有 Processor 的预取、补块等未逐项承接。
- **结论**：注入回源接口和正确顺序保留；接通接口不等于允许跳过业务处理，兼容性未通过。

### R10 请求状态、后台刷新和进程交接

- **目的**：后台刷新不能修改前台状态；停机与 tableflip 升级前须停止旧写入者，等待实际文件和 Body 关闭。
- **改法与动机**：增加任务登记、独立快照、Body 清理包装、存储 Drain 和所有权锁，协调网络、异步写入和存储关闭。
- **主要位置**：[task.go](../server/middleware/caching/task.go)、[legacy_lease.go](../server/middleware/caching/legacy_lease.go)、[representation_lease.go](../server/middleware/caching/representation_lease.go)、[storage/lifecycle.go](../storage/lifecycle.go)、[upgrade.go](../upgrade.go)。
- **偏离**：关闭责任散落于 defer、取消回调、Body.Close 和后台 goroutine；普通缓存任务还写死 2 分钟上限，没有与大文件和慢客户端需求对齐。
- **结论**：停写交接和状态独立的目标保留；生命周期应能直接审查，超时策略需独立设计。目前没有慢速大文件等完整业务证据，未通过。

### R11 公开开关、存储接入与回滚

- **目的**：透明 v2、图片转换和响应合并可分别灰度；关闭读取后仍能处理 PURGE 和旧数据清理。
- **改法与动机**：区分缓存读取开关与 storage.representation 控制所有权；存在控制历史时禁止直接移除绑定，避免失效历史丢失。
- **主要位置**：[representation_config.go](../server/middleware/caching/representation_config.go)、[storage/representation.go](../storage/representation.go)、[配置示例](../config.example.yaml)。
- **偏离**：开关和文档形成可部署外观，但未满足普通缓存的业务完整性；禁用 v2 后的路径也已受旧 Vary 旁路和旧合并停用等共享修改影响。
- **结论**：必须提供当前配置、支持矩阵、数据兼容和回退影响，不能把“默认关闭”当作系列完成依据。当前未通过。

### R12 Accept 协商、转换规格与路由

- **目的**：选择客户端接受的 JPEG/PNG/WebP；显式参数和自动协商产生可复用的完整转换规格。
- **改法与动机**：新增 transform 的 Accept 解析、规格规范化、路由和 Plan，通过上下文传递，避免信任客户端伪造内部转换头。
- **主要位置**：[transform](../transform/)、[转换契约](../api/defined/v1/transform/)、[transform 中间件](../server/middleware/transform/)。
- **结论**：q=0、格式能力、无效参数、no-transform 等正确性要求应保留；这部分不能成为重新定义普通缓存语义的理由。本轮只确认结构，历史编码/协商矩阵未独立重验，不判通过。

### R13 源图缓存与同源复用

- **目的**：同一 JPEG 源图生成 WebP/PNG 时，尽量只回源一次；源图自身的 Vary 仍有效。
- **改法与动机**：managed_source 产生独立源请求，移除客户端 Range/条件及明确的转换参数，使用受控 Accept 和源缓存；源任务按键排队后复查。
- **主要位置**：[managed_source.go](../server/middleware/caching/managed_source.go)、[managed_gate.go](../server/middleware/caching/managed_gate.go)。
- **边界**：图片解码和转换可要求完整源图；这个限制不能外推到普通文件的透明缓存。
- **结论**：源复用目标保留；源读取、解码、临时文件和索引注解集中于同一文件，需清楚区分资源所有者和失败清理。目前只有局部/历史证据，未通过系列验收。

### R14 派生成品缓存与源版本依赖

- **目的**：等价规格只转换一次；源变化后淘汰旧成品，源未变化的 304 可继续复用成品。
- **改法与动机**：成品键使用确切源 ID/版本、规格指纹及编码/策略版本；完整验证后发布，TTL 受源新鲜度约束。
- **主要位置**：[managed_provider.go](../server/middleware/caching/managed_provider.go)、[managed_product.go](../server/middleware/caching/managed_product.go)、[dependency.go](../storage/representation/dependency.go)。
- **结论**：完整转换成品后再发布有合理业务依据；普通透明文件不应被迫采用同样策略。成品依赖机制和任务合并需保留可测行为，当前可维护性及整体性能未通过。

### R15 HTTP worker、图片检查与资源预算

- **目的**：在 Tavern 保持 CGO_ENABLED=0 的前提下接入转换服务；限制输入、输出、像素、并发和排队，拒绝错误页或损坏输出。
- **改法与动机**：定义 HTTP worker 契约，启动时做转换能力检查，结果校验实际 MIME、字节和尺寸，使用受限临时文件。
- **主要位置**：[worker 契约](image-worker-contract.md)、[http_worker.go](../transform/http_worker.go)、[worker_probe.go](../transform/worker_probe.go)、[inspector.go](../transform/inspect.go)。
- **结论**：转换预算合理，但启动探测覆盖范围、耗时与不可用处理需有部署约束。旧文档的真实 WebP 测试不能替代本轮重新验收；尚无完整运行规模证据，未判通过。

### R16 成品 HTTP 响应和失败回退

- **目的**：HEAD、ETag/304、If-Range、单/多 Range 使用最终成品字节；转换失败只在策略和客户端能力允许时回源图。
- **改法与动机**：managed_response 使用 ServeContent 处理最终字节；源/成品分别维护头、摘要、长度和 Vary；失败按超时、排队等情况映射错误。
- **主要位置**：[managed_response.go](../server/middleware/caching/managed_response.go)、[managed_product.go](../server/middleware/caching/managed_product.go)、[托管图片说明](managed-image-operations.md)。
- **偏离风险**：HTTP 装配包含 Pipe、goroutine、Seek 包装和多层 Close，读者很难识别结果由谁持有。图片路径有客户端条件处理，普通 v2 却统一旁路，形成同包内两套行为。
- **结论**：最终字节上的条件/范围语义保留；需要明确实现边界，不能用图片局部验证替普通缓存验收。

### R17 指标、文档与性能验证

- **目的**：能观察源命中、成品命中、转换、旁路、合并及回收；避免任意 URL/头值标签导致高基数。
- **改法与动机**：新增固定维度指标、Grafana 查询、维护说明和定向基准，用于判断各阶段工作是否生效。
- **主要位置**：[pkg/metrics](../pkg/metrics/)、[metrics.go](../server/middleware/caching/metrics.go)、[观测说明](representation-observability.md)。
- **偏离**：功能计数、固定图片样本和若干分配指标不能证明大文件首包、按需回源、磁盘命中操作数和启动时间没有退化；文档却使用了整体完成表述。
- **结论**：保留观测能力；需要改造前后相同负载对照，报告具体差值和缺失证据。当前性能和文档交付未通过。

### R18 旧数据回收、屏障压缩与安全请求合并

- **目的**：旧 Vary 数据逐步清理；失效历史可维护；同资源的等价冷请求降低源站压力，不能串响应。
- **改法与动机**：按预算扫描、退休和删除；仅删除被更大目录屏障覆盖的历史；新 response_sharing 在响应和 EOF 验证后共享有界完整缓冲。
- **主要位置**：[legacy_gc.go](../storage/legacy_gc.go)、[compaction.go](../storage/representation/compaction.go)、[response_sharing.go](../server/middleware/caching/response_sharing.go)、[proxy.go](../proxy/proxy.go)。
- **偏离**：旧合并开关实际失效，新共享依赖 v2 且不覆盖 Range/缺块/过期刷新；候选还要求完整请求头相同并拒绝指定 peer。可共享的小响应在返回前先缓冲完整 Body，增加首包等待。
- **结论**：禁止错误复用有必要；防击穿能力和延迟代价不能长期省略。全头等价是保守实现选择，不是业务必然要求，也不能未经证明就只按 Vary 放宽。回收/合并需分别验收，当前未通过。

### R19 纯 memory v2 扩展

- **目的**：纯内存 Bucket 可以使用 Vary/转换能力，重启自然清空，不要求磁盘控制目录。
- **改法与动机**：声明易失能力，使用进程内控制记录和新 incarnation，避免把内存缓存伪装为持久存储。
- **主要位置**：[storage/representation.go](../storage/representation.go)、[memory/representation.go](../storage/bucket/memory/representation.go)、[representation_memory_test.go](../storage/representation_memory_test.go)。
- **偏离**：memory/disk 混合被直接拒绝，未覆盖常见内存热点层加磁盘容量层。
- **证据与结论**：E06 的现有测试确认“拒绝混合”正是当前实现规则。纯 memory 的局部接入不代表分层兼容；未通过。

### R20 磁盘默认控制目录

- **目的**：减少额外配置，省略 state_directory 时复用 Bucket 下的默认目录，并保持已有失效历史。
- **改法与动机**：从已有绑定中选择唯一目录，首次按规范路径稳定选取，拒绝歧义或丢失绑定。
- **主要位置**：[storage/representation.go](../storage/representation.go)、[binding.go](../storage/representation/binding.go)、[默认目录测试](../storage/representation_default_test.go)。
- **结论**：默认值和既有历史识别可保留；目录简化不能代替拓扑变更、恢复和扩容流程。本轮没有重跑该全部矩阵，不据历史结果通过系列验收。

### R21 普通缓存批量写回、数据库与依赖变化

- **目的**：降低普通元数据更新的单次写入和同步成本，保留可靠失效、正常关闭排空及读取可见性。
- **改法与动机**：当前 Pebble 队列合并同键更新，默认每秒写回/同步 WAL；达到 1024 键或约 4 MiB 提前提交 NoSync；同步屏障先排空此前待写状态。读取与扫描包含待写层。
- **主要位置**：[pebble.go](../storage/indexdb/pebble/pebble.go)、[writeback.go](../storage/indexdb/pebble/writeback.go)、[durability.go](../storage/representation/durability.go)、[go.mod](../go.mod)。
- **关键澄清**：“Set 被调用”“写入 WAL/批次”“请求逐次等待 fsync”是三个不同问题。当前不能笼统描述为“全部数据库直写”；早期严格提交把同步要求扩大到普通缓存，当前写回又扩大了 IndexDB 的实现改动范围。
- **结论**：本轮没有逐项复验所有依赖升级、跨后端一致性和吞吐；这些变化不能与 Vary 正确性一起默认为获准。保留原写入契约、显式说明损失窗口/错误传播/关闭语义，并单独验收。当前未通过。

### R22 缓存身份不随源站路由变化

- **目的**：同一公开 URL 切源、主备或负载均衡后继续复用缓存。
- **原改法与动机**：曾自动把 upstream 配置指纹、指定地址和 context peer 纳入隔离；动机是隔离不同来源，却错误把路由节点视为内容身份。
- **本轮修正**：删除自动地址/配置绑定，保留公开 URL、显式 namespace、isolation_scope 和响应 Vary。
- **主要位置**：[representation_origin.go](../server/middleware/caching/representation_origin.go)、[server.go](../server/server.go)、[源切换回归](../server/representation_origin_switch_test.go)。
- **证据与结论**：E02 复现原 7 个失败；E04/E05 是修正前的诊断对照；E03 确认实际移除地址绑定后 GET/HEAD 和 suffix Range 恢复，E08 记录相应定向回归。该问题已局部修正，不代表其他 5 个失败或系列验收已解决。

### R23 原有 CDN 能力完整承接

- **目的**：大改流程后仍保留预取、错误码缓存、分片补齐、渐进写入和完整性校验通知等既有能力。
- **改法与动机**：v2 用单独 lookup/proxy/writer 简化新格式接入；旧 Processor 和 writeSession 继续服务旧格式。
- **实际结果**：独立路径未建立完整的能力映射；X-Prefetch、fill_range_percent、错误码开关被绕过，v2 发布也没有接回旧 CacheCompleted 事件通知。
- **主要位置**：[caching.go](../server/middleware/caching/caching.go)、[processor.go](../server/middleware/caching/processor.go)、[write_session.go](../server/middleware/caching/write_session.go)、[representation_writer.go](../server/middleware/caching/representation_writer.go)、[verifier](../plugin/verifier/verifier.go)。
- **证据与结论**：预取/小 Range 由 E06 复现，错误码由 E02/E03 复现；通知缺失是调用链静态确认，未发送真实外部校验请求。能力兼容未通过，不能要求原用例适应功能缺失。

### R24 可阅读性、可维护性与验收纪律

- **目的**：流程可大改，但维护者能够直接识别领域、调用顺序、状态和资源释放责任。
- **改法与动机**：在同一个 package 中增加 representation、managed、legacy、task 等文件前缀，沿用 Caching 和 Metadata 便于渐进接入。
- **实际结果**：文件前缀未形成真实边界；HTTP、图片任务、并发门控和存储发布交错；同一状态对象跨路径含义变化，关闭责任隐含在包装器中。
- **结论**：不能只重命名或补注释。应先确定普通缓存、图片转换和存储协调的职责与稳定契约，再建立清晰状态所有者。原有业务测试失败时先查回归，不以“测试夹具不适配”维护新设计。本项未通过。

## 4. 阶段与提交追踪

以下是来源映射，不是阶段重新验收结果。所有阶段仍受整体验收拒收结论约束。

| 原阶段 | 提交 | 对应需求及当前判断 |
| --- | --- | --- |
| P1.1/P1.2 | 93119b8 | R01/R02；Vary 纠错保留，旁路范围需要拆分 |
| P1.3 | f6eb9c9 | R03；索引与响应分离保留，类型边界需重整 |
| P1.4 | 603cadb | R04；200/304 和预算机制不能替代 Range 重验证 |
| P1.5 | 42a506f | 阶段测试补充；不能覆盖本轮业务兼容与性能缺口 |
| P2.1 | b010749 | R05；固定写入目标保留，填充策略不合格 |
| P2.2 | 1ea5f5f | R06；早期严格同步提交超出普通缓存需要 |
| P2.3 | 3fc6e30 | R07；失效语义保留，刷新容量未解决 |
| P2.4 | 53b2c13 | R08；集合生命周期机制不能替代实际分层/扩容 |
| P2.5 | 34f0620 | R09/R10；接口注入、停写保护保留，业务承接不完整 |
| P2.6 | 9544aea | R11；公开开关不代表可以交付 |
| P3.1 | c84209d | R12；转换契约与普通缓存需明确边界 |
| P3.2 | e989a2b | R13/R14；源与成品复用有必要，维护性未合格 |
| P3.3 | 39d940c | R15；worker 契约不能代替当前实际部署验证 |
| P3.4 | bd1465a | R16；成品 HTTP 行为有专用实现，普通缓存仍有缺口 |
| P4.1 | 2c21a5f | R17；指标和历史基准不证明无性能退化 |
| P4.2 | 3011a8a | R18；维护预算不能变成不可回收刷新总量限制 |
| P4.3 | 1d84956 | R18；部分冷 GET 共享不等于防击穿恢复 |
| 后续修正 | 4862788 | Range 证明与回滚边界修正，未消除当前缺陷 |
| 本地未提交扩展 | 无独立提交身份 | R19—R21，包括 memory、默认目录、写回、摘要与恢复 |
| 本次会话局部修正 | 无独立提交身份 | R22；移除自动源站绑定，未修复其余问题 |

## 5. 已确认偏差与详细处置结论

此表列出当前问题及需要满足的结果，不授权立即执行所有重构。

| 编号 | 当前偏差与证据 | 业务影响 | 详细结论 |
| --- | --- | --- | --- |
| D01 | v2 发现已有对象缺块时删除 Range；E06 有强 ETag 时仍重建全体 | 大文件随机读取和拖动放大回源，增加读取前缀和尾部开销 | 以同一表示/强验证器证明后按需补块；全量替换只作为确有依据的回退，不作为正常路径 |
| D02 | 未形成完整块的小 Range 不发布；取消时丢弃全部未发布块；E06 | 重复小范围和常见下载中断无法渐进预热 | 补齐配置必须有效；已确认分片的可读状态、版本证明和生命周期需独立于整文件完成 |
| D03 | 带 Range 的过期请求不进入条件验证；E03/E04 | 无变化资源也全量回源；原 304/Range 用例失败 | 将源表示验证与客户端范围拆开，验证成功后按原 Range 返回缓存 |
| D04 | durable LookupRead 预先打开/Stat 所有已缓存分片；E07 | 1 字节请求承担全对象分片检查；无关尾块缺失影响已存在首块；持锁文件操作增加同分片锁竞争 | 命中关键路径的工作量应与所需范围对应；完整性恢复/巡检另行设计 |
| D05 | 新鲜条件请求和 no-cache/max-age=0 完全旁路；E06 | 协商请求打源，刷新结果不更新既有缓存 | 分离禁止存储、强制验证、客户端条件和请求身份限制，不能用单个 bypass 标志替代 |
| D06 | v2 响应形状仅允许 200/206；E02/E03 | 显式负缓存失效，错误热点持续打源；显式零长度 200 也被排除 | 承接已存在的错误码与 TTL 规则；零长度等响应明确策略，不能只因写入器需要块而否定整个响应 |
| D07 | v2 跳过 Prefetch/FillRange；无 CacheCompleted 发布调用；E06 与静态调用链 | 预取失效、配置失效、磁盘完整性校验插件缺少触发 | 逐个列出旧处理器/事件与新路径映射，恢复业务结果；不得宣称配置仍有效 |
| D08 | 精确 URL 和目录屏障共用 4096 条/1 MiB 预算，独立 URL 无法被压缩；E06 | 日常连续刷新累计后拒绝新任务，原始插件映射为 503 | 保留失效正确性，重做可持续的历史回收/代际方案；提高数字不能单独解决 |
| D09 | 构造 Manager 前台扫描全部元数据和文件，存在摘要时完整读文件计算 SHA-256；静态确认 | 重启/升级准备工作随缓存总量增加，不能用 async_load 宣称已异步 | 必须定义恢复可用性、校验时机与工作预算；缺少真实规模测量，不能给出耗时倍数 |
| D10 | memory/disk 混合拒绝，固定拓扑变更拒绝，自动分层未完整接入；E06 与静态确认 | 内存热点层、换盘、扩容和缩容受限 | 将控制所有权保护与数据拓扑运维分别设计，提供可执行迁移/退化路径 |
| D11 | 旧合并开关不再生效，新共享仅适用部分完整冷 GET，且对可共享响应先完整缓冲；静态确认 | Range/刷新热点缺少合并，带不同追踪头可能不能同组，小响应首包也等待缓冲 | 共享正确性与防击穿能力一起验收；明确兼容开关、延迟代价和范围证明 |
| D12 | 生命周期隐藏在多层 Body、defer 和取消回调，普通任务固定 2 分钟；静态确认 | 长下载策略不明确，维护者难证明取消、停机与资源释放顺序 | 建立稳定的请求、填充、读取和后台任务所有者；超时变成独立业务策略 |
| D13 | 曾把 upstream 配置和 peer 自动作为缓存身份；E01/E02，已局部修正 | 切源丢命中 | 保持当前修正，重新验收头覆盖、context peer、负载均衡和持久重开，不倒退到地址绑定 |
| D14 | 曾把普通缓存纳入严格同步发布；当前为批量写回；静态确认 | 历史设计可能增加写入等待；当前仍需查队列、阈值提交与锁持有成本 | 清楚记录历史与当前差别，不将普通缓存变成逐条同步；写回和依赖变化独立验收 |

D01/D09/D11 指出可确定的全量读取或缓冲行为；本文没有生产 TTFB、P95、磁盘吞吐的对照数据。这里的退化风险不能虚构为已测得的具体百分比。

## 6. 原有用例的实际结果

### 6.1 执行阶段

| 阶段 | 结果 | 解释 |
| --- | --- | --- |
| 最初受限执行 | 6 包创建 httptest listener 失败 | 属于执行环境限制，未执行到功能断言；不能计作功能结果 |
| E01：连接已有服务 | 18 个顶层测试，11 通过、7 失败；6 包中 5 包失败 | 实际请求已执行，之后用当前源码独立实例复核 |
| E02：当前源码独立实例、仅重跑失败项 | 7 个失败均复现 | 排除既有服务二进制或缓存残留是主要原因 |
| E04：临时固定每个 URL 的上游地址 | GET/HEAD 与 suffix Range 通过；其余仍有失败 | 诊断实验，不是对原用例的修复或验收 |
| E05：固定上游，再使用合法 ETag/完整块预热 | 短文件测试通过；两个 Range 重验证仍失败 | 辨别块边界、验证器与 Range 排除条件，不修改原断言 |
| E03：修正源站身份后，原 all-features 全范围复验 | 18 个顶层测试，13 通过、5 失败；6 包中 4 包失败 | 仅通过临时覆盖连接独立 socket；未固定上游端口、未更改业务断言 |

### 6.2 七个失败测试逐项追踪

| 测试 | 身份修正前 | 身份修正后 | 当前结论 |
| --- | --- | --- | --- |
| TestCacheMethodAllowGETorHEAD | 失败 | 通过 | 自动地址隔离导致，已局部修正 |
| TestSuffixRangeHitUsesCachedObjectSize | 失败 | 通过 | 同上 |
| TestRangeOffset | 失败 | 仍失败 | 当前按全量重建产生 REVALIDATE_MISS/HIT，不符合原 PART_MISS/PART_HIT 路径；不能仅改状态断言掩盖回源放大 |
| TestRevalidateDoubleSkip | 失败 | 仍失败 | Range 不发送预期条件头；合法 ETag 对照仍失败 |
| TestRangeExpiredRefresh | 失败 | 仍失败 | 源未变化的 Range 重验证未接通 |
| TestErrCodeCache | 失败 | 仍失败 | 显式错误码缓存未承接 |
| TestContentLenShorter | 失败 | 仍失败 | 未对齐范围未积累可复用完整块；改完整块只是诊断对照，不是整改 |

TestContengLenChanged、TestETagChange、TestLastModified 等通过，只证明该次断言中的响应头与字节相符。切源身份修正前，各子用例可能落到不同资源，不能据此证明旧缓存被正确检测并替换。应补充资源命中、已发布前置状态、回源范围及次数的断言。

## 7. 当前代码领域与生命周期

### 7.1 三条执行路径

| 路径 | 主要入口 | 主要状态 | 当前边界问题 |
| --- | --- | --- | --- |
| 原格式普通缓存 | ProcessorChain、doProxy 的旧分支 | Caching、writeSession、LegacyFillLease | 已受通用准入、合并停用和缺块证明修改影响 |
| v2 透明缓存 | lookupRepresentation、proxyRepresentation | Resource/VariantRef、representationWriter、Reservation、ReadLease | 另开路径导致原功能未完整承接 |
| 图片转换缓存 | managedProvider.roundTrip/resolve/source/convert | Plan、源图、成品、临时文件和读取保护 | 与普通 HTTP、存储协调在同一 package 交织 |

representation 的业务含义是“某 URL 的一种具体响应”；managed 实际指图片转换；legacy 指旧数据格式。当前以这些词作文件前缀，不能替代领域分层。

### 7.2 文件职责

| 文件或组 | 原本要解决的问题 | 应明确的边界 |
| --- | --- | --- |
| representation_config / caching_representation | 启动绑定、查找具体表示 | 配置与单次请求状态分离 |
| representation_origin | 回源、准入、验证、填充协调 | 当前职责过多，应明确 HTTP 决策和存储调用接口 |
| representation_freshness / representation_response | 新鲜度、响应形状和裁剪 | 规则与执行 I/O 分离；裁剪不能隐藏整文件读取 |
| representation_writer / representation_lease | 一次填充、一次缓存读取保护 | 写入状态与读取保护分开，不混用“lease” |
| managed_provider / managed_gate | 图片任务调度和排队复查 | 明确与透明请求合并的区别 |
| managed_source / managed_product | 源图复用与转换成品 | 不能把图片完整生成策略推广到透明文件 |
| managed_response / managed_purge | 最终图片 HTTP、URL 失效映射 | 结果描述、HTTP 发送和失效协调独立 |
| task / write_session / legacy_lease | 停机登记、旧写入隔离及旧格式失效保护 | 谁接收取消、谁可脱离客户端、谁最终释放必须明确 |
| response_sharing | 临时复用一个有界回源结果 | 共享缓冲不等于长期缓存，也不等于流式防击穿已恢复 |

### 7.3 状态归属

| 对象 | 创建者 | 生命周期与结束条件 |
| --- | --- | --- |
| 配置、Provider、共享组 | Middleware 初始化/绑定请求链 | 中间件实例期间持有；底层存储由 Storage 关闭 |
| Manager/Repository | Storage 初始化 | Storage.Close 停止任务和维护后关闭 |
| Caching | 每次 preCacheProcessor | 一次请求及其 Body 消费期间；不是全局缓存管理器 |
| IndexState | Capture | 填充前版本快照，不是可随意重读替换的当前版本 |
| Reservation | ReserveFor/ReserveReplacementFor | 占用一次填充预算，发布或失败后释放 |
| ReadLease | LookupRead 等读取入口 | 固定具体对象和位置，Body/结果关闭或取消后释放 |
| representationWriter | 响应准入之后 | Read 驱动写块；当前 EOF 发布，失败/提前 Close 撤销 |
| managedSource/managedResult | 图片任务 | 临时文件、成品读取保护必须随 close/Close 释放 |
| responseCall 与缓冲 | 一次共享回源 | 组完成后不再接新等待者；现有消费者释放后回收缓冲 |

### 7.4 请求和停机的实际结束点

1. 普通 GET/HEAD 在进入缓存时登记 task。RoundTrip 返回只说明响应可读取，不能说明写缓存或读取已经结束。
2. HIT 的 ReadLease 随 Body 关闭释放；MISS 的 writer 随读取推进，EOF 才尝试发布。
3. 旧写入队列有独立任务；图片任务可脱离客户端取消，但受转换预算、超时和存储 Drain 管理。
4. Storage.Drain 停止接收新任务、取消并等待活动任务，再等待 Bucket 文件句柄。
5. Storage.Close 之后停止 Manager，再关闭 Bucket、IndexDB 和 SharedKV。

现有实现用多层 Body、取消回调、defer 和 goroutine 协作实现这些步骤。需要整改的是让所有权和状态转换直接可读，而非再增加一层名称相近的包装。

## 8. 证据归档与复现边界

证据目录：[dist/v2-review-2026-11-02](dist/v2-review-2026-11-02/README.md)。归档的是本次会话已有记录，不是编写本文时新生成的验收结果。

| 编号 | 文件 | 证明范围 |
| --- | --- | --- |
| E01 | [all-features-initial.log](dist/v2-review-2026-11-02/all-features-initial.log) | 已有服务上的原 7 个失败 |
| E02 | [all-features-current-source.log](dist/v2-review-2026-11-02/all-features-current-source.log) | 当前源码独立实例复现 7 个失败 |
| E03 | [all-features-after-origin-fix.log](dist/v2-review-2026-11-02/all-features-after-origin-fix.log) | 仅修身份后仍有 5 个失败 |
| E04 | [stable-origin-control.log](dist/v2-review-2026-11-02/stable-origin-control.log) | 固定 peer 的诊断对照 |
| E05 | [fixture-controls.log](dist/v2-review-2026-11-02/fixture-controls.log) | 合法 ETag/完整块预热诊断对照 |
| E06 | [cdn-behavior-probes.log](dist/v2-review-2026-11-02/cdn-behavior-probes.log) | Range、预取、no-cache、刷新容量、取消/拓扑/混合限制 |
| E07 | [disk-lookup-probe.log](dist/v2-review-2026-11-02/disk-lookup-probe.log) | 单字节查找打开全部分片，无关块缺失影响命中 |
| E08 | [origin-switch-regressions.log](dist/v2-review-2026-11-02/origin-switch-regressions.log) | 源站身份局部修正的定向回归，日志仅含 package 汇总 |

诊断探针的 PASS 表示“确实观察到所检查的当前行为”，例如缺块全量回源；不能读成“该行为符合业务需求”。E06 的容量探针使用缩小预算；E07 使用 4 块对象和模拟尾块缺失，不能外推生产延迟数字。

原始日志保留测试临时路径。它们用于追溯，正文结论不依赖这些临时目录继续存在。新的探针片段和 overlay 复现步骤已归档；原有服务配置、不同历史时刻工作区与整个运行环境未做完整镜像，不能承诺逐字重现全部历史输出。

## 9. 偏离的原因与需要改变的做法

| 原因 | 具体表现 | 必须改变的做法 |
| --- | --- | --- |
| 目标漂移 | 从增加 Vary/图片能力扩大为重新定义普通缓存核心 | 先建立原有能力到新入口的映射，每个业务目标有前后行为和证据 |
| 把正确性简化成禁用 | 不续写、不合并、不处理条件、拒绝拓扑变化 | 给出满足正确性的业务实现；确需限制则单独说明影响并对齐需求 |
| 普通文件与图片转换混用策略 | 把成品完整发布要求扩展到普通文件填充 | 按领域确定流式和完整生成策略，不因共用模型共用全部规则 |
| 验证围绕实现闭环 | 以“取消不能发布前缀”等新规则自证通过 | 原用例先按回归分析，断言修改须有业务依据 |
| 缺少性能对照 | 只有计数/正确性/小样本基准，却宣称整体完成 | 记录 TTFB、回源放大、文件操作、内存、启动时间等前后对照 |
| 责任隐含在包装中 | 多种 lease、Body 和后台任务互相套用 | 对配置、请求、填充、读取和后台任务分别声明所有者与结束点 |
| 文档替实现合理化 | 阶段勾选和通过记录掩盖业务缺口 | 历史证据保留，但当前状态必须显示拒收和未验证项 |

## 10. 后续整改的验收门槛

以下是结果要求；实现方式需要逐批审查，不能把此列表理解成已完成。

| 验收项 | 必须提供的证据 |
| --- | --- |
| 原有用例 | all-features 原业务断言恢复；任何用例修正说明原缺陷和需求依据 |
| Range/补块 | 有效版本证明下只取所需块；小 Range、suffix、open range、交叉范围、缺块和源变化分别验证 |
| 中断与按需缓存 | 客户端中断后的完整块可复用；取消、断流和新旧版本竞争不混块、不复活 |
| 重验证 | 普通 GET、HEAD、Range 和条件请求分别验证缓存/源站交互与返回字节 |
| 负缓存与预取 | 显式开关、TTL、X-Prefetch、补齐比例在新旧路径结果明确 |
| 防击穿 | 冷热点、刷新热点、Range 缺块分别记录并发回源次数、等待者取消及内容正确性 |
| 首包与 I/O | 同负载比较改造前后 TTFB、回源字节、分片打开次数、磁盘操作和内存；禁止无证据收益承诺 |
| 刷新可持续性 | 连续不同 URL/目录刷新、控制记录回收和长时间运行；预算耗尽恢复流程可执行 |
| 存储运行 | memory/disk 分层、扩缩容、换盘、故障盘、重启、SIGUSR2 交接边界清楚且有验证 |
| 数据库 | 普通写入、同步屏障、失败重试和关闭排空分别验证；依赖/后端变化单独列出 |
| 插件 | v2 磁盘缓存完成事件能触发原校验链路；外部服务调用仅在授权环境验证 |
| 可维护性 | 能从入口定位领域；每种状态只有明确语义；创建、发布、取消、关闭责任可直接审查 |
| 报告 | 仅把实际执行且对应需求的证据写成通过；缺少项明确未验证，不以阶段记录替代最终验收 |

实现阶段仍遵守仓库最小范围验证策略：每批优先运行受影响 package 的定向测试；并发变更才增加相应 race；跨包接口按影响扩大。阶段交付还必须覆盖相关原业务用例，最终业务验收包含用户明确要求的 all-features 范围。本文编辑仅做文档与证据一致性检查。

## 11. 最终评论

**验收均不通过。**

可保留的需求包括正确 Vary 选择、固定写入目标、可靠失效、源图与成品复用、状态独立和可观测性。当前实现中的整文件替换、过度旁路、全对象检查、不可持续刷新容量、分层/拓扑限制，以及不清楚的领域和生命周期，不能作为这些需求的必然代价。

源站身份绑定的局部修正、普通缓存批量写回的局部调整、以及任何内部测试通过，都不改变本次整个 v2 系列被拒收的结论。
