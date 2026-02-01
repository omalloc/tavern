package watchdog

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"

	pluginv1 "github.com/omalloc/tavern/api/defined/v1/plugin"
	"github.com/omalloc/tavern/contrib/log"
	"github.com/omalloc/tavern/metrics"
	"github.com/omalloc/tavern/plugin"
	"github.com/prometheus/client_golang/prometheus"
)

var _ pluginv1.Plugin = (*watchDogPlugin)(nil)

type watchDogPlugin struct {
	mu           sync.RWMutex
	smoothedData map[string]float64 // 存储平滑后的状态码指标
}

func init() {
	plugin.Register("watchdog", New)
}

func New(c pluginv1.Option, log *log.Helper) (pluginv1.Plugin, error) {
	return &watchDogPlugin{
		smoothedData: make(map[string]float64),
	}, nil
}

// Start implements [plugin.Plugin].
func (w *watchDogPlugin) Start(context.Context) error {

	go w.tick()

	return nil
}

// Stop implements [plugin.Plugin].
func (w *watchDogPlugin) Stop(context.Context) error {
	return nil
}

// AddRouter implements [plugin.Plugin].
func (p *watchDogPlugin) AddRouter(router *http.ServeMux) {
	router.Handle("/plugin/graph", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 从 tick 数据源获取平滑后的指标
		p.mu.RLock()
		data := make(map[string]float64, len(p.smoothedData))
		for code, smoothedValue := range p.smoothedData {
			switch code {
			case "total":
				data["total"] = smoothedValue
			case "200", "206":
				data["2xx"] += smoothedValue
			case "400", "401", "403", "404":
				data["4xx"] += smoothedValue
			case "499":
				data["499"] += smoothedValue
			case "500", "502", "503", "504":
				data["5xx"] += smoothedValue
			}
		}
		p.mu.RUnlock()

		buf, err := json.Marshal(data)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(buf)
	}))
}

// HandleFunc implements [plugin.Plugin].
func (w *watchDogPlugin) HandleFunc(next http.HandlerFunc) http.HandlerFunc {
	return next
}

func (p *watchDogPlugin) tick() {
	metricsMap := map[string]*metrics.CounterSmoother{
		"200": {Alpha: 0.3},
		"206": {Alpha: 0.3},
		"400": {Alpha: 0.3},
		"401": {Alpha: 0.3},
		"403": {Alpha: 0.3},
		"404": {Alpha: 0.3},
		"499": {Alpha: 0.3},
		"500": {Alpha: 0.3},
		"502": {Alpha: 0.3},
		"503": {Alpha: 0.3},
		"504": {Alpha: 0.3},
	}

	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	for range ticker.C {
		familys, err := prometheus.DefaultGatherer.Gather()
		if err != nil {
			continue
		}

		// 临时存储本次收集的平滑值
		tempData := make(map[string]float64)
		totalCounter := float64(0)
		for _, mf := range familys {
			if mf.GetName() == "tr_tavern_requests_code_total" {
				for _, metric := range mf.GetMetric() {
					for _, label := range metric.Label {
						if label.GetName() == "code" {
							code := label.GetValue()
							val := metric.GetCounter().GetValue()
							totalCounter += val
							if smoother, ok := metricsMap[code]; ok {
								smoothedValue := smoother.Update(val)
								tempData[code] = smoothedValue
							}
						}
					}
				}
			}
		}

		// 使用写锁更新共享数据
		p.mu.Lock()
		for code, value := range tempData {
			p.smoothedData[code] = value
		}
		p.smoothedData["total"] = totalCounter
		p.mu.Unlock()
	}
}
