# 托管图片路由：启用、HTTP 行为与回滚

P3.4 已接通公开转换入口，默认关闭。启用要求持久化 representation 缓存可用、查询参数参与缓存键、实际 HTTP worker 完整通过当前声明的输入输出矩阵检查。未部署后端或后端检查失败会拒绝启动，不会静默变成普通代理。

## 启用顺序

1. 按 [透明 v2 操作说明](vary-representation-operations.md) 配置 disk/Pebble bucket 和 storage.representation.state_directory。保留控制目录、bucket 拓扑和单写入者约束。
2. 部署 worker，按 [worker 契约](image-worker-contract.md) 使用独立检查命令验收实际地址和 encoder_version。只声明已经通过的转换组合。
3. 配置单一路由与小规模预算，再开启 transform.enabled 和 caching.representation.enabled。服务器启动还会执行一次矩阵检查；检查成功后才接受转换流量。
4. 使用代表性图片校准字节、像素、并发和超时预算，观察回源与转换次数，再扩大路由。

中间件相对顺序必须为请求头 rewrite → transform → multirange → caching。托管多范围请求由缓存层针对一个固定成品组装，避免不同部分选中不同源版本。非 GET/HEAD 请求保持原处理流程，PURGE 插件仍先于缓存管线执行。

## 配置

下面是主配置中的示例片段。worker 地址与版本均需替换；示例转换开关默认关闭。现有 config.yaml 不会被自动改为启用未部署的服务。

```yaml
server:
  middleware:
    - name: transform
      options:
        enabled: false
        startup_timeout: 2m
        routes:
          - path_prefix: /images/
            mode: auto
            default_format: source
            formats: [image/webp, image/jpeg, image/png]
            quality: 82
            png_compression: 6
            alpha_policy: reject
            animation_policy: preserve_source
            fallback_to_source: false
            policy_version: images-v1
        backend:
          name: http
          endpoint: http://127.0.0.1:8090/v1/transform
          capabilities:
            schema_version: 1
            policy_version: worker-policy-v1
            encoder_version: worker-build-v1
            flatten_alpha: true
            input_formats: [image/jpeg, image/png, image/webp]
            output_formats:
              - {mime: image/jpeg, quality_min: 1, quality_max: 100, lossless: false}
              - {mime: image/png, compression_min: 0, compression_max: 9, lossless: true}
              - {mime: image/webp, quality_min: 1, quality_max: 100, lossless: true}
            conversions:
              - {input_mime: image/jpeg, output_mime: image/jpeg}
              - {input_mime: image/jpeg, output_mime: image/png}
              - {input_mime: image/jpeg, output_mime: image/webp}
              - {input_mime: image/png, output_mime: image/jpeg}
              - {input_mime: image/png, output_mime: image/png}
              - {input_mime: image/png, output_mime: image/webp}
              - {input_mime: image/webp, output_mime: image/jpeg}
              - {input_mime: image/webp, output_mime: image/png}
              - {input_mime: image/webp, output_mime: image/webp}
        limits:
          max_input_bytes: 16777216
          max_output_bytes: 33554432
          max_pixels: 40000000
          concurrency: 2
          queue_size: 32
          timeout: 3s
          temporary_directory: /var/lib/tavern/image-tmp
    - name: multirange
    - name: caching
      options:
        include_query_in_cache_key: true
        vary_limit: 100
        representation:
          enabled: false
          namespace: http-v2
```

limits.timeout 覆盖一次源获取到成品发布的任务及 worker 排队/RPC；startup_timeout 是全部探测的总上限。预算必须为正，临时目录需预先创建。路由默认质量、PNG 压缩级别及 flatten 策略也必须落在后端声明的能力范围。limits 数值属于初始建议，不是吞吐或内存占用保证。

path_prefix 是以 `/` 结尾的绝对转义路径，最长前缀优先。默认仅识别 format 和 quality；可以通过 format_parameter、quality_parameter 配置其他参数名。识别的参数从源 URL 移除，其他查询字节、顺序、重复项和转义原样保留；不会根据文件扩展名推断另一个源地址。重复、非法或不适用的转换参数返回 400。

mode 为 auto 时，没有明确 image 类型偏好的 Accept 保留源图；有明确偏好时按 q 值、具体排除与配置格式顺序选择。mode: explicit 要求 format 参数。format 支持 source、jpeg/jpg、png、webp 或相应 MIME；quality 只用于有损 JPEG/WebP，范围还受能力声明限制。PNG 压缩级别是路由策略中的 png_compression，不复用 quality。首版 default_format 只能为 source，不支持缩放、裁剪和任意额外转换参数。

