# 图片 worker 契约与部署前检查

P3.3 已提供 HTTP worker 适配器、完整图片检查器和独立检查命令。公开转换路由仍保持关闭，HTTP 条件请求、Range、失败回退及公开配置由 P3.4 接入。仓库没有假定已部署的 worker 地址。

## HTTP 协议 v1

适配器仅向配置好的 HTTP/HTTPS 地址发送 `POST /v1/transform`。Body 是已经完整读取、解压并检查的源图片，具有完整 Content-Length。请求不包含源 URL、客户端 Cookie、Authorization 或客户端伪内部头；worker 不需要回源。

| 字段 | 内容 |
| --- | --- |
| Content-Type | 已确认的源 MIME：image/jpeg、image/png 或 image/webp |
| Accept | 本次规范规格的输出 MIME |
| Accept-Encoding | identity |
| X-Tavern-Transform-Version | 1 |
| X-Tavern-Transform-Spec | TransformSpec 规范 JSON 的无填充 base64url 编码 |

JSON 字段顺序及可选字段与 `api/defined/v1/transform.TransformSpec` 的标准 JSON 编码一致。适配器生成该值，不透传客户端提供的规格。示例解码内容：

```json
{"schema_version":1,"output_mime":"image/jpeg","quality":82,"lossless":false,"alpha_policy":"reject","animation_policy":"reject","orientation_policy":"preserve","color_policy":"preserve"}
```

JPEG/WebP 有损模式使用 quality；PNG 必须为 lossless，使用 compression_level，不能把 quality 当作 PNG 压缩级别。WebP 无损规格不包含 quality。JPEG 遇到透明源默认拒绝；显式 flatten 必须带规范 `#rrggbb` 背景且后端声明支持。版本 1 无缩放或裁剪，输出宽高必须等于源宽高；朝向与色彩策略只接受 preserve。worker 必须遵守这些策略，不能默默旋转、改变色彩或提取动画首帧。

成功响应必须为 200，Content-Type 必须与规格一致，并具有单一 `X-Tavern-Encoder-Version`，精确匹配配置。响应图片应为 identity 编码、完整静态图片。适配器拒绝重定向、非 200、错误 MIME、重复协议头、版本不符、错误页、截断、尺寸变化、预算超限及透明通道丢失。后端版本发生变化时应修改 encoder_version，避免复用旧成品。

输出先写本地临时文件并计算 SHA-256 和实际长度，确认完整容器及可解码性后才交给表示存储。调用方必须 Close 输出；成功发布、错误和取消都会释放临时文件及执行名额。源/成品索引沿用表示协调层的提交和失效屏障，RPC 返回成功本身不会发布缓存。

## 有界执行

输入与输出各自具有字节上限；完整检查同时限制像素、容器块数和动画帧数。PNG/APNG 与 WebP 动画逐帧完整检查，累计帧像素计入预算，动画只交由源策略决定保留或拒绝。JPEG/PNG 使用标准库，WebP 解码使用 golang.org/x/image v0.47.0；Tavern 保持 CGO_ENABLED=0，不内置 WebP 编码器。

整个源获取/解码/转换/发布任务受并发、排队及超时预算约束；worker 另有 RPC 预算。等待中的请求不提前获取和解码源。私有输出持有任务名额到 Close，worker 输出持有 RPC 名额到 Close，因此已完成但尚未消费的临时文件也受限。队列满明确返回忙，不无限等待。

字节预算硬上限为 128 MiB，执行并发范围 1—128，队列范围 1—4096。超时涵盖排队、网络读取和输出检查，取消传播到 RPC。解码器通过受上下文控制的读取检查取消；CPU 解码不是独立进程，内存与耗时仍需要用实际图片校准。

以下示例值是初始预算，不代表性能保证。临时目录应预先创建，并具有足够空间；不要跨进程共用未受磁盘容量管理的无限目录。

## 检查实际部署的 worker

创建独立 `worker-contract.yaml`；这不是 Tavern 主配置，也不会开启流量：

```yaml
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
```

只声明部署后端实际支持的输入、输出和转换组合。执行：

```bash
CGO_ENABLED=0 go run ./cmd/timagecheck -config worker-contract.yaml -timeout 2m
```

检查命令逐一执行声明的组合，并验证质量区间两端、PNG/WebP 无损像素一致性、透明通道保留与显式白底 flatten。质量区间跨度至少 20 时，要求两端产生不同输出，拒绝完全忽略质量的 worker。输出 JSON 包含 encoder_version、能力指纹和各检查的成品摘要。全部组合通过才返回成功；未验证或失败的实例不能用于公开路由绑定。

这些小图用于功能契约，不代替代表性图片、EXIF/ICC 元数据兼容性和吞吐量验收。检查通过仅对当前配置和 worker 版本成立，不将全部 JPEG/PNG/WebP 服务统一标为支持。

## 仓库验证方式

默认 Go 单元测试使用进程内 HTTP 测试服务及实际 JPEG/PNG 编码字节，不依赖独立服务或本机编码工具。另提供显式本地 WebP 编码矩阵检查：

```bash
TAVERN_TEST_CWEBP=1 go test ./transform -run '^TestHTTPWorkerRealWebPContract$' -count=1 -v
```

此命令要求本机 `cwebp` 可执行文件。P3.3 已用 cwebp 1.6.0 验证 9 种输入输出组合，共 24 个质量、无损与透明策略案例；这验证适配器和该本地编码组合，部署地址仍必须单独检查。微型 WebP 固定样本由本项目生成的像素编码而来。
