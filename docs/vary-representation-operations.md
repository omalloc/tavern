# 透明 v2 缓存的启用与回滚

P2 阶段已接通透明 Vary 缓存。默认仍使用旧格式的普通无 Vary 缓存；旧 Vary 数据继续旁路。托管图片可按 [图片操作说明](managed-image-operations.md) 独立灰度启用，默认关闭；透明冷 GET 可单独启用 [经过验证的响应合并](response-sharing-operations.md)，在线重分片仍未开放。

## 启用

使用独立实例或部署配置做小流量灰度；当前没有按主机/路径动态分流或在线切换。先保持既有存储配置，在下面两个位置添加绑定：

```yaml
storage:
  # 原有 driver/db_type/buckets 等配置继续保留。
  # 公开持久化 v2 仅支持 native/disk + Pebble，所有参与的 bucket 都须满足。
  representation:
    state_directory: /cache-control/tavern-v2
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

`state_directory` 必须是单独且持久化保留的目录，不能与某个数据或索引目录相同。它保存拓扑绑定并持有排他所有权锁；数据与索引目录同样有写入所有权锁。不要删除锁文件来绕开另一个活跃进程。进程正常关闭或退出后 OS 释放所有权。

存储绑定即使 `enabled: false` 也会建立受保护控制目录、注册 PURGE 并运行有界孤儿回收。启用读取需要该绑定，不能只增加缓存开关；不支持的后端、无效预算及变更的拓扑会使启动失败。memory 只用于进程内契约测试，NutsDB 当前不承诺此模式的持久性。

`namespace` 与 `isolation_scope` 为稳定的部署身份。服务器还自动加入 upstream 配置身份；单次指定的 upstream 地址进入独立隔离范围。请求 rewrite 必须在 caching 之前，否则缓存命中与回源看到的选择头不同，服务器会拒绝这种顺序。首次开启 v2 不读取已有普通对象或旧 Vary 对象；新数据使用独立物理键。

目前 Vary 字段上限固定为 32，选择快照上限固定为 16384 字节；配置只允许默认值，暂不接受其他数值。`vary_limit` 计算 ready 和 pending 的不同选择项，超限的新组合旁路。`vary_ignore_key` 继续忽略，不能用它合并选择维度。

## 观察与维护

用同一 URL 的不同选择头验证字节、Content-Type、Vary 与 X-Cache；重复请求应分别命中各自表示。正常 Age/Date、条件验证、HEAD、单 Range 与受控部分缓存沿用已验证路径。星号、非法/超预算 Vary、禁止共享的请求或响应继续旁路。

文件/目录 PURGE 先同步逻辑屏障，再清理旧格式数据。v2 字节按读取/写入租约延迟回收，不能用磁盘文件仍存在判断 PURGE 失败。迁移以整个资源集合切换位置，不能调用旧格式单对象迁移来移动 v2 物理对象。

旧 Vary 文件可通过默认关闭的 `legacy_vary_gc_budget` 定时限速回收。目录屏障自动保守压缩，不按年龄删除；预算、重启续跑、故障演练及本地基准见 [表示缓存维护说明](representation-maintenance.md)。

保留固定 bucket 成员、路径、类型及 state directory。健康变化不转移控制 home；home 失败时旁路。增删成员或替换整个拓扑需要明确的离线迁移/重建流程，当前启动会拒绝静默重分片。

SIGUSR2 会停止新缓存任务，取消并等待活跃填充/读取及异步写入，关闭旧数据库后才启动新写入者。期间新请求经同一 origin 入口旁路；失败时不会自动重开旧库。关闭等待失败会阻止交接。响应合并默认关闭；未启用时并发 MISS 各自回源，需要按此行为评估源站负载。

## 回滚

1. 将 `server.middleware[].options.representation.enabled` 改为 `false`，通过正常停写/重启交接部署。
2. 保留 `storage.representation`、state directory、全部 bucket 和控制记录。此时不读取 v2，普通旧缓存可继续使用，旧 Vary 仍旁路；PURGE 仍覆盖 v2，重新启用不会忽略回滚期间的失效。
3. 不删除根、失效目录或拓扑 manifest 来“降级”。已有控制记录却移除存储绑定会使启动失败。
4. 回滚到不理解 v2 的旧二进制时使用独立缓存目录；复用原目录之前必须完成停写及明确的清理/失效迁移。

更完整的验收与后续图片阶段见 [实施方案](vary-representation-implementation-plan.md)。
