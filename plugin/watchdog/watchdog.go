package watchdog

import (
	"context"
	"encoding/json"
	"net/http"

	pluginv1 "github.com/omalloc/tavern/api/defined/v1/plugin"
	"github.com/omalloc/tavern/contrib/log"
	"github.com/omalloc/tavern/metrics"
	"github.com/omalloc/tavern/plugin"
)

var _ pluginv1.Plugin = (*watchDogPlugin)(nil)

type watchDogPlugin struct {
}

func init() {
	plugin.Register("watchdog", New)
}

func New(c pluginv1.Option, log *log.Helper) (pluginv1.Plugin, error) {
	return &watchDogPlugin{}, nil
}

// Start implements [plugin.Plugin].
func (w *watchDogPlugin) Start(context.Context) error {
	return nil
}

// Stop implements [plugin.Plugin].
func (w *watchDogPlugin) Stop(context.Context) error {
	return nil
}

// AddRouter implements [plugin.Plugin].
func (w *watchDogPlugin) AddRouter(router *http.ServeMux) {
	router.Handle("/plugin/graph", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 指标输出
		if totals := metrics.CollectorRequestsCodeTotal(); len(totals) > 0 {
			data := make(map[string]float64, len(totals))
			for _, curr := range totals {
				data["total"] += curr.Count

				switch curr.Code {
				case "200", "206":
					data["2xx"] += curr.Count
				case "400", "401", "403", "404":
					data["4xx"] += curr.Count
				case "499":
					data["499"] += curr.Count
				case "500", "502", "503", "504":
					data["5xx"] += curr.Count
				}
			}

			buf, err := json.Marshal(data)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}

			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(buf)
			return
		}
	}))
}

// HandleFunc implements [plugin.Plugin].
func (w *watchDogPlugin) HandleFunc(next http.HandlerFunc) http.HandlerFunc {
	return next
}
