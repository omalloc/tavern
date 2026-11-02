# 透明 v2 缓存的启用与回滚

> **验收状态（2026-11-02）：整个 v2 系列验收均不通过。** 本文记录已有实现与操作方式，不构成当前发布通过结论；业务缺口、性能及维护性问题见 [v2 变更审查结论](v2-change-requirements-review.md)。

P2 阶段已接通透明 Vary 缓存。默认仍使用旧格式的普通无 Vary 缓存；旧 Vary 数据继续旁路。托管图片可按 [图片操作说明](managed-image-operations.md) 独立灰度启用，默认关闭；透明冷 GET 可单独启用 [经过验证的响应合并](response-sharing-operations.md)，在线重分片仍未开放。

## 启用

使用独立实例或部署配置做小流量灰度；当前没有按主机/路径动态分流或在线切换。先保持既有存储配置，在下面两个位置添加绑定：

```yaml
storage:
  # 原有 driver/db_type/buckets 等配置继续保留。
  # 公开持久化 v2 仅支持 native/disk + Pebble，所有参与的 bucket 都须满足。
  representation:
    # state_directory: /cache-control/tavern-v2 # 可选；默认使用 bucket 根目录下的 .tavern-v2
    gc_interval: 1m
    gc_budget: 64

server:
  middleware:
    # recovery、请求 rewrite、multirange 等中间件放在 caching 之前。
    - name: caching
      required: true
      options:
        include_query_in_cache_key: true
        vary_limit: 100
        representation:
          enabled: false # 首次灰度时改为 true，示例保持关闭。
          namespace: http-v2
          isolation_scope: origin
          max_vary_fields: 32
          max_selector_bytes: 16384
```

磁盘模式允许 `storage.representation: {}`：默认控制目录为 `<bucket 根目录>/.tavern-v2/`。例如 bucket.path 为 `/Volumes/cache1` 时，默认是 `/Volumes/cache1/.tavern-v2/`。显式 `state_directory` 优先；控制目录必须与数据根目录、索引目录区分，并持久保留。它保存拓扑绑定并持有排他所有权锁；数据与索引目录同样有写入所有权锁。不要删除锁文件来绕开另一个活跃进程。进程正常关闭或退出后 OS 释放所有权。

多个 bucket 优先沿用其中已存在的默认控制目录；首次初始化才按规范绝对路径排序选取第一个 bucket，配置列表重排不会搬迁状态。已有外部控制目录的部署必须保留原显式路径；发现多个默认绑定、丢失默认绑定或已有绑定与当前拓扑不符时，启动明确失败，不自动重建失效历史。需要独立于整组数据磁盘保留部署身份时，继续显式配置外部控制目录。

绑定文件现在使用版本 4，同时核对真实索引目录、后端类型和屏障 catalog 身份，并标识允许缓存异步写回的恢复协议。既有版本 2/3 文件仅在原 catalog 完整且身份匹配时升级；空索引、替换的 catalog 或改变 DBPath 会拒绝启动。旧程序不能直接打开已升级的 state directory；回滚程序使用独立目录或先执行明确的离线迁移，不能删除绑定文件绕过校验。

存储绑定即使读取 `enabled: false` 也会注册 PURGE 并运行有界孤儿回收；磁盘模式建立受保护的持久化控制目录。启用读取需要该绑定，不能只增加缓存开关；不支持的后端、无效预算及变更的持久化拓扑会使启动失败。NutsDB 当前不承诺此模式的持久性。

## 普通缓存批量写回与可靠失效

公开 v2 的分片数据写入操作系统页缓存，关闭成功后确认分片，不在普通填充路径强制同步文件或目录。分片恢复日志、表示元数据和资源索引先进入可读的 Pebble 写回队列；同一键只保留最新快照，Get、扫描和后续 HIT 立即可见，无需等待落盘。

