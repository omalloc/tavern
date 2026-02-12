## custom internal protocol defined

### ProtocolRequestIDKey

Set request id

### X-FS-Mem

force store in memory

### ProtocolCacheStatusKey

Response cache status with name, like X-Cache: HIT from memory

### PrefetchCacheKey

Prefetch cache, value 1 mean prefetch, 0 mean not prefetch

### CacheTime

Set force `Cache-Control` Cache time, value is seconds, like `CacheTime: 60` mean `Cache-Control: max-age=60`

### InternalTraceKey

Internal trace key, value is 1 or 0, 1 mean enable trace, 0 mean disable trace

### InternalStoreUrl

Set force store-url, value is string, like `http://example.com/somepath/app.apk`

### InternalSwapfile

Show response swapfile info, debug now!

### InternalFillRangePercent

Set fill range percent, value is int, like `InternalFillRangePercent: 50` mean fill 50% of response

### InternalCacheErrCode

Set force cache error code, value is int, like `InternalCacheErrCode: 1` mean force cache errcode like > 400

### InternalUpstreamAddr

Dynamic set upstream addr, value is string, like `InternalUpstreamAddr: [IP_ADDRESS]`
