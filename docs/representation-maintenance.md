# 表示缓存维护与故障演练

> **验收状态（2026-11-02）：整个 v2 系列验收均不通过。** 本文记录已有实现与操作方式，不构成当前发布通过结论；业务缺口、性能及维护性问题见 [v2 变更审查结论](v2-change-requirements-review.md)。

日期：2026-10-30。对应实施方案 P4.2；公开持久化模式为 disk/Pebble。透明 v2 与托管图片的读取开关仍默认关闭，可选透明响应合并见 [P4.3 说明](response-sharing-operations.md)。

## 旧 Vary 回收

v2 使用独立物理 ID，查询不会回退到旧 VirtualKey。普通无 Vary 缓存继续使用原路径；旧 Vary 数据不再用于命中。已有 storage.representation 绑定可选择开启限速回收：

```yaml
storage:
  representation:
    state_directory: /cache-control/tavern-v2
    gc_interval: 1m
    gc_budget: 64
    legacy_vary_gc_budget: 32
    legacy_vary_gc_file_ops: 128
```

`legacy_vary_gc_budget` 默认 0，关闭旧格式回收；开启范围 1—128，为每轮最多扫描的实际元数据数。`legacy_vary_gc_file_ops` 默认为 128，范围 2—1024，为最终文件及匹配临时文件的删除尝试总上限，不存在文件的删除尝试也计数。每个块保留至少两个操作的剩余预算才开始处理，因此一轮不一定耗尽预算。扫描游标在进程内轮换，部分删除进度同步写入原记录，重启从头扫描也可继续。

定时维护依次执行 v2 孤儿回收、屏障压缩和可选旧 Vary 回收，共用本轮 gc_interval 期限。周期应容纳实际磁盘同步时间，过短周期可能只得到超时和保留日志；不会将超时解释为删除完成。建议从默认一分钟开始，结合失败指标和磁盘压力调整。

回收先在存储所有者的排他保护下，确认相同规范 URL 没有未关闭的旧格式读写租约；失效但尚未关闭的租约也阻止删除。随后同步撤销所有 bucket 内该 URL 的旧根引用，再按实际 ID 删除。VirtualKey 字符串仅用于识别旧格式，从不作为跟随删除其他 ID 的指令。旧 Vary 对象禁止进入旧的单对象迁移路径；v2 对象继续由集合迁移及读取、写入、迁移 pin 保护。

旧 Vary 的磁盘回收先同步退休标记及引用，再删除 Chunks/Parts 所登记的最终文件和同一块名下的 `.tmp*` 文件，之后同步剩余位图。任何同步、删除、取消或元数据删除失败都保留可重试记录；控制失败之前不删除字节。普通缓存、保留的 v2/control ID、未知记录种类、未知标志及不匹配的索引身份不在旧 Vary 回收范围内。公开 v2 的普通退休索引采用批量写回，PURGE 屏障仍同步确认；丢失普通退休快照时，缺失/损坏的旧文件只会导致 MISS。启动阶段另以有界目录分页清理无元数据或位图归属的孤立文件和临时文件；在线恢复不执行该清理，无需通过文件年龄推断安全性。

同步维护接口为存储的可选 `LegacyVaryCollector.CollectLegacyVary(ctx, records, files)`，native、migrator 和 diraware 均提供；通过同一存储任务/句柄生命周期停写。定时工作由 Manager 等待结束后，才允许关闭底层数据库。

## 屏障压缩

每轮最多检查 min(gc_budget, 128) 个候选，轮换控制 home。只有同 scheme、host、转义路径祖先目录同时覆盖某条记录的 hard 和 soft epoch，才删除该被覆盖条目。目录祖先必须严格更短；确切 URL 可被覆盖其全部查询参数的同路径目录取代。目录边界继续采用规范尾斜杠，不把 `%2F` 当作目录分隔符。

压缩保持每个可能 URL 的最大 hard/soft epoch、catalog incarnation 和根/票据的比较语义，覆盖冷命名空间及无法枚举的在途票据。控制记录同步成功才确认压缩；年龄不构成删除依据。不被其他目录同时覆盖的历史始终保留。