每个数据库每秒合并提交待写元数据，并为该周期执行一次 WAL 同步。队列达到 1024 个不同键或编码后约 4 MiB 时，提前提交一个 NoSync 批次以限制内存；这类批次不逐次等待同步落盘。后台 WAL 同步不持有队列锁，普通写入可以继续入队。后台失败会记录错误并阻止继续无界接收，后续周期重试；正常停机先等待后台任务，再排空队列和关闭数据库。

PURGE、目录失效、无查询参数映射及屏障压缩仍需可靠提交。同步写按数据库写入顺序将较早的待写批次与屏障一起提交，成功前不会确认 PURGE；后台不能在屏障之后重放更旧的排队快照。发布仍在资源锁内检查 incarnation、epoch、引用版本和完整分片状态。显式集合迁移保留同步切换协议。

异常断电允许丢失最近缓存，恢复后按 MISS 回源。启动恢复会检查已引用文件的存在、长度和新记录保存的 SHA-256 分片摘要，损坏的引用被撤销；此前同步保存的旧记录可以没有分片摘要。启动阶段还分页遍历固定哈希目录，清理没有元数据或位图归属的文件及遗留临时文件，不根据文件名重新挂接缓存。目录分页限制内存占用，但启动校验工作量随缓存大小增加；普通在线 Recover 不执行文件名清理，以免干扰活跃写入者。已经确认的 PURGE 屏障必须保留，恢复不会将被失效的旧表示重新变成可命中的缓存。

Pebble bucket 的 `db_config.wal_min_sync_interval` 单位是秒，应设为 `0`。该选项指定相邻 WAL 同步的最小间隔；设为 `5` 会延迟后台同步和可靠失效确认。

```yaml
storage:
  buckets:
    - path: /cache1
      db_config:
        wal_min_sync_interval: 0
        write_sync_mode: false
```

`0` 取消人为等待，后台批次和失效屏障仍执行同步落盘。`write_sync_mode` 控制旧格式普通写入；公开 v2 的普通表示和资源索引始终采用批量写回，可靠失效不受该选项影响。修改配置后重启服务生效。

## 纯内存模式

全部参与 v2 的 bucket 都使用 `driver: memory` 时，公开入口自动采用进程内模式。缓存字节、变体索引、目录屏障和源/成品依赖都保存在内存；普通重启和 SIGUSR2 新进程均从冷缓存开始，不承诺重启后的失效历史或数据恢复。进程内的固定代际、PURGE 屏障、读取租约、淘汰保护和停写等待仍生效，托管图片和可选响应合并可以使用此模式。

```yaml
storage:
  driver: native
  db_type: pebble
  representation: {} # 内存模式不需要 state_directory
  buckets:
    - path: inmemory
      driver: memory # bucket 设置覆盖顶层 driver
      type: normal
      max_object_limit: 10000
```

缓存中间件的 `representation.enabled` 仍需开启。模式依据实际 bucket 能力选择，不能把不支持同步写的磁盘后端当作 memory 降级。当前拒绝 memory 与磁盘 bucket 混用。纯 memory 若配置了尚未使用的 `state_directory`，不会创建状态文件或目录；若该目录已有磁盘模式的拓扑绑定，则拒绝复用，应省略内存模式的目录配置并保留原磁盘状态。

以下命名空间与失效规则在两种模式内相同；文中“持久化保留”及跨进程回滚历史只适用于磁盘模式。内存模式的图片转换仍可使用受预算限制的本地临时文件，临时文件不作为重启恢复缓存。

`namespace` 与 `isolation_scope` 为显式配置的部署身份。缓存身份由公开 URL、命名空间和显式隔离范围决定，不加入 upstream 配置、回源地址或所选节点；切源、主备切换和多源负载均衡沿用同一份缓存。需要隔离同一 URL 的独立内容时，显式使用不同的 `namespace` 或 `isolation_scope`。请求 rewrite 必须在 caching 之前，否则缓存命中与回源看到的选择头不同，服务器会拒绝这种顺序。首次开启 v2 不读取已有普通对象或旧 Vary 对象；新数据使用独立物理键。 从含 upstream 指纹或 peer 地址的旧 v2 键升级时，旧条目不会自动改名；首次访问新键需要重新预热，后续切源不再改变缓存身份。

