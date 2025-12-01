package proxy

import "github.com/omalloc/proxy/selector"

var (
	globalProxy Proxy         = nil
	loopback    selector.Node = nil
)

func SetDefault(proxy Proxy) {
	globalProxy = proxy
}

func GetProxy() Proxy {
	return globalProxy
}

func SetLoopback(let selector.Node) {
	loopback = let
}

func GetLoopback() selector.Node {
	return loopback
}
