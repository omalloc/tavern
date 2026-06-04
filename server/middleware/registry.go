package middleware

import (
	"errors"
	"strings"

	configv1 "github.com/omalloc/tavern/api/defined/v1/middleware"
	"github.com/omalloc/tavern/contrib/log"
)

var globalRegistry = NewRegistry()

// ErrNotFound is middleware not found.
var ErrNotFound = errors.New("Middleware has not been registered")

type Registry interface {
	Register(name string, factory Factory)
	Create(c *configv1.Middleware) (Middleware, func(), error)
}

type middlewareRegistry struct {
	middleware map[string]Factory
}

// NewRegistry returns a new middleware registry.
func NewRegistry() Registry {
	return &middlewareRegistry{
		middleware: map[string]Factory{},
	}
}

// Register registers one middleware.
func (p *middlewareRegistry) Register(name string, factory Factory) {
	p.middleware[createFullName(name)] = factory
}

func (r *middlewareRegistry) Create(cfg *configv1.Middleware) (Middleware, func(), error) {
	fullname := createFullName(cfg.Name)
	if method, ok := r.getMiddleware(fullname); ok {
		if cfg.Required {
			instance, cleanup, err := method(cfg)
			if err != nil {
				log.Errorw(log.DefaultMessageKey, "Failed to create required middleware", "reason", "create_required_middleware_failed", "name", cfg.Name, "error", err, "config", cfg)
				return nil, nil, err
			}

			log.Debugf("middleware created at %s", fullname)
			return instance, cleanup, nil
		}

		instance, cleanup, err := method(cfg)
		if err != nil {
			log.Errorw(log.DefaultMessageKey, "Error to create middleware", "reason", "create_middleware_failed_but_not_required", "name", cfg.Name, "error", err)
			return EmptyMiddleware, nil, nil
		}

		log.Debugf("use %s", createFullName(cfg.Name))
		return instance, cleanup, nil
	}
	return nil, nil, ErrNotFound
}

func (r *middlewareRegistry) getMiddleware(name string) (Factory, bool) {
	nameLower := strings.ToLower(name)
	middlewareFn, ok := r.middleware[nameLower]
	if ok {
		return middlewareFn, true
	}
	return nil, false
}

// Register registers one middleware.
func Register(name string, factory Factory) {
	globalRegistry.Register(name, factory)
}

// Create instantiates a middleware based on `cfg`.
func Create(c *configv1.Middleware) (Middleware, func(), error) {
	return globalRegistry.Create(c)
}

func createFullName(name string) string {
	return strings.ToLower("tavern.middleware." + name)
}
