package plugin

import (
	configv1 "github.com/omalloc/tavern/api/defined/v1/plugin"
	"github.com/omalloc/tavern/contrib/log"
)

type Factory func(c configv1.Option, log *log.Helper) (configv1.Plugin, error)

var globalRegistry = NewRegistry()

func Register(name string, f Factory) {
	globalRegistry.Register(name, f)
}

func Create(opt configv1.Option, log *log.Helper) (configv1.Plugin, error) {
	return globalRegistry.Create(opt, log)
}
