package plugin

import (
	"net/http"

	"github.com/omalloc/tavern/contrib/transport"
)

type Plugin interface {
	transport.Server

	AddRouter(router *http.ServeMux)

	HandleFunc(next http.HandlerFunc) http.HandlerFunc
}

type Option interface {
	PluginName() string    // plugin name
	Unmarshal(v any) error // plugin config unmarshal
}
