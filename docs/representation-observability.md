# Vary 与图片转换观测

P4.1 为新表示协调器和托管图片路径提供 Prometheus 指标，并在 [现有 Grafana Dashboard](Grafana-Prometheus-Dashboard.json) 中增加 12 个面板。沿用原 Dashboard 的数据源与 instance 变量；导入时按部署选择 Prometheus 数据源。此文件是可导入配置，未假定某个在线 Grafana 已部署。

默认采集端点仍为 `/metrics`，访问权限沿用内部接口配置。完整启用与回滚分别见 [v2 操作说明](vary-representation-operations.md) 和 [图片操作说明](managed-image-operations.md)。没有启用或尚未执行某种操作时，相应计数序列可能不存在；这不表示操作失败。

## 指标与口径

全部新指标以 `tr_tavern_` 开头。标签只允许固定枚举，不包含 URL、Cookie、Accept 原文、选择指纹、完整规格、动态 policy/encoder_version 或 worker 地址。未知调用值归为 other，不产生任意新标签。

| 指标后缀 | 标签 | 口径 |
| --- | --- | --- |
| vary_lookup_total | result、reason | v2 选择决定及明确的旧格式旁路；不统计内部读取 pin/文件验证的重复查找 |
| vary_admission_total | result、reason | 源 200/206 响应的入场决定；admitted 不代表写入完成；旧格式无 Vary 入场标为 reason=legacy |
| representation_commit_total | result | paired Publish 的 published/superseded/failed；含 304、源检查元数据更新，不等于新 Body 数 |
| representation_pending | kind | origin/derived 尚未 Release 的预留数；是 lease 数，不是不同 selector 数，也不是读取 pin 数 |
| representation_invalidate_total | mode、scope、result | manager 的 soft/hard、file/dir 屏障调用结果；包含参数映射后的源屏障，不等于客户端 PURGE 次数 |
| source_fetch_total | result | 托管源解析结果：hit=新鲜元数据；revalidate=可复用的 304；miss=完整获取/200 换代；failed=源路径失败 |
| transform_total | backend、source_mime、target_mime、result | 真正调用转换接口的尝试，经输出验证与发布后确定结果；不统计成品命中或启动/独立契约探测 |
| transform_duration_seconds | backend、result | 从调用后端到输出验证/发布结束的直方图；不含源获取，也不包含启动探测；含后端预算等待 |
| transform_queue_depth | backend、stage | 当前预算等待数；stage=task 是整个源/成品任务，worker 是 RPC/输出预算 |
| transform_active | backend、stage | 当前占用许可；私有响应和 worker 输出在 Close 前继续占用 |
| transform_queue_rejected_total | backend、stage、reason | 排队预算的 full/timeout/cancelled；每一层独立计数 |
| representation_gc_total | result | collected=删除候选，skipped=可达/被保护/控制或旧记录，failed=扫描或删除错误；这是决定事件数 |

lookup.result 为 hit/miss/bypass，reason 包括 matched、not_found、stale、range、request_policy、conditions、body、policy、control_error、legacy。admission.result 为 admitted/bypass，reason 包括 eligible、wildcard、invalid_vary、field_budget、selector_budget、policy、shape、control_error、legacy。完整条件验证经 paired Publish 计数，304 不作为一次新 Body 入场。

转换 backend 固定为 http 或 other，MIME 固定为 image/jpeg、image/png、image/webp 或 other；result 为 success、failed、timeout、cancelled、superseded。后端调用成功但输出验证或持久发布失败仍计为失败；成功生成但被新屏障拒绝的结果计为 superseded。由于记录的是接口尝试，RPC 排队拒绝也会计入该次失败，不能把全部尝试数直接等同于编码服务成功数。

`cache_requests_total` 已包含托管路径，store_type=managed 表示该逻辑管线，而非物理 bucket 类型。私有原样/回退是 BYPASS；命中的最终成品是 HIT。

## 面板与查询

新增分组为 “Vary representations and managed images”，覆盖选择、入场、提交、预留、失效、源复用、转换次数/耗时、排队/活跃、拒绝以及 GC。

选择与入场是不同阶段，应分开观察，避免把同一 MISS 的后续不入场再算成一次查找：

