# Vary 与资源转换缓存最终验收

日期：2026-10-30。范围为 [实施方案](vary-representation-implementation-plan.md) P1.1—P4.3、共用不变量及第 10.3 节的全部完成条件。按当前源码、运行结果和提交历史核验，不以阶段勾选或提交说明本身证明完成。

## 实际检查

环境：darwin/arm64、Apple M1 Pro、Go 1.26.0。测试使用本地 httptest、临时 bucket、实际 disk/Pebble、独立崩溃/升级子进程；不要求部署服务可用。

| 检查 | 当前证据 |
| --- | --- |
| CI 构建 | `GOTOOLCHAIN=go1.26.0 make build` 成功，项目目标强制 CGO_ENABLED=0 |
| CI 运行配置 | 实际启动 `./bin/tavern -c ./tests/config.test.yaml`，等待监听 `/tmp/tavern.sock`；内存缓存每轮重新启动，未借用已有实例 |
| 全量检查 | `TAVERN_TEST_CWEBP=1 GOTOOLCHAIN=go1.26.0 go test -count=1 -v ./...` 退出 0；39 个有测试 package 通过，962 条 test/subtest PASS，0 条 FAIL；同一次执行包含真实 WebP 检查 |
| 定向并发检查 | `go test -race` 覆盖 object/metrics、storage/representation、legacygc、disk/memory、storage、proxy、iobuf、purge、caching、server；最新修复后的 iobuf/purge/caching/server 再次通过 |
| 无 CGO 定向检查 | CGO_ENABLED=0 下新响应合并、表示读取、缺块证明、排队写任务、关闭边界及 Unix PURGE 检查通过 |
| 真实编码器 | `TAVERN_TEST_CWEBP=1 GOTOOLCHAIN=go1.26.0 go test ./transform ./server -run 'Test(HTTPWorkerRealWebPContract\|Managed.*Real\|Public.*WebP)' -count=1 -v` 成功；cwebp 1.6.0/libsharpyuv 0.4.2，9 个输入输出组合、24 项质量/无损/alpha 案例；公开 JPEG→WebP、JPEG→PNG、PNG→JPEG 通过 |
| Fuzz | FuzzParseVary、FuzzSnapshot 各运行 2 秒、2 workers，分别执行 104952、102705 次，无失败；检查确定性、输入所有权、预算及不 panic |
| 面板 | 现有 Dashboard JSON 的 32 个唯一 panel ID、40 条查询和 24 列网格边界通过；新增指标使用固定允许标签 |
| 基准 | `BenchmarkManagedRepresentation -benchtime=20x -benchmem` 的源/转换调用、命中率、控制 I/O、分配结果见 [维护说明](representation-maintenance.md)；不推算生产性能倍数 |

最终全量检查只有一个 SKIP：TestDurableCrashWriter 是由 TestDurableCrashRecoveryAndMissingFiles 启动的子进程入口，父测试通过，不能直接单独运行该退出入口代替恢复验证。两个 WebP 检查在默认环境下显式跳过，本轮设置开关后实际执行通过。GitHub Actions 的远端运行及部署 Grafana 未在本次触发；这里记录的是本地同等 CI 步骤的实际结果。

## 共用契约

