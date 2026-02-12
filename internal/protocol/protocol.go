package protocol

const AppName = "tavern"

// define gw->backend Protocol constants
const (
	ProtocolRequestIDKey     = "X-Request-ID"
	ProtocolCacheStatusKey   = "X-Cache"
	ProtocolForceStoreMemory = "X-FS-Mem"

	PrefetchCacheKey = "X-Prefetch"
	CacheTime        = "X-CacheTime"

	InternalTraceKey         = "i-xtrace"
	InternalStoreUrl         = "i-x-store-url"
	InternalSwapfile         = "i-x-swapfile"
	InternalFillRangePercent = "i-x-fp"
	InternalCacheErrCode     = "i-x-ct-code"
	InternalUpstreamAddr     = "i-x-ups-addr"
)

// define flag constants
const (
	FlagOn  = "1" // gateway control flag ON
	FlagOff = "0" // gateway control flag OFF
)
