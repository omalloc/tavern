# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- implement range-union collapsed forwarding for chunk requests ([30f6b1f](https://github.com/omalloc/tavern/commit/30f6b1f980bd9752912805578037acbfbd64e543))

- implement collapsed forwarding for object and chunk requests with tests ([473e921](https://github.com/omalloc/tavern/commit/473e921db46777b30bce8495c86b1d65c4d7533c))

- add comprehensive Prometheus metrics across cache, proxy, server, and storage layers ([#62](https://github.com/omalloc/tavern/pull/62))


### Changed

- use shared metrics namespace constant, update Go toolchain to 1.26 ([38eb63e](https://github.com/omalloc/tavern/commit/38eb63ecaabb5d4dac74849ebda0533b4fcd671e))

- reorganize metrics into pkg/, add traces package, enhance iobuf ([0fe04fe](https://github.com/omalloc/tavern/commit/0fe04fe534a02c3f25492adfbd1e3c2e9508851f))


### Documentation

- overhaul README with comprehensive project overview ([114e494](https://github.com/omalloc/tavern/commit/114e494d72cd430e33f4adff2f8da061e1440751))

- add comprehensive Tavern CDN ecosystem documentation ([e49479b](https://github.com/omalloc/tavern/commit/e49479b05b99f356b37948fa1a06b673b89ed013))


### Fixed

- update caching middleware to handle cache bypass and improve error handling ([#56](https://github.com/omalloc/tavern/pull/56))

- resolve code review findings for collapsed forwarding ([b324e5b](https://github.com/omalloc/tavern/commit/b324e5b0b381c6d96c6e14e7ab6664326a117f02))

- improve collapsed forwarding tests and fix race conditions ([5d2832c](https://github.com/omalloc/tavern/commit/5d2832c2f7267d8bedac705cb5e9c19e984548b1))

- add panic recovery in object and chunk flight groups to prevent goroutine leaks ([ddd666d](https://github.com/omalloc/tavern/commit/ddd666d905cf48bd93bd45be2d2084e31c20514c))

## [1.1.1] (https://github.com/omalloc/tavern/compare/v1.1.0...v1.1.1) (2026-06-02)

### Fixed

- return valid Caching struct on preCacheProcessor error to avoid nil dereference ([bb2191b](https://github.com/omalloc/tavern/commit/bb2191bc9e57b26ab54cf54ac56271e1a1e19765))

- use c.ctx instead of req.Context() in revalidate DiscardWithMessage for correct request ID ([f8df9a2](https://github.com/omalloc/tavern/commit/f8df9a2d71ccccc4162dd2631642ed6d50e488ad))

- prevent double-skip in revalidate path by clearing fillRange context before lazyRespond ([10e0bfb](https://github.com/omalloc/tavern/commit/10e0bfbc6907d24608b5962a14021cd487aad4b6))

- prevent double-close in partsReader by advancing index after Close ([269cd3a](https://github.com/omalloc/tavern/commit/269cd3afdf5ea5303a71cadc7becf13c96f562eb))

- clear metadata chunks during cache revalidation and add tests for revalidation logic #53 ([#54](https://github.com/omalloc/tavern/pull/54))

## [1.1.0] (https://github.com/omalloc/tavern/compare/v1.0.0...v1.1.0) (2026-04-23)

### Added

- add protocol code generation and update caching logic with new protocol constants ([684e623](https://github.com/omalloc/tavern/commit/684e623bd35a5e9affad893b64b52382b4fef65f))

- introduce iobuf.NopCloser for optional close operations, update dependencies ([#50](https://github.com/omalloc/tavern/pull/50))

- Implement smoothed requests per second (RPS) calculation and display in the top command, and improve plugin shutdown logic. ([#43](https://github.com/omalloc/tavern/pull/43))

- enhance `TopK` output with object details and update `top` command display logic, and rename `qs` plugin API paths ([9cfc0b2](https://github.com/omalloc/tavern/commit/9cfc0b2741bd2be4124bb7b35fffd9be950d6ab7))

- implement real-time monitoring for QS plugin via SSE and introduce `ttop` command with enhanced hot key details. ([df37a4c](https://github.com/omalloc/tavern/commit/df37a4ce327a3bdb0c88935f3add8f8306936610))

- implement TopK functionality for LRU cache and storage buckets, integrating it into the QS plugin for hot URL tracking and on-demand metric collection. ([c14ff51](https://github.com/omalloc/tavern/commit/c14ff519466d3149e609b4d64856a090082f4b98))

- add CPU and memory usage metrics to QsPlugin ([b8db396](https://github.com/omalloc/tavern/commit/b8db396ab7b3b872b7209354237bf1f4d948c1ab))

- implement request smoothing and add metrics endpoint ([f4623a8](https://github.com/omalloc/tavern/commit/f4623a89382ea23ddd79b8112bab3584cdbd6215))

- implement object migration for memory and disk buckets and correct DBPath in disk migration tests. ([#40](https://github.com/omalloc/tavern/pull/40))

- Add `Touch` method to storage buckets for updating object access metadata and integrate it into the caching middleware. ([4c8a3b8](https://github.com/omalloc/tavern/commit/4c8a3b8baf80d3a9e61ebd916e0bfeff4ce62193))

- promote support ([21b0615](https://github.com/omalloc/tavern/commit/21b061575a43d33adb78befe6ae3f6ac615dbb9d))

- introduce storage migration functionality with configurable promote and demote policies. ([8fc91b6](https://github.com/omalloc/tavern/commit/8fc91b602b835468ab3b2969b1e0b8b75cbf5aec))

- Introduce flag constants and enable conditional caching of error responses using `FlagOn`. ([#39](https://github.com/omalloc/tavern/pull/39))

- replace md5 with xxhash ([36378e1](https://github.com/omalloc/tavern/commit/36378e1f7b009cb3a0c8f5d558c36d2e0b4233f9))

- add Tavern service unit file for systemd integration ([0358581](https://github.com/omalloc/tavern/commit/03585815edc27b2db60445f399f64156ad03fed6))

- replace example-plugin with qs-plugin and update related functionality ([b188143](https://github.com/omalloc/tavern/commit/b188143c250bbfaac375944833b29d3b9a85bd4b))

- add Grafana dashboard template.json ([915cd93](https://github.com/omalloc/tavern/commit/915cd9326b83d0e5356c3183819e7083880f2a31))

- add Prometheus metrics for purge request tracking ([#35](https://github.com/omalloc/tavern/pull/35))


### Changed

- rename internal constants package to protocol and document internal headers. ([6f21c37](https://github.com/omalloc/tavern/commit/6f21c37d4dbe7aa1a275b8aee804ec20d0d26013))

- implement DirAware storage functionality with new sharedkv implementations and configuration. ([c9c661c](https://github.com/omalloc/tavern/commit/c9c661cda42585f1be821262175c7826f48c0796))


### Documentation

- Mark "Hot migration" and "Warm/cold split" as completed in READMEs ([#41](https://github.com/omalloc/tavern/pull/41))


### Fixed

- PebbleDB and noneSharedKV Close idempotent using `sync.Once`. fix hot upgrade process ([#51](https://github.com/omalloc/tavern/pull/51))

- pass correct chunk index to checkChunkSize in getContents ([7c7c5a0](https://github.com/omalloc/tavern/commit/7c7c5a0ede3cfea984eae217fd5dfad9c74b9551))

- Ensure response body is always closed in async revalidation.  fix #46 ([#47](https://github.com/omalloc/tavern/pull/47))

- close file descriptors in getContents error paths to prevent fd leaks ([#45](https://github.com/omalloc/tavern/pull/45))

- (Breaking Change) rename `normal` storage type to `warm` ([#42](https://github.com/omalloc/tavern/pull/42))

- test case config apply `migration` ([34a8e28](https://github.com/omalloc/tavern/commit/34a8e280151952ee7fbc4696f3e1ccade52fd5c3))

- caching middleware's partRequest getUpstreamReader range request preparation. ([#38](https://github.com/omalloc/tavern/pull/38))

- add iobuf testcase ([fdff4dd](https://github.com/omalloc/tavern/commit/fdff4dd6145fbf01aa88b8e2ab404e493323b872))

- refactor savepart async reader, and chunkWriter close check ([d434abc](https://github.com/omalloc/tavern/commit/d434abc48a0f55af8202b967514f96a717b24197))

- lru coverage; remove kv_pebble log ([5208c71](https://github.com/omalloc/tavern/commit/5208c715ac45ebd73dd785e4ff24b7710dd65ffa))

- refactor getContents to improve find last hit block index. ([8b7f3f1](https://github.com/omalloc/tavern/commit/8b7f3f15d9d6b71df364b4d21a1b630a50b2d20a))

## [1.0.0] (2026-01-20)

### Added

- enhance DiscardBody function to support read speed limit and update related tests ([#33](https://github.com/omalloc/tavern/pull/33))

- save purge dir task to sharedkv and add design doc ([658aecf](https://github.com/omalloc/tavern/commit/658aecf846e0a2b03a6e9eef4d29b98371e5a91c))

- enhance PathTrie to support generic key-value types and update related logic ([dae0b1e](https://github.com/omalloc/tavern/commit/dae0b1e25803de05de4b3e0061f3b9a6fc35e1bb))

- implement marked storage with push-mark logic and directory purge enhancements ([7264c52](https://github.com/omalloc/tavern/commit/7264c5266b0ef665a408e3ac5297d162520dc61c))

- add Clone method for RequestMetric and NewContext function for context management ([f1f1d71](https://github.com/omalloc/tavern/commit/f1f1d71731f2a4f0bb884e8a6b93eb52ce448210))

- Implement fuzzy refresh middleware for caching (#31) ([#31](https://github.com/omalloc/tavern/pull/31))

- implement Hijack method in ResponseRecorder and enhance error handling for chunked responses ([#32](https://github.com/omalloc/tavern/pull/32))

- update mock server to use rate limiting ([fbee3ce](https://github.com/omalloc/tavern/commit/fbee3cec3367737a79c8b5b6f1a51605aafbadc6))

- only cache GET/HEAD requests and discard RequestURI for proxy clients ([46d36b9](https://github.com/omalloc/tavern/commit/46d36b91bfa53429b351f569f2b4e85afa5404de))

- add tests for cache error codes and allowed methods ([fa5136b](https://github.com/omalloc/tavern/commit/fa5136b36a5cb57097108b49cf55df5c534b8327))

- add e2e tests ([61f9034](https://github.com/omalloc/tavern/commit/61f9034bfa791c3c02a8449c51acdf6cd127905b))

- implement ByteRange parsing and handling for HTTP range requests ([6f3a413](https://github.com/omalloc/tavern/commit/6f3a41320d3edf9eaf573b1d423ef812eca83d0f))

- implement graceful start and stop handling with SIGUSR2 support ([abca883](https://github.com/omalloc/tavern/commit/abca883637a5645dea1421354a004fe5253d9819))

- add DBPath configuration for storage buckets and implement NutsDB indexdb ([9e7849b](https://github.com/omalloc/tavern/commit/9e7849b6685a7458930654dc73d7615e5a5d568a))

- improve chunk handling ([9ede226](https://github.com/omalloc/tavern/commit/9ede226a80b6857f5fabf78ee03c16b14ae7bab0))

- enhance ReadChunkFile to return chunk path ([a2eeeae](https://github.com/omalloc/tavern/commit/a2eeeae39e2b6e5d06b5b912f4e1a4fdd8b1f0b6))

- refactor bucket structure, add ReadChunkFile method ([381c80c](https://github.com/omalloc/tavern/commit/381c80c26621124a71cb81da2b982af587e2f3d3))

- add WriteChunkFile method for bucket implementations ([b5b95b1](https://github.com/omalloc/tavern/commit/b5b95b12838c174b0a9ffc682404695503c18ac7))

- implement cache completion event handling and reporting ([475dcd7](https://github.com/omalloc/tavern/commit/475dcd76b3f7a732e678c1bc9f21bbf8f23187c4))

- enhance storage cleanup, add plugin for service domains ([6d5526c](https://github.com/omalloc/tavern/commit/6d5526cf0b57161ed3557cda8bf096b1c496ca9a))

- improve HTTP vary  handling (rfc7231#section-7.1.4) ([569b891](https://github.com/omalloc/tavern/commit/569b891458e61349403382b551681f957d1e2180))

- implement in-memory SharedKV store using Pebble; directory purge supports. ([f607e70](https://github.com/omalloc/tavern/commit/f607e70a71a35d2bc68343278594eb0cd18ac975))

- logger configuration with rotation settings and add runtime version http endpoint ([a7389de](https://github.com/omalloc/tavern/commit/a7389de5a69107e8dbde599e3af4b0c2b4c6ffc2))

- add async SavepartReader ([#17](https://github.com/omalloc/tavern/pull/17))

- add LocalApiAllowHosts and fill range settings to server configuration ([db30de2](https://github.com/omalloc/tavern/commit/db30de2f7f009864aa5acb6d4e8b78eaaeec13f3))

- implement fill range processing and enhance chunk handling ([076ce7f](https://github.com/omalloc/tavern/commit/076ce7f2c86f7573cdb0357be685c08fef2fadbe))

- add Build function for chunk index generation ([24e3bb0](https://github.com/omalloc/tavern/commit/24e3bb0e3a61f0c4f249ac59b64caa58ee1b82de))

- caching with slice-file support ([5f01923](https://github.com/omalloc/tavern/commit/5f01923326e0f1f7349bdf832cea52f046eec0bd))

- add VaryProcessor for handling Vary headers in caching logic ([88ff037](https://github.com/omalloc/tavern/commit/88ff037294c49751c42e6e8c2e7f7e0e156eab43))

- integrate RequestID and improve access log handling ([a20756c](https://github.com/omalloc/tavern/commit/a20756cb627f3e5c9a24b9f190b6e69f48400b80))

- WIP: implement caching pool and reset functionality; add benchmark test ([89d9d93](https://github.com/omalloc/tavern/commit/89d9d93ddb4f7bf040b70c584c6bab92c8b76d3b))

- plugin handling and configuration options ([1388301](https://github.com/omalloc/tavern/commit/13883013e32f70d571774dc7b247148a0b08f869))

- add JSON marshalling for `ID` and metadata cloning logic ([e67567a](https://github.com/omalloc/tavern/commit/e67567a05e1610e709eec071dc162779146c9b69))

- add support for configurable request collapse wait timeout ([cb301bb](https://github.com/omalloc/tavern/commit/cb301bbcbd9ebd5d21da865187aaa8c32ef434f3))

- add customizable logger with support for file-based output ([e520d35](https://github.com/omalloc/tavern/commit/e520d35b657e9675c6fa48070c17bc61ecae1904))

- support PURGE cache handler ([c4bd2c1](https://github.com/omalloc/tavern/commit/c4bd2c17c6f5e1b58aecc7ccc361bc75c7a71ad9))

- add FileChangedProcessor for file change detection and handling ([10e43a2](https://github.com/omalloc/tavern/commit/10e43a2bc1b733d24d6b1a17a15f9712b25bfb81))

- introduce prefetch processor for enhanced caching performance ([7782db0](https://github.com/omalloc/tavern/commit/7782db0f2a8e95eb4eb965c48d36791592cd28ae))

- caching with async range requests and add async reader utility ([2e8fe8c](https://github.com/omalloc/tavern/commit/2e8fe8cc3455faab70052ebf704a11a500cd25f7))

- add block-based bitmap utilities, range readers, and part reader implementations ([b6588c8](https://github.com/omalloc/tavern/commit/b6588c842060d648f118e51f9fcf708d117422f2))

- caching middleware with range request handling ([1959cb9](https://github.com/omalloc/tavern/commit/1959cb982dce8e04d0f7e8bf39deffd294708c48))

- add SavepartReader and RangeReader implementations with associated tests ([5d2820e](https://github.com/omalloc/tavern/commit/5d2820ef4daf3119cd9ed2c86f987680d03be6b9))

- add ResourceLocker interface and implement locking mechanism in caching middleware; enhance bucket interface with Path method ([723ef93](https://github.com/omalloc/tavern/commit/723ef930982b202be77b7232fb7d3841afca7d7c))

- implement multi-range middleware with support for multi range requests ([30d2644](https://github.com/omalloc/tavern/commit/30d2644fbd206cbb7d2dd3fcaaa75d3a87658ba5))

- implement caching middleware with support for range requests and cache control parsing ([564edb9](https://github.com/omalloc/tavern/commit/564edb9c756d464106830c06b9a456d0888ecb19))

- add access logging, metrics, and query tool with usage documentation ([5997cf2](https://github.com/omalloc/tavern/commit/5997cf2121f49892267c113713ccf1cb15f81129))

- add access logging and pprof support with configuration options ([20a779c](https://github.com/omalloc/tavern/commit/20a779c54bbc561ea8088c45594e046670b82d97))

- implement discard methods and add unit tests for disk bucket ([2162fc3](https://github.com/omalloc/tavern/commit/2162fc3ad22c2aedd1348e71c92d6f69207f005c))

- disk bucket with improved metadata handling and path utilities ([724cbd5](https://github.com/omalloc/tavern/commit/724cbd546e6ff696bf86c26dcdc3a4f9bcd316e4))

- disk bucket with LRU caching and async loading support ([6aa7be0](https://github.com/omalloc/tavern/commit/6aa7be06813fe49fa407db994007f3bd9077d995))

- implement flexible storage layer with multiple backends ([90d84d6](https://github.com/omalloc/tavern/commit/90d84d6b28c9cfb9760bf14efe11d102b15f5c6f))

- implement a new proxy system with selector and singleflight support ([b14a84e](https://github.com/omalloc/tavern/commit/b14a84e9501910aa23680b2e914c71a0c399de18))

- add serveMux handle ([50e759b](https://github.com/omalloc/tavern/commit/50e759beb7dfcf0765618924ab3a8672e9b36608))

- add transport plugin ([4c7de80](https://github.com/omalloc/tavern/commit/4c7de804591bd1ed468163460f657ce0c9d7348c))

- add caching middleware ([3d8b4e9](https://github.com/omalloc/tavern/commit/3d8b4e9c3840920cbb989041fcae4742e737cc16))

- add prometheus register ([50f8d87](https://github.com/omalloc/tavern/commit/50f8d87d882e92fd93fda0c7076faf6c8aec164d))


### Changed

- improve caching metrics CacheStatus in accesslog ([#19](https://github.com/omalloc/tavern/pull/19))

- simplify DiscardWithHash by delegating to discard method ([0dcdeae](https://github.com/omalloc/tavern/commit/0dcdeaee38e87912214e8c33ddeb74a773e58c25))

- update middleware options and remove deprecated config file ([2b5d44e](https://github.com/omalloc/tavern/commit/2b5d44e8ec95f4b2147905d1d335481c6b1c0e9e))


### Documentation

- update README.md with project banner, badges ([18c5fd5](https://github.com/omalloc/tavern/commit/18c5fd539425384b6f0901f5f2513f9dffa04cf4))

- update README with expanded feature list ([94840a0](https://github.com/omalloc/tavern/commit/94840a0b95c19330a5d5fb01bce0fa1ee3737720))


### Fixed

- pathtrie typo ([6eb1355](https://github.com/omalloc/tavern/commit/6eb1355efd6c714f067f0e6640842d7f3b3b8720))

- update object size  to use Content-Range when available and set Range header for HEAD requests ([4accc28](https://github.com/omalloc/tavern/commit/4accc28fb86b34a65f956d68aaf0955a278a4e6f))

- update end range calculation to use ObjectSize from Content-Range instead of Content-Length ([39a380d](https://github.com/omalloc/tavern/commit/39a380d3d8b2f38bc42f11f9fae24f42f9609c37))

- cast totalSize to uint64 in ContentRange test for type consistency ([5b99300](https://github.com/omalloc/tavern/commit/5b993006d88f97901647f8320e31c1c1e9cd98aa))

- closer conn check if the request is chunked and ensure that read are not at EOF, drop index metadata. ([#30](https://github.com/omalloc/tavern/pull/30))

- use request context in workerRequest cloning ([b91bc02](https://github.com/omalloc/tavern/commit/b91bc02c2848c3cf43f6ec5ced2a2c064e478aa4))

- reorder import statements and uncomment sleep duration in Do method ([#28](https://github.com/omalloc/tavern/pull/28))

- int type, and test flag count=1 ([7018736](https://github.com/omalloc/tavern/commit/701873660b300063a68049066df47087a6fdbc56))

- add e2e test config ([8ec6133](https://github.com/omalloc/tavern/commit/8ec61331b37df8207b8d7b164743348068402212))

- path to config file in Server step ([676d860](https://github.com/omalloc/tavern/commit/676d860b90fd74b232c5b78b3550ec9496b57be3))

- disable caching for error response codes ([a5e80d7](https://github.com/omalloc/tavern/commit/a5e80d75e249a4a974258a44083760dd76e28339))

- errcode skip caching and skip Method not HEAD/GET request ([a790f67](https://github.com/omalloc/tavern/commit/a790f67493890e81f0fd7e61221bc833853edd8f))

- disk_test fix ([a699574](https://github.com/omalloc/tavern/commit/a699574d024f14aa7a3e23b85afbe995203c9dbc))

- address code review comments - fix errors, nil checks, and goroutine leak ([#22](https://github.com/omalloc/tavern/pull/22))

- handle EOF error in io.Copy during response processing #21 ([8e16950](https://github.com/omalloc/tavern/commit/8e16950e54fc3bad453ff38aef8d1992f66fff1b))

- remove accept-encoding header when proxy request  Fix #15 ([#16](https://github.com/omalloc/tavern/pull/16))

- preserve request copy headers to proxy request #11 ([e26fb10](https://github.com/omalloc/tavern/commit/e26fb103e0c5521d89513586059a6e04090e5678))

- handle nil response in duplicate request handling logic #6 ([#9](https://github.com/omalloc/tavern/pull/9))

- remove caching obj pool. ([d9aa660](https://github.com/omalloc/tavern/commit/d9aa660a17e4f0554af231171cf085b629c0b91f))

- clone() lost field `BlockSize`. ([164a234](https://github.com/omalloc/tavern/commit/164a234f62590b5fb17f8729e994cfe93ea742d7))

- prevent duplicate calls to onClose in savepartReader #3 ([f8924e9](https://github.com/omalloc/tavern/commit/f8924e9c6a0eb4209ec32d80d068d6b8f2d6d315))

- .HasComplete() use BlockSize metadata to cache object ([b274a61](https://github.com/omalloc/tavern/commit/b274a616fc87140b23b075ed0ffe2a8e7dc50952))

- correct method name typos and introduce revalidation processor ([d35658d](https://github.com/omalloc/tavern/commit/d35658d39705711d0eceacc3717816c6b931cf80))

- listen serve addr ([73da3ed](https://github.com/omalloc/tavern/commit/73da3ed631d68d4282290898705b604445d423b2))

[Unreleased]: https://github.com/omalloc/tavern/compare/v1.1.1...HEAD
[1.1.1]: https://github.com/omalloc/tavern/compare/v1.1.0...v1.1.1
[1.1.0]: https://github.com/omalloc/tavern/compare/v1.0.0...v1.1.0
[1.0.0]: https://github.com/omalloc/tavern/compare/...v1.0.0