| 方案要求 | 当前实现及证明 |
| --- | --- |
| §3 模块边界 | 选择工具/对象模型 → representation/transform → caching/server。存储协调器与转换器不导入 caching，worker 接收完整本地字节；Accept、规格、路由与 HTTPWorker 实际集中在 transform。第 9 节命令已按实际 package/测试名校正 |
| §4.1 快照、指纹与预算 | varycontrol 的 Parse/Capture/Match/Fingerprint；TestParseVary、TestSnapshot、TestVaryMatch、TestFingerprint、预算测试及两项实际 fuzz。字段缺失、空值、多行顺序及组合边界分别验证 |
| §4.2 格式与深拷贝 | object/representation.go、representation_key.go；TestResourceRecordCodec/Clone、TestStructuredKey、TestInspectedImageMetadataRoundTripAndClone。JSON/CBOR 往返，旧缺省和未知 schema 拒绝，控制记录不成为响应或普通 LRU 对象 |
| §4.3 键与身份 | ResourceKey、独立保留物理后缀、源 ID/version 与完整规范规格/policy/encoder 指纹；TestResourceKeyRestoresOpaqueIsolationWithoutChangingIDs、TestManagedSourceMappingPreservesUnrelatedQueryAndEscapedPath、TestSpecNormalizesApplicableDefaultsAndOwnsIdentity |
| §4.4 不变量 1—2 | 固定写入会话和不可变 body generation，Parts/Chunks 仅在确认后声明；TestRepresentationPartialGenerationAndHeaderIsolation、TestDurablePublicationOrderAndFailures |
| §4.4 不变量 3—5 | 新代际不合并旧块；ReserveFor/Publish 对 incarnation、purge/revalidate、catalog/prefix、routing 再检查；TestPublishCapturedInvalidationRejectsLateResult、TestPrefixBarrierSoftValidationAndHardCutoff、TestRepresentationPurgeRejectsLate200And304 |
| §4.4 不变量 6—8 | 读取 pin 允许旧读者结束，新查询先看屏障；网络前 Capture，派生提交复核确切源及 Date 冲突；TestManagerCollectionMigrationAndReaderPins、TestSourceReplacementRejectsLateDerivedCommit、TestPublishLateValidationSameDate、TestRepresentationLateOlderResponseCannotReplaceNewer |
| §6 提交协议五步 | publication.go：先捕获/预留、锁外确认数据、锁内复核并同步子/根、合并最新根、释放预算；失败不确认。数据/子/根同步失败分别注入，物理字节不按文件名重新挂根；崩溃与缺失块测试另行验证 |
| §6 迁移协议五步 | migration.go：锁内快照/迁移 pin、锁外全集合复制同步、版本/epoch 复核切换、读取 pin 延迟旧位置回收、失败目标日志回收；三个 ManagerMigration*、ActualReopen 测试检查实际文件与固定位置 |

## 分阶段检查项

下面的序号按对应阶段的勾选项顺序引用；同一行合并共享证据的要求，不省略阶段中的项。

