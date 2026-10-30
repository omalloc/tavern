# 透明 v2 响应合并

P4.3 提供独立、默认关闭的 `caching.response_sharing`。它要求 `caching.representation.enabled=true` 和既有 storage.representation 控制所有权；配置不满足或预算无效时启动失败。旧 `collapsed_request`、`collapsed_request_wait_timeout` 不启用此功能，proxy.Do 的旧 collapse 参数也不再共享响应。

```yaml
server:
  middleware:
    - name: caching
      options:
        include_query_in_cache_key: true
        representation:
          enabled: true
          namespace: http-v2
        response_sharing:
          enabled: false # 独立实例灰度时改为 true
          max_body_bytes: 1048576
          max_flights: 32
          max_waiters: 32
          wait_timeout: 1s
```

首版只处理没有既有表示的透明完整 GET。方法、URL（含转义、查询顺序和 Host）及全部实际选择头必须等价。每个请求拥有独立快照，并再次按本次 origin 的 Vary 检查，不能根据旧索引或摘要碰撞直接注册响应体。

请求无 Body、Range、条件头、Cookie、Authorization、Proxy-Authorization 或指定 peer；请求 no-store/no-cache/max-age=0 同样旁路。未知字段保留顺序与缺失/空值差别。请求头快照最多 32 字段、16 KiB，URL 最多 16 KiB，超过则独立获取。带客户端证书或要求关闭连接的请求也采用保守旁路。不同 namespace/upstream 隔离范围，以及 incarnation、PURGE/revalidate epoch、目录 hard/soft 屏障、位置与 routing epoch，不能加入同一组。

响应须为允许共享的完整 200，有唯一、可信的 Content-Length；Vary 星号/非法/超预算、private/no-store/no-cache、Set-Cookie、范围、Trailer、升级连接、未知长度或超过字节上限均不共享。拒绝时 leader 保留普通响应，等待者独立回源；不会先向等待者发送 leader 的响应头再判断资格。

为避免慢等待者阻塞其他人，首版将通过头部检查的小响应完整预读至 EOF，再给匹配者各自的只读缓冲视图。它会延迟这些响应的首字节，适合有界小对象；大对象、未知长度和流式响应立即保留普通路径。长度错误、读错误、关闭错误及异常退出不产生可共享的半成品，已读前缀和终止错误仍属于 leader。

`max_body_bytes` 默认 1 MiB，范围 1—8 MiB；预读最多为声明长度加一个用于检查越界的字节。`max_flights` 默认 32，范围 1—128；已完成缓冲直到最后一个响应 Body.Close 才释放名额，不能在 EOF 时假装内存已释放。实际 Go 分配包含容量和对象开销，不将这个配置解释为进程总内存上限。`max_waiters` 为每组除 leader 外的等待人数，默认 32，范围 1—128。`wait_timeout` 默认 1 秒，范围大于 0、最多 30 秒；等候超时或预算满时独立回源，单个等待者取消不取消 leader。

Header、Trailer、TransferEncoding、Request 和 Body 视图各自拥有状态。每个响应独立计算 Age，存储端不重复增加已经由合并层计入的同一段回源/缓冲时间；缓存状态与发布决策仍由各请求自己的处理链确定。合并只复用 origin 获取结果，不把共享缓冲当成长期缓存，不绕过各自的失效提交检查。

leader 的存储任务拥有预读，取消时关闭 origin Body 并等待实际关闭完成，没有脱离存储交接的后台取流。新 PURGE 后请求捕获新屏障身份，不能加入旧组；已开始的旧请求允许完成，但其过时提交仍被存储协调器拒绝。真实 SIGUSR2 测试在预读打开时验证停写与所有权交接。

HEAD、条件验证、缺块和所有 Range 获取保持独立，不按旧 object ID 联合范围。恢复缺块共享还需证明 body_generation/强验证器一致，当前没有开启这项优化。托管图片继续使用源和规范成品任务的两层门控，不经过此透明响应合并器。

观测使用 `tr_tavern_response_sharing_total{result}`，每次进入合并器恰好一个决定：leader、shared、rejected、capacity、timeout、cancelled 或 bypass。leader 表示接受一个候选，不代表响应最终可共享；rejected 表示等待者未通过响应证明后独立获取。`response_sharing_active` 计仍拥有缓冲名额的组，`response_sharing_waiters` 计正在等待证明及完整字节的请求。无动态 URL 或选择头标签。回源次数沿用 upstream 指标；不要以 shared 增长代替 MIME、字节和 PURGE 验收。

验证包含同一套 enabled/disabled 透明 Vary、星号、no-store、条件和 Range 回归；8 个实际等价请求只获取一次且 Header/Body 各自独立；公开 disk/Pebble 下 6 个并发冷请求一次回源，之后正常命中。另有取消、等待超时、人数/缓冲预算、异常、截断/越界、文件/目录 soft/hard PURGE 和实际 SIGUSR2 演练。不同 Accept 得到各自正确的 MIME/Body，比只检查回源计数更严格。

灰度按独立实例/配置部署，比较相同请求分布的真实源负载、首字节延迟、内存和失败率；没有通用收益倍数。出现问题时仅关闭 response_sharing.enabled 并正常停写重启，继续保留 v2、路由、控制目录和 PURGE，普通表示存储及图片能力不必随之关闭。
