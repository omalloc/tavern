# Changelog

All notable changes to this project will be documented in this file. See [standard-version](https://github.com/conventional-changelog/standard-version) for commit guidelines.

## [1.1.0](https://github.com/omalloc/tavern/compare/v1.0.0...v1.1.0) (2026-04-23)


### Features

* Add `Touch` method to storage buckets for updating object access metadata and integrate it into the caching middleware. ([4c8a3b8](https://github.com/omalloc/tavern/commit/4c8a3b8baf80d3a9e61ebd916e0bfeff4ce62193))
* add Grafana dashboard template.json ([915cd93](https://github.com/omalloc/tavern/commit/915cd9326b83d0e5356c3183819e7083880f2a31))
* add protocol code generation and update caching logic with new protocol constants ([684e623](https://github.com/omalloc/tavern/commit/684e623bd35a5e9affad893b64b52382b4fef65f))
* enhance `TopK` output with object details and update `top` command display logic, and rename `qs` plugin API paths ([9cfc0b2](https://github.com/omalloc/tavern/commit/9cfc0b2741bd2be4124bb7b35fffd9be950d6ab7))
* implement object migration for memory and disk buckets and correct DBPath in disk migration tests. ([fe8e7ce](https://github.com/omalloc/tavern/commit/fe8e7ce42325654f3ac716d9c5fdee784b67083d))
* implement real-time monitoring for QS plugin via SSE and introduce `ttop` command with enhanced hot key details. ([df37a4c](https://github.com/omalloc/tavern/commit/df37a4ce327a3bdb0c88935f3add8f8306936610))
* Implement smoothed requests per second (RPS) calculation and display in the top command, and improve plugin shutdown logic. ([016bec8](https://github.com/omalloc/tavern/commit/016bec832717f962712d5519dbc655de8e489993))
* implement TopK functionality for LRU cache and storage buckets, integrating it into the QS plugin for hot URL tracking and on-demand metric collection. ([c14ff51](https://github.com/omalloc/tavern/commit/c14ff519466d3149e609b4d64856a090082f4b98))
* Introduce flag constants and enable conditional caching of error responses using `FlagOn`. ([afd055d](https://github.com/omalloc/tavern/commit/afd055d14a2c63438927b6f915d5ef0138e9101e))
* introduce iobuf.NopCloser for optional close operations, update dependencies ([943cead](https://github.com/omalloc/tavern/commit/943cead1c6077bbe427ec1de2611505454a3ef46))
* introduce storage migration functionality with configurable promote and demote policies. ([8fc91b6](https://github.com/omalloc/tavern/commit/8fc91b602b835468ab3b2969b1e0b8b75cbf5aec))
* **metrics:** add CPU and memory usage metrics to QsPlugin ([b8db396](https://github.com/omalloc/tavern/commit/b8db396ab7b3b872b7209354237bf1f4d948c1ab))
* **plugin:** implement request smoothing and add metrics endpoint ([f4623a8](https://github.com/omalloc/tavern/commit/f4623a89382ea23ddd79b8112bab3584cdbd6215))
* **plugin:** replace example-plugin with qs-plugin and update related functionality ([b188143](https://github.com/omalloc/tavern/commit/b188143c250bbfaac375944833b29d3b9a85bd4b))
* promote support ([21b0615](https://github.com/omalloc/tavern/commit/21b061575a43d33adb78befe6ae3f6ac615dbb9d))
* **purge:** add Prometheus metrics for purge request tracking ([6434971](https://github.com/omalloc/tavern/commit/6434971cdb4ffbfe2fbdcca309f8b17883e6f2bd))
* **systemd:** add Tavern service unit file for systemd integration ([0358581](https://github.com/omalloc/tavern/commit/03585815edc27b2db60445f399f64156ad03fed6))
* **verifier:** replace md5 with xxhash ([36378e1](https://github.com/omalloc/tavern/commit/36378e1f7b009cb3a0c8f5d558c36d2e0b4233f9))


### Bug Fixes

* (Breaking Change) rename `normal` storage type to `warm` ([4215c81](https://github.com/omalloc/tavern/commit/4215c819194a1269409bd05acc5662647aac162f))
* caching middleware's partRequest getUpstreamReader range request preparation. ([ce4d992](https://github.com/omalloc/tavern/commit/ce4d9926d41658ba11417fe0552d727bbb001316))
* **caching:** refactor getContents to improve find last hit block index. ([8b7f3f1](https://github.com/omalloc/tavern/commit/8b7f3f15d9d6b71df364b4d21a1b630a50b2d20a))
* **caching:** refactor savepart async reader, and chunkWriter close check ([d434abc](https://github.com/omalloc/tavern/commit/d434abc48a0f55af8202b967514f96a717b24197))
* close file descriptors in getContents error paths to prevent fd leaks ([392219c](https://github.com/omalloc/tavern/commit/392219c7d86f2f03ee338dd6dce75bb1f753a5f0))
* Ensure response body is always closed in async revalidation.  fix [#46](https://github.com/omalloc/tavern/issues/46) ([d51c0bf](https://github.com/omalloc/tavern/commit/d51c0bfce205cb249c691ce8e814a400b0ab4cd6))
* pass correct chunk index to checkChunkSize in getContents ([7c7c5a0](https://github.com/omalloc/tavern/commit/7c7c5a0ede3cfea984eae217fd5dfad9c74b9551))
* PebbleDB and noneSharedKV Close idempotent using `sync.Once`. fix hot upgrade process ([8788aca](https://github.com/omalloc/tavern/commit/8788aca58bc2d6cd98b0c8b3e14ecdafee0207fd))
* test case config apply `migration` ([34a8e28](https://github.com/omalloc/tavern/commit/34a8e280151952ee7fbc4696f3e1ccade52fd5c3))
* **test:** add iobuf testcase ([fdff4dd](https://github.com/omalloc/tavern/commit/fdff4dd6145fbf01aa88b8e2ab404e493323b872))
* **test:** lru coverage; remove kv_pebble log ([5208c71](https://github.com/omalloc/tavern/commit/5208c715ac45ebd73dd785e4ff24b7710dd65ffa))