| 检查项 | 源码与实际行为证据 |
| --- | --- |
| P1.1 #1—3 | caching_vary_test.go 的可控源站、独立 MIME/Body 并发与缺块旁路；proxy.Transport 固定关闭旧共享，额外 TestLegacyCollapseFlagCannotShareDifferentVaryRequests 验证旧 bool 参数也不能重开 |
| P1.1 #4—5 | caching_vary.go、representation_origin.go、cachecontrol：TestVaryBypass、TestCacheAdmissionRequest/Response/LegacyPolicy，星号/非法/预算/认证及 private/no-store 优先，不预先建立写入器 |
| P1.1 #6—7 | 非空 vary_ignore_key 一次启动弃用提示；示例删除该项；TestVaryLegacyBypass、TestCacheAdmissionOrdinaryHit、默认关闭 v2 的整套集成检查 |
| P1.2 #1—6 | 上表 §4.1 的六项工具契约及边界/碰撞/fuzz 测试；没有以逗号清洗请求字段或截断后继续匹配 |
| P1.3 #1—3 | 独立 record kind、根直接存物理 ID、IndexRepository Load/Update/ReserveFor/Release；TestIndexUnknownSchemaAndMissingChild、TestIndexUpdateConcurrent、TestReservationBudgetAndRelease |
| P1.3 #4—6 | 独立字段集合与选择/Date，新鲜度属于所选表示，Caching 分开持根/子；TestIndexLookupCartesian/DateAndFieldSets/StableDateTie、TestVaryAcceptanceFieldSetsAndRevalidation |
| P1.4 #1—2、#7—8 | admission 在 Body writer 前完成；200 替换选择项，ready+pending 预算及明确旁路；TestRepresentationOriginAdmissionBypass/BudgetBypassesOnlyNewSelector、TestRepresentationPendingBudgetReleasedByClose、TestVaryAcceptanceConcurrentBudget |
| P1.4 #3—5 | 实际验证请求建立新 Vary；保留允许字段、Age/Date；无 Body/验证器的 304 有界重取，重复 ETag/Last-Modified 不作确认依据；RepresentationValidation*、RepresentationAmbiguousValidatorForcesUnconditionalFetch、Unsolicited304IsBoundedAndClient304IsPreserved、Freshness 测试 |
| P1.4 #6 | 锁内重读合并；TestPublishConcurrentSelectors、TestRepresentationConcurrentOriginVariantsMerge、TestIndexUpdateConcurrent |
| P1.5 #1—5 | representation_acceptance_test.go 的编码笛卡尔积、字段演化、200/304、逗号选择真实 ID 退役及并发预算；检查实际字节及根引用 |
| P1.5 #6 | 阶段一提交 42a506f 的 go.mod 没有图片编解码依赖；此历史门槛不与阶段三后来加入的 x/image 混淆 |
| P2.1 #1—2 | representation_writer.go 与 write_session.go 分别拥有固定快照；部分响应存完整表示头，客户端裁剪在外；PartialGenerationAndHeaderIsolation、RangeCroppingConsumesFullOrigin |
| P2.1 #3—4 | v2 缺块完整重建到新 ID，不续写发布代际；旧普通对象缺块额外先证明强 If-Range，无证明完整替换；TestLegacyMissingRangeEstablishesVersionBeforeSendingHeaders、CanAccumulateProvenBlocks |
| P2.1 #5—7 | filling/confirmed bitmap/EOF、失败不发布、不删除已发布新代际、后台独立快照；UnknownLengthPublishesOnlyAtEOF、InterruptedBodiesNeverPublishPrefixes、FailedNewGenerationPreservesPublishedBody、CancellationReleasesReadPinAndBackgroundOwnsDuplicate |
| P2.2 #1—4 | 可选持久能力强制 SetSync，不被 NoSync 覆盖；VolatileAndUnsupportedBackends 拒绝 memory/NutsDB 持久声明；PublicationOrderAndFailures、DataSyncDoesNotHoldInvalidationLock、CrashRecoveryAndMissingFiles、InvalidBarrierHistoryCannotBeOverwritten |
| P2.3 #1—4 | URL/目录屏障覆盖冷资源、所有命名空间，按 escaped path 边界；SingleResourceInvalidationCreatesColdIdentity、PrefixBarrierCanonicalBoundariesAndColdResources、InvalidationFailuresBudgetAndCatalogIdentity、PurgeRoutesColdNamespacesAndFailsOnBarrierFailure |
| P2.3 #5—6 | dependency.go 对源过期/失效/换代/退役复核，独立规格退役；DerivedDependencyRetirementAndSourceValidation、SourceBoundDerivedLifetimeFollowsOnlySameFreshSourceVersion；plugin/purge 的状态、权限、预算/同步错误映射及 Unix 授权测试 |
| P2.4 全部定位与恢复要求 | ManagerStableTopologyAndNoFallback、StateDirectoryHasOneOwner、ActualReopenKeepsMigrationAndOrphanJournal；home 固定，不根据健康另建历史，不支持静默重分片 |
| P2.4 全部集合迁移与淘汰要求 | CollectionMigrationAndReaderPins、MigrationFailuresLeaveSourceAndCollectTarget、MigrationPurgeAndLateFill、EvictionRetiresSourceDependenciesAndRespectsReaders、SupersededFillKeepsIndependentWriterPin；MigrateResource 是协调器接口，旧单对象自动升降不冒充 v2 自动策略 |
| P2.5 #1—3 | origin 显式注入、proxy.Transport、clone 与 rewrite 顺序；TestInjectedOriginAndDrainingBypass、TestRequestRewriteMustPrecedeCacheLookup、RepresentationValidationCapturesActualUpstreamRequest |
| P2.5 #4—6 | 生命周期 Group 等实际 finish，文件/DB 停写、跨进程所有权、旧进程迟到调用拒绝；DrainCancelsBlockedFillAndJoinsBodyCallbacks、LegacyCancellationKeepsLeaseUntilActualBodyClose、LegacyQueuedWriteOwnsBoundedLifecycle、DiskWriterOwnershipAcrossProcesses、LiveSIGUSR2DrainsFillBeforeNewWriter、HandoffRequiresDrainAndCloseBeforeStartingWriter |
| P2.6 #1—7 | 上述持久失败、PURGE/304、迁移、注入及真实升级检查，加 PublicRepresentationReopenRollbackAndPurge、RepresentationPeerOverrideIsPartOfIsolation、InvalidRepresentationStartupCannotSilentlyRemoveCaching；公开开关与存储 owner 分离，暂停的保留托管路由明确 503，普通代理与映射 PURGE 仍可用 |
| P3.1 #1—3 | transform/accept.go、spec.go；AcceptSpecificityWeightsAndStablePriority、AcceptParametersAndFieldLines、AcceptRejectsMalformedOrUnboundedInput；格式参数失败不退成任意输出 |
| P3.1 #4—7 | SpecNormalizesApplicableDefaultsAndOwnsIdentity、PNGIsLosslessAndDoesNotReuseJPEGQuality、AlphaAnimationAndCodecConstraints、PlanSnapshotsCannotBeForgedOrMutatedByHeaders；质量/背景/版本进入完整身份，无 resize/crop/首帧提取 |
| P3.2 #1—3 | routes.go、managed_source.go、blob.go；SourceMapping/MappingRejectsDuplicate*、SpoolDecodedConfirmsEOFAndBoundsBothSizes、ManagedProviderCompressedSourceAndPrivateIsolation；固定源 Accept/identity，不递归公开 URL |
| P3.2 #4—6 | ManagedProviderCoalescesSourcesAndProducts、DifferentTargetsConcurrentlyShareSource、Source304KeepsProductsAnd200ReplacesThem；20 个同规格源/转换各一次，两个规格源一次/转换各一次，HIT 不增加源文件读取/解码/worker 计数 |
| P3.2 #7—8 | FilePurgeUsesSameSourceMapping、PurgeRejectsLateTransform、CancellationDoesNotCancelAnotherWaiter、PrivateOutputBoundsWholeTask；真实私有源不发布共享依赖，转换锁不包网络 |
| P3.3 #1—3 | http_worker.go、inspect.go、cmd/timagecheck；WireAndOutputOwnership、ContractVerify、ExplicitWorkerCheckUsesConfiguredEndpoint、ImageInspectorCompleteFormats/InvalidFrames；显式真实 WebP 矩阵通过后才可 Verified |
| P3.3 #4—6 | RejectsInvalidOutputsAndCleans、TimeoutQueueAndRedirect、RejectsDuplicateContractFields、RejectsUnverifiedMetadata、WorkBudgetBoundsAndCancellation；本地临时对象、完整解码/摘要、大小/像素、并发/等待与版本检查 |
| P3.4 #1—3 | managed_response.go；ManagedHTTPConditionsAndRangesUseFinalBytes、CompressedSourceAndPrivateIsolation、OriginalSourceAcceptChecksActualMediaParameters；实际 MIME/长度/ETag/coding 和所有分支 Vary |
| P3.4 #4—6 | SourceBoundLifetime、NoTransformAndFallbackIsolation、ColdRangeSameProduct、ValidationCannotRelabelOldWireBytes；304/HEAD/单多 Range/If-Range/416 在最终字节上计算，强 ETag 大小写敏感，冷请求共用规格 |
| 阶段三验收 #1—9 | 上述矩阵/20 并发、规格版本/质量/alpha、源 304/200 与失效、最终响应/编码、私有/回退和 worker 故障测试；另有公开 disk 重开、暂停期间映射 PURGE、恢复不复活及启动失败不透明降级 |
| P4.1 指标/标签/面板/文档 | pkg/metrics/representation.go 与相关接入点；MetricLabelsCannotContainRequestValues、CountConversionsNotProductHits、ReleaseFailedAndSupersededLeases、ObservedBudgetReturnsToZero*；面板静态检查和中英文说明/配置实际对照 |
| P4.2 #1—2 | LegacyCollectionBoundedLeaseProtectionAndExactIDs、ScheduledOptInAndRestartProgress；先同步切引用、再有界删文件，退休/部分进度可恢复；读取、写入和迁移 pin 的实际文件检查 |
| P4.2 #3—5 | BarrierCompactionPreservesEveryMatchingEpoch/FreshRootAndInFlightTicket/SyncFailureAndDiskRestart；ManagedStorageFaultsFailClosedAndRetry、TransparentHomeFailurePreservesOriginResponse、已验证 worker 随后停止；固定样本基准见维护说明 |
| P4.3 #1—5 | response_sharing.go；CanReuseResponseRequiresActualRequestAndResponseProof、EqualRequestsOwnEveryResponse、RejectedHeadersAndBodiesFetchIndependently、BudgetsIncludeUnconsumedBodies、PurgeDoesNotReusePreBarrierResponse、TransparentRegressionEnabledAndDisabled、PublicResponseSharingColdGetUsesOneOrigin；8/6 个等价请求分别源一次，Range/缺块未恢复联合 |

