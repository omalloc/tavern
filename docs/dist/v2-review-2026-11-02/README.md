# v2 审查证据归档（2026-11-02）

本目录配合 [逐项审查文档](../../v2-change-requirements-review.md) 使用。最终验收结论为：**验收均不通过**。

这里的日志来自本次会话此前实际执行的命令；编写文档时只做归档和一致性检查，没有重新执行 Go 测试。日志中的 PASS/FAIL 只适用于对应命令和时刻，不能改变系列拒收结论。

## 1. 文件与命令范围

| 证据 | 文件 | 当时执行的范围及关键环境 |
| --- | --- | --- |
| E01 | all-features-initial.log | go test -v -count=1 /Users/sendya/workspace/github.com/omalloc/tavern/tests/all-features/...；连接已有 /tmp/tavern.sock，18 个顶层测试中 7 个失败 |
| E02 | all-features-current-source.log | 构建当前源码到临时目录，独立 socket/缓存/日志；只重跑原失败的 7 个测试，均复现 |
| E03 | all-features-after-origin-fix.log | 移除 upstream 身份绑定后，重新构建临时实例；原 all-features 全范围，5 个顶层测试仍失败 |
| E04 | stable-origin-control.log | 独立实例；临时保持每个 URL 的测试上游地址不变，执行原失败项；这是诊断夹具，不能计为原测试通过 |
| E05 | fixture-controls.log | 固定上游后，临时把两个 Range 重验证用例的 ETag 加引号、短文件的预热范围改为完整块；仅用于原因分离 |
| E06 | cdn-behavior-probes.log | 临时探针覆盖 Range、预取、条件/no-cache 和刷新容量；同时执行取消、固定拓扑、混合存储的既有边界测试 |
| E07 | disk-lookup-probe.log | 临时磁盘探针，4 个分片对象；仅查首字节前已打开所有块，模拟尾块缺失导致该查询 MISS |
| E08 | origin-switch-regressions.log | caching/server 两包的定向源切换、公开身份、持久重开、Vary、PURGE、图片和共享回归；该命令未用 -v，日志仅保留包汇总 |

实际工具链：go1.27.3 darwin/arm64；模块声明 Go 1.26.0。程序二进制构建使用 CGO_ENABLED=0。最初在受限环境中创建 httptest listener 被拒绝的输出未列为业务失败证据；上述 Go 测试在允许本地监听的环境执行。

E02 的测试选择正则为：

~~~text
TestCacheMethodAllowGETorHEAD|TestRevalidateDoubleSkip|TestErrCodeCache|TestContentLenShorter|TestRangeOffset$|TestRangeExpiredRefresh|TestSuffixRangeHitUsesCachedObjectSize
~~~

E04 使用相同选择。E05 仅选择：

~~~text
TestRevalidateDoubleSkip|TestContentLenShorter|TestRangeExpiredRefresh
~~~

E08 的测试选择正则为：

~~~text
TestRepresentationOriginSwitchRetainsCache|TestRepresentationKeyUsesPublicIdentity|TestPublicRepresentationUpstreamChangeRetainsCacheAfterReopen|TestRepresentationOriginCartesianAndVaryEvolution|TestPublicRepresentationReopenRollbackAndPurge|TestManagedExplicitRoutePurge|TestPublicManagedPipelineConfigurationAndReuse|TestPublicResponseSharingColdGetUsesOneOrigin
~~~

E02/E03 的临时 socket 覆盖只将 pkg/e2e 的 localAddr 和 SetLocalAddr 转到独立实例。E04 另外保持源站服务端口，E05 又修改诊断输入。只有 E03 用于说明源站身份修正后原 all-features 结果，不能混用三种覆盖的结论。

## 2. 探针结果如何解释

caching-probes.go.txt 和 storage-probes.go.txt 保存新增探针的函数片段，依赖仓库现有测试辅助函数，不是独立可执行程序。

探针断言用于确认当前观察，例如：

- 小 Range 重复两次仍两次回源且无引用。
- 带 X-Prefetch 的 Range 原样到达源站，没有完整预取。
- no-cache 请求拿到 new，后续普通请求仍返回 old。
- 缩小到 2 条的屏障预算无法接受第三个独立 URL，压缩删除 0 条。
- 单字节查找先打开全部 4 个块；未请求的最后一块缺失影响首字节查询。

因此探针 PASS 表示这些现象可复现，**不是业务验收通过**。整改后可用探针观察变化，但不应继续要求错误行为保持不变。

## 3. 在当前源码上复现探针

在仓库根目录执行以下命令。它在系统临时目录创建 Go overlay，把归档函数追加到现有测试文件的临时副本，然后只执行指定测试；不会修改仓库测试文件。需要可以创建本地 httptest listener 的环境，失败时先区分环境权限与业务结果。

~~~sh
python3 - <<'PY'
from pathlib import Path
import json
import subprocess
import tempfile

root = Path.cwd()
evidence = root / "docs/dist/v2-review-2026-11-02"
work = Path(tempfile.mkdtemp(prefix="tavern-cdn-audit-"))
mapping = {}
for source, snippet in [
    ("server/middleware/caching/representation_origin_test.go", "caching-probes.go.txt"),
    ("storage/representation/index_test.go", "storage-probes.go.txt"),
]:
    original = root / source
    replacement = work / original.name
    replacement.write_text(original.read_text() + "\n" + (evidence / snippet).read_text())
    mapping[str(original)] = str(replacement)
overlay = work / "overlay.json"
overlay.write_text(json.dumps({"Replace": mapping}))
print("Temporary overlay:", overlay, flush=True)
subprocess.run([
    "go", "test", "-overlay", str(overlay), "-v", "-count=1",
    "./server/middleware/caching", "./storage/representation", "./storage",
    "-run",
    "TestCDNAudit|TestMemoryRepresentationRejectsMixedStorage|"
    "TestManagerStableTopologyAndNoFallback|"
    "TestRepresentationFillCancellationClosesOriginAndReleasesBudget",
], check=True)
PY
~~~

此步骤复现 E06/E07 的观察范围，不复现历史二进制、服务残留和早期工作区的全部状态。测试辅助函数或接口变化后，片段可能需要重新审查；不要为保持旧探针 PASS 而保留已确认的业务缺陷。

## 4. 日志核对与缺失证据

SHA256SUMS 记录 8 个原始日志和 2 个探针片段的 SHA-256，归档时未改写日志内容。它用于核对归档一致性，不代表对历史运行环境的完整签名。

原始输出包含当时的临时文件绝对路径、随机对象摘要和测试请求头；正文不依赖那些路径继续存在。本目录不包含运行二进制、缓存数据、服务器完整输出或外部服务凭据。

没有归档为本次实际证据的项目包括：生产规模 TTFB/P95、长时间批量 PURGE、慢速大文件超时、全部图片真实编码矩阵、所有依赖/后端回归、原方案宣称的历史全仓测试。相应内容在主文档中标为静态确认、历史记录或待验证。
