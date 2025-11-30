package plugin

import (
	"fmt"
	"strings"

	configv1 "github.com/omalloc/tavern/api/defined/v1/plugin"
	"github.com/omalloc/tavern/contrib/log"
	"github.com/omalloc/tavern/internal/constants"
)

type Registry interface {
	// Register registers a plugin factory with the given name.
	Register(name string, factory Factory)
	// Create creates a plugin instance using the factory associated with the given name.
	Create(opt configv1.Option, log *log.Helper) (configv1.Plugin, error)
}

type pluginRegistry struct {
	plugins map[string]Factory
}

// Create implements Registry.
func (p *pluginRegistry) Create(opt configv1.Option, log *log.Helper) (configv1.Plugin, error) {
	n := fmtName(opt.PluginName())

	factory, exists := p.plugins[n]
	if !exists {
		return nil, fmt.Errorf("plugin %s not registered", n)
	}
	return factory(opt, log)
}

// Register implements Registry.
func (p *pluginRegistry) Register(name string, factory Factory) {
	n := fmtName(name)
	if _, exists := p.plugins[n]; exists {
		log.Warnf("plugin %s already registered", n)
		return
	}

	p.plugins[n] = factory
}

func NewRegistry() Registry {
	return &pluginRegistry{
		plugins: make(map[string]Factory),
	}
}

func fmtName(name string) string {
	return strings.ToLower(fmt.Sprintf("%s.plugin.%s", constants.AppName, name))
}
