package constants

const AppName = "tavern"

// define gw->backend Protocol constants
const (
	ProtocolRequestIDKey   = "X-Request-ID"
	ProtocolCacheStatusKey = "X-Cache"
	PrefetchCacheKey       = "X-Prefetch"
	CacheTime              = "X-CacheTime"

	InternalTraceKey         = "i-xtrace"
	InternalStoreUrl         = "i-x-store-url"
	InternalSwapfile         = "i-x-swapfile"
	InternalFillRangePercent = "i-x-fp"
	InternalCacheErrCode     = "i-x-ct-code"
)