压缩不保证能释放任意已满的屏障目录。未覆盖历史达到 4096 条或字节预算后，新增失效仍返回预算错误，不能为了报告 PURGE 成功而丢掉历史。需要重建时，必须停写并使用新的空控制和数据目录完成整个缓存重建；不能只删除 catalog、根或拓扑 manifest。

## 故障验证

本地定向检查使用临时目录、实际磁盘/Pebble、httptest worker 和独立子进程，不依赖部署中的服务。

| 故障/交错 | 可观察结果及测试 |
| --- | --- |
| 已通过契约的 worker 随后停止 | 两次转换均 no-store 502，不冒充目标图片；允许的原格式仍正确响应。`TestManagedProviderHTTPWorkerCompletePublication/worker-disappears-after-verification` |
| 数据写返回 ENOSPC、home 读取 ENODEV、控制写 EIO | 托管响应明确失败，临时文件清理；恢复后同规格返回可解码 PNG。`TestManagedStorageFaultsFailClosedAndRetry` |
| 透明查询 home 失败 | 保留实际 origin 的状态、MIME 和字节。`TestTransparentHomeFailurePreservesOriginResponse` |
| 旧格式读写在途、跨 bucket 根和伪 VirtualKey | 活跃租约期间零删除，释放后按预算推进，普通对象完整保留。`TestLegacyCollectionBoundedLeaseProtectionAndExactIDs` |
| 回收日志同步、物理删除或最终索引删除失败 | 不提前删除，失败可恢复；临时文件也受预算限制。`TestReclaimDiskFullAndDeleteFailureRemainRetryable`、`TestDiskCleanupBoundsTemporaryFilesAndRetriesFailure` |
| 回收部分完成后关闭重开 | 保留撤销引用和进度，定时任务继续清理；默认关闭时经过 tick 仍保留数据。`TestLegacyCollectionScheduledOptInAndRestartProgress` |
| 压缩与填充交错、控制同步失败、磁盘重开 | 当前票据仍可提交，旧票据不能复活，最大 epoch 和 incarnation 不变。`TestBarrierCompactionPreservesFreshRootAndInFlightTicket`、`TestBarrierCompactionSyncFailureAndDiskRestart` |
| 无 Close 的进程退出、文件丢失 | 已确认屏障保留，不根据孤立元数据制造可读 Body。`TestDurableCrashRecoveryAndMissingFiles` |
| 迁移失败、读取 pin、迟到填充 | 源集合保留或原子切换；旧位置在最后租约释放后回收。`TestManagerCollectionMigrationAndReaderPins`、`TestManagerMigrationFailuresLeaveSourceAndCollectTarget`、`TestManagerSupersededFillKeepsIndependentWriterPin` |
| 活跃填充时真实 SIGUSR2、关闭失败 | 旧写入停止并释放数据库后才启动新写入者；失败不启动。`TestLiveSIGUSR2DrainsFillBeforeNewWriter`、`TestHandoffRequiresDrainAndCloseBeforeStartingWriter` |

## 固定样本基准

命令：`GOTOOLCHAIN=go1.26.0 go test ./server/middleware/caching -run '^$' -bench '^BenchmarkManagedRepresentation$' -benchtime=20x -count=1 -benchmem`。

环境：darwin/arm64、Apple M1 Pro、Go 1.26.0，benchmark 并行度后缀 -8。样本固定 8×8 JPEG，真实 Go PNG 编码，memory bucket/Pebble-vfs；所有模式先暖源及一个成品。下表为一次 20 轮测量，回源/转换次数包含暖场，控制 I/O 与分配数仅计测量段。

| 模式 | ns/op | 回源数 | 转换数 | 成品命中率 | 控制读/op | 控制写/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| hot_product | 183788 | 1 | 1 | 1.000 | 6 | 0 | 103907 | 1240 |
| rebuild_product_with_maintenance | 1506173 | 1 | 21 | 0 | 27 | 2 | 1591136 | 5198 |

第二种模式每轮显式退休成品，再转换并执行有界孤儿回收，包含维护成本；并非纯编码耗时。控制读写是调用次数，不等于设备 IOPS；B/op 是累计分配，不是常驻或峰值内存。这组结果只建立可重复的流程基线，不提供远程 worker、生产磁盘、真实尺寸图片的性能倍数或发布阈值。生产阈值需固定实际样本分布、并发、设备及后端版本后另行测量。