目前 Vary 字段上限固定为 32，选择快照上限固定为 16384 字节；配置只允许默认值，暂不接受其他数值。`vary_limit` 计算 ready 和 pending 的不同选择项，超限的新组合旁路。`vary_ignore_key` 继续忽略，不能用它合并选择维度。

## 观察与维护

用同一 URL 的不同选择头验证字节、Content-Type、Vary 与 X-Cache；重复请求应分别命中各自表示。正常 Age/Date、条件验证、HEAD、单 Range 与受控部分缓存沿用已验证路径。星号、非法/超预算 Vary、禁止共享的请求或响应继续旁路。

文件/目录 PURGE 先同步逻辑屏障，再清理旧格式数据。v2 字节按读取/写入租约延迟回收，不能用磁盘文件仍存在判断 PURGE 失败。迁移以整个资源集合切换位置，不能调用旧格式单对象迁移来移动 v2 物理对象。

透明 v2 配置 `include_query_in_cache_key=false` 时，命名空间的无查询参数映射会持久化保存。PURGE 带查询参数的公开 URL 同时失效该命名空间的无查询键，关闭读取或重启后仍有效；其他命名空间仍保持精确查询参数隔离。最多保存 128 个此类命名空间，映射屏障计入既有条数和字节预算，预算不足时明确失败。不要通过改回配置或删除映射记录撤销既有失效历史。

成功的 304 使用本次验证响应的 Date/Age 和请求耗时重新计算新鲜度；缺少 Age 按零计算，不继续累计上一次响应的驻留年龄。Vary 字段变化可原子替换所选旧引用并复用其预算，提交前再次检查总数；失败时旧引用保留。

集合迁移通过协调器 MigrateResource 执行；既有自动 Promote/Demote 单对象策略用于旧普通对象，不承诺自动提升/降低 v2 集合。旧普通对象缺块时也须先建立强 If-Range 证明，再发送对应的响应头；没有可靠证明则获取完整替换并按客户端范围裁剪，不把新字节拼入旧响应头。

旧 Vary 文件可通过默认关闭的 `legacy_vary_gc_budget` 定时限速回收。目录屏障自动保守压缩，不按年龄删除；预算、重启续跑、故障演练及本地基准见 [表示缓存维护说明](representation-maintenance.md)。

保留固定 bucket 成员、路径、类型及 state directory。健康变化不转移控制 home；home 失败时旁路。增删成员或替换整个拓扑需要明确的离线迁移/重建流程，当前启动会拒绝静默重分片。

SIGUSR2 会停止新缓存任务，取消并等待活跃填充/读取及异步写入，关闭旧数据库后才启动新写入者。期间新请求经同一 origin 入口旁路；失败时不会自动重开旧库。关闭等待失败会阻止交接。响应合并默认关闭；未启用时并发 MISS 各自回源，需要按此行为评估源站负载。

## 回滚

1. 先关闭依赖 v2 的 transform.enabled 和 response_sharing.enabled（若已开启），再将 `server.middleware[].options.representation.enabled` 改为 `false`，通过正常停写/重启交接部署；保留托管路由映射，暂停的托管 GET/HEAD 明确返回 no-store 503，普通 URI 继续透明代理。
2. 保留 `storage.representation`、state directory、全部 bucket 和控制记录。此时不读取 v2，普通旧缓存可继续使用，旧 Vary 仍旁路；PURGE 仍覆盖 v2，重新启用不会忽略回滚期间的失效。
3. 不删除根、失效目录或拓扑 manifest 来“降级”。已有控制记录却移除存储绑定会使启动失败。
4. 回滚到不理解 v2 的旧二进制时使用独立缓存目录；复用原目录之前必须完成停写及明确的清理/失效迁移。

更完整的验收与后续图片阶段见 [实施方案](vary-representation-implementation-plan.md)。