## 最终核验发现并修复的缺口

1. 旧 Range 限制器可在完整块末尾结束而不向源读取 EOF。同步/异步保存器 Close 现在确认该完整块，仍拒绝把不完整尾块当完成；新增边界测试不伪造 EOF。
2. 客户端完成后取消可使排队本地写入失败。固定旧写入会话现在使用独立、受存储 Drain 管理的有期限任务；Body.Close 等队列真正结束后释放旧读写租约，取消不提前释放物理排他保护。
3. 旧缺块在异步获取新响应头之前已发送旧头，可能配上新字节。现在在前台先以强 If-Range 证明版本；不匹配、弱/非法/缺少验证器时完整替换后裁剪。明确验证更新/缩短尺寸、ETag、Last-Modified、Content-Range 与最终字节一致。
4. Unix 未命名对端在 Darwin 可为空。PURGE 仅凭 net/http 提供的真实 Unix listener 身份归一为 `@`，仍需显式 allowlist；TCP、缺失 listener 和伪转发头不获得授权。
5. 集成测试删除未经验证的共享/范围联合假设，保留并加强并发 MIME/字节、完整文件 hash、范围、普通 HIT 和 URL 隔离检查。测试源对无 Range 请求返回完整 200；HEAD 用真实 HEAD。CI 配置修正布尔拼写，关闭影响确定性 TTL 的概率刷新并移除外部 verifier 依赖；verifier 自身测试仍执行。
6. 回滚保留路由时，不再将托管读取直接交给普通回源：暂停路由 GET/HEAD 返回 no-store 503，未匹配 URI 与 PURGE 继续原链；公共配置测试同时关闭转换/v2 读取，检查不联系 worker、不回源错误 MIME，普通 URI 仍可用，映射 PURGE 后再开启不复活旧成品。

## 完成与发布边界

§10.3 六项完成条件分别由上面的透明存储/生命周期、真实图片矩阵、分层调用计数、最终 HTTP 字节、逻辑屏障与迟到拒绝、配置/观测/全量 CI 证明。当前实现无待补的阶段项。

默认关闭的透明 v2、托管路由、可选合并应独立灰度。持久 v2 支持 disk/Pebble；memory 是进程内契约，NutsDB 不宣称持久 v2。跨格式元数据保留未经证明时明确拒绝转换；不支持 resize/crop/动画首帧。拓扑重分片及自动 v2 热冷策略未作为已交付能力。屏障只压缩被覆盖历史，不按年龄删除未覆盖记录。部署 worker 须按实际地址/版本重新 Verify，部署性能和在线 Grafana 状态不由本地测试推断。

启用、回滚、维护、转换协议、响应合并各有独立操作说明；关闭读取或优化后保留控制 owner、映射与失效历史。测试实例在验收后关闭，最终代码按阶段及核验修复分别提交。
