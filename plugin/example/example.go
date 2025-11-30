package example

import (
	"context"
	"net/http"

	configv1 "github.com/omalloc/tavern/api/defined/v1/plugin"
	"github.com/omalloc/tavern/contrib/log"
	"github.com/omalloc/tavern/plugin"
)

var _ configv1.Plugin = (*ExamplePlugin)(nil)

type option struct {
	Option1 string `json:"option1"`
	Option2 int    `json:"option2"`
}

type ExamplePlugin struct {
	log *log.Helper
	opt *option
}

func init() {
	plugin.Register("example-plugin", NewExamplePlugin)
}

func NewExamplePlugin(opts configv1.Option, log *log.Helper) (configv1.Plugin, error) {
	opt := &option{}
	if err := opts.Unmarshal(opt); err != nil {
		return nil, err
	}
	return &ExamplePlugin{
		log: log,
		opt: opt,
	}, nil
}

// HandleFunc implements plugin.Plugin.
func (e *ExamplePlugin) HandleFunc(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		next(w, r)
	}
}

// ServeHTTP implements plugin.Plugin.
func (e *ExamplePlugin) AddRouter(router *http.ServeMux) {

}

// Start implements plugin.Plugin.
func (e *ExamplePlugin) Start(context.Context) error {
	// you can add your startup logic here

	// e.g.
	//
	// go func() {
	//     // do something
	// }()
	return nil
}

// Stop implements plugin.Plugin.
func (e *ExamplePlugin) Stop(context.Context) error {
	// you can add your cleanup logic here

	// e.g.
	//
	// stopCh <- struct{}{}
	return nil
}
