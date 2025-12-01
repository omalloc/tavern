package indexdb

import (
	"fmt"
	"strings"

	"github.com/omalloc/tavern/api/defined/v1/storage"
	"github.com/omalloc/tavern/contrib/log"
)

type Registry struct {
	registry map[string]storage.IndexDBFactory
}

func NewRegistry() *Registry {
	return &Registry{
		registry: make(map[string]storage.IndexDBFactory),
	}
}

func (r *Registry) Register(name string, factory storage.IndexDBFactory) {
	r.registry[createTypedName(name)] = factory
}

func (r *Registry) Create(name string, option storage.Option) (storage.IndexDB, error) {
	factory, ok := r.registry[createTypedName(name)]
	if !ok {
		return nil, fmt.Errorf("db type %s not registered", name)
	}

	log.Debugf("creating indexdb %s in path %s", name, option.DBPath())
	return factory(option.DBPath(), option)
}

func Register(name string, factory storage.IndexDBFactory) {
	defaultRegistry.Register(name, factory)
}

func Create(name string, option storage.Option) (storage.IndexDB, error) {
	return defaultRegistry.Create(name, option)
}

func createTypedName(name string) string {
	return fmt.Sprintf("tavern.indexdb.%s", strings.ToLower(name))
}