```promql
sum by (result, reason) (rate(tr_tavern_vary_lookup_total[5m]))
sum by (result, reason) (rate(tr_tavern_vary_admission_total[5m]))
```

只分析新表示资格时筛选 admission.reason=eligible；reason=legacy 的 admitted 是普通旧格式无 Vary 缓存，不表示 v2 发布。

成功转换的 P95，注意直方图的累积桶要保留 le：

```promql
histogram_quantile(0.95,
  sum by (le, backend) (
    rate(tr_tavern_transform_duration_seconds_bucket{result="success"}[5m])
  )
)
```

预算等待与实际驻留分开看：

```promql
sum by (backend, stage) (tr_tavern_transform_queue_depth)
sum by (backend, stage) (tr_tavern_transform_active)
sum by (backend, stage, reason) (rate(tr_tavern_transform_queue_rejected_total[5m]))
```

任务已结束且响应都已关闭后，queue_depth、active 和 representation_pending 应回到基线。单个客户端取消不会撤销仍被其他等待者使用的已接收任务，应等任务实际结束后判断是否归零。进程内多实例指标聚合到同一 backend/stage；不要对 gauge 使用 rate。启动探测会短暂占用 worker 许可，但不增加转换次数和耗时样本。

## 运行判断

- wildcard/invalid_vary/field_budget/selector_budget 旁路增长：核对源站实际选择字段、快照预算和 vary_limit；不要通过忽略字段恢复错误复用。
- published 正常但 source miss/transform success 持续很高：检查源 TTL、主动 PURGE、规格/版本变化与最终命中率。对相同规格重复命中不应继续增加转换次数。
- superseded 增长：核对失效、迁移与源换代频率，表示迟到结果被拒绝，不代表旧结果被成功写回。
- failed 提交或失效增长：结合固定 ID 日志检查控制存储、数据目录、同步写入与拓扑；不能仅通过关闭同步掩盖失败。
- task 队列先上升：源获取、解码、发布或私有响应消费可能限制吞吐；worker 队列/超时上升时再核对后端能力和并发。取消与队列满分别观察，不能都归为编码错误。
- GC skipped 较高：可达对象、读取 pin、在途写入和受保护记录都会跳过；先看磁盘增长和 failed，不把 skipped 本身设为错误告警。

阈值需用固定图片、流量分布和节点资源校准。本阶段不提供性能倍数、吞吐保证或通用告警阈值；后续故障演练和基准单独记录。

维护任务另提供 `tr_tavern_representation_barrier_compaction_total{result="removed|retained|failed"}`、`tr_tavern_legacy_vary_gc_total{result="collected|skipped|failed"}` 和 `tr_tavern_legacy_vary_gc_file_ops_total`。前者计候选及失败轮次，后者分别计旧格式对象决定和最终/临时文件删除尝试；不将目录扫描、元数据同步或不存在文件解释为已释放字节数。配置及故障演练见 [维护说明](representation-maintenance.md)。

P4.3 可选响应合并单独提供 `tr_tavern_response_sharing_total{result}`、`tr_tavern_response_sharing_active` 和 `tr_tavern_response_sharing_waiters`。前者每次调用一个固定结果，active 包含已完成但尚有未关闭 Body 的缓冲组，waiters 只计等待共享证明/字节的请求。默认关闭，范围与首字节延迟取舍见 [响应合并说明](response-sharing-operations.md)。

新的入场调试日志只记录 resource_id、固定结果/原因和合法字段名；转换调试日志记录 resource_id、有限规格指纹与结果，不打印选择值。原有访问日志沿用 access_log 配置，指标与这些日志各自保持其用途。

## 验证

定向测试验证：任意 URL/请求值不能扩展指标标签；相同成品命中不增加转换次数；失败及迟到发布释放预留并分类计数；排队满和取消后 queue/active 归零。受影响存储、图片与服务器并发测试继续通过。

Dashboard JSON 检查唯一面板 ID、网格范围以及新查询引用的指标名。仓库验证不需要连接运行中的 Prometheus/Grafana；上线后仍应使用本部署采集目标确认数据源与 instance 选择。