透明 PNG/WebP 转 JPEG 默认拒绝；显式配置 alpha_policy: flatten、background: '#ffffff' 且后端声明 flatten_alpha 后才允许。背景色、质量、policy_version、encoder_version 都进入成品身份。

## HTTP 行为

| 请求或结果 | 行为 |
| --- | --- |
| 成品命中 | 直接返回完整编码成品，不解码源、不执行转换 |
| If-None-Match | 针对最终表示的大小写敏感 ETag，匹配返回无 Body 的 304 |
| If-Match | 针对最终表示，失败返回 412 |
| HEAD | 与同规格 GET 相同的 MIME、长度及验证器，无 Body；冷请求允许内部完整获取和转换 |
| Range / If-Range | 针对最终编码字节返回 206/416 或完整 200；冷请求先完成完整成品，绝不把成品偏移传给源 |
| 多范围 | 使用同一固定成品组装 multipart/byteranges，最多 16 个范围，头部最多 16 KiB |
| Accept 无可用格式 | 406，不作为图片成品缓存 |
| worker 失败 | 默认 502，任务超时 504，队列满 503；错误响应 no-store |
| 可选回退 | 仅 fallback_to_source=true 且客户端接受源 MIME 时返回原图，使用原始源验证器和编码，响应 no-store，不写入目标成品键 |
| 请求或响应 no-transform | 原样路径保留源字节、MIME、编码及验证器，不解码或转换；显式格式冲突返回 406 |
| 不接受实际 Content-Encoding | 406；转换器只生成 identity，不能假装生成 gzip/br |

所有托管响应统一包含 Vary: Accept、Cache-Control、Accept-Encoding，并合并相关源选择字段；包括原样响应、回退、HEAD 和 304。错误响应也带协商维度并禁止缓存，避免下游把一次回退误当作稳定目标成品。

转换成品使用实际编码长度与 SHA-256 强 ETag；源 Content-Range、源验证器、旧摘要、编码和来源下载名称不会直接用于成品。原样返回保留源表示头。Age 会累计驻留时间，成品依赖源的新鲜度；源 304 保持内容版本并可延续成品，源 200 换代或 PURGE 后不能继续新鲜命中旧依赖。

首版对动画实行保留源或拒绝转换，不提取首帧。完整图片检查会识别 EXIF/XMP、ICC 及 PNG 朝向/色彩元数据；其跨格式保留尚未通过后端契约，因此转换明确拒绝这类输入，配置允许时可原样回退，也可显式 format=source。它们不会被静默旋转、丢弃色彩信息或写成错误目标。no-transform 原样路径不需要图片解码能力。

## 关闭读取与回滚

关闭 transform.enabled；需要时同时关闭 caching.representation.enabled。保留 routes、参数名、backend.capabilities、命名空间、storage.representation 和已有目录，重新启动以完成停写交接。关闭后不联系 worker，保留的路由仍负责把 `?format=...` 的 PURGE 映射到对应源集合，因此回滚期间的失效会在重新开启后继续生效。

保留路由下的 GET/HEAD 暂停为 no-store 503，仍带协商 Vary；不会把显式目标请求当普通 URL 直送源站，也不会把任意源 MIME 冒充目标输出。未匹配的普通请求继续透明代理，PURGE 等控制方法继续经过原处理链。若同时启用了 response_sharing，关闭 v2 读取时也要关闭 response_sharing.enabled；转换失败时允许源图回退的策略属于已启用托管路由的 fallback_to_source 配置，暂停路由默认明确返回错误。

不可直接删掉路由历史、改变源参数映射、命名空间或复用控制目录给另一套拓扑。首版不支持热替换这些映射；先完成失效和维护迁移，再调整配置。保留完整路由定义也允许在 worker 暂时不可用时关闭转换并继续执行正确 PURGE。

源和成品内部使用独立命名空间，不会被回滚时的普通 URL 查找当作透明响应。存储进入停写/关闭后，已识别的托管请求返回错误；不会绕过格式约束把目标 URL 直接回源。

## 验证证据

默认测试使用实际 JPEG/PNG 编码和进程内 HTTP worker，覆盖启动检查、20 个相同规格并发、HTTP 条件/范围、原样策略、错误回退、停止读取时的句柄释放，以及 disk/Pebble 重开和回滚期间的参数映射 PURGE。

本机真实 WebP 验收为独立显式测试，不是默认外部依赖：

```bash
TAVERN_TEST_CWEBP=1 go test -race ./server -run '^TestPublicManagedRealWebP$' -count=1 -v
```

该测试经过公开配置和真实 HTTP worker 适配器，验证 JPEG→WebP、JPEG→PNG、PNG→JPEG，以及同源 20 个两种目标格式并发请求的源/转换次数。部署地址仍须独立验收，不能从本机检查推定任意服务均支持该矩阵。
