package qs

import (
	"context"
	"encoding/binary"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/goccy/go-json"
	"github.com/kelindar/bitmap"
	configv1 "github.com/omalloc/tavern/api/defined/v1/plugin"
	"github.com/omalloc/tavern/api/defined/v1/storage/object"
	"github.com/omalloc/tavern/contrib/log"
	"github.com/omalloc/tavern/metrics"
	"github.com/omalloc/tavern/plugin"
	"github.com/omalloc/tavern/storage"
	"github.com/prometheus/client_golang/prometheus"
)

var _ configv1.Plugin = (*QsPlugin)(nil)

type SimpleMetadata struct {
	ID       string    `json:"id"`
	Chunks   string    `json:"chunks,omitempty"`
	Code     int       `json:"code"`
	Size     uint64    `json:"size"`
	RespUnix time.Time `json:"resp_unix"`
	Expired  time.Time `json:"expired"`
	Flags    string    `json:"flags"`
	CacheRef int64     `json:"cache_ref"`
	Vd       []string  `json:"varykey,omitempty"`
}

type option struct {
	Option1 string `json:"option1"`
	Option2 int    `json:"option2"`
}

type QsPlugin struct {
	log *log.Helper
	opt *option

	mu           sync.RWMutex
	stopCh       chan struct{}
	smoothedData map[string]float64 // 存储平滑后的状态码指标
}

func init() {
	plugin.Register("qs-plugin", NewQsPlugin)
}

func NewQsPlugin(opts configv1.Option, log *log.Helper) (configv1.Plugin, error) {
	opt := &option{}
	if err := opts.Unmarshal(opt); err != nil {
		return nil, err
	}
	return &QsPlugin{
		log:          log,
		opt:          opt,
		stopCh:       make(chan struct{}, 1),
		smoothedData: make(map[string]float64),
	}, nil
}

// HandleFunc implements plugin.Plugin.
func (qs *QsPlugin) HandleFunc(next http.HandlerFunc) http.HandlerFunc {
	return next
}

// AddRouter implements plugin.Plugin.
func (qs *QsPlugin) AddRouter(router *http.ServeMux) {
	router.Handle("/plugin/store/disk", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buckets := storage.Current().Buckets()

		bucketObjectCounter := make(map[string]uint64, len(buckets))
		for _, bucket := range buckets {
			bucketObjectCounter[bucket.ID()] = bucket.Objects()
		}

		payload, _ := json.Marshal(bucketObjectCounter)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(payload)))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(payload)
	}))

	router.Handle("/plugin/store/object/simple", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		getHash := r.URL.Query().Get("hash") != ""

		buckets := storage.Current().Buckets()
		objects := make([]*SimpleMetadata, 0)
		for _, bucket := range buckets {
			_ = bucket.Iterate(context.Background(), func(obj *object.Metadata) error {
				var vd []string
				if obj.IsVary() {
					vd = obj.VirtualKey
				}

				md := &SimpleMetadata{
					ID:       obj.ID.Key(),
					Chunks:   convRange(obj.Chunks),
					Code:     obj.Code,
					Size:     obj.Size,
					RespUnix: time.Unix(obj.RespUnix, 0),
					Expired:  time.Unix(obj.ExpiresAt, 0),
					CacheRef: obj.Refs,
					Flags:    obj.Flags.String(),
					Vd:       vd,
				}

				if getHash {
					md.ID = obj.ID.HashStr()
				}

				objects = append(objects, md)

				return nil
			})
		}

		payload, _ := json.Marshal(objects)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(payload)))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(payload)
	}))

	// get this device's service domains
	router.Handle("/plugin/store/service-domains", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sharedKV := storage.Current().SharedKV()
		// type map[domain]counter
		domainMap := make(map[string]uint32)
		_ = sharedKV.IteratePrefix(r.Context(), []byte("if/domain"), func(key, val []byte) error {
			if len(key) > 10 {
				domainMap[string(key[10:])] = binary.BigEndian.Uint32(val)
			}
			return nil
		})

		buf, err := json.Marshal(domainMap)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Length", strconv.Itoa(len(buf)))
		w.WriteHeader(http.StatusOK)

		_, _ = w.Write(buf)
	}))

	router.Handle("/plugin/qs/graph", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		qs.mu.RLock()
		data := make(map[string]float64, len(qs.smoothedData))
		for code, smoothedValue := range qs.smoothedData {
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
		qs.mu.RUnlock()

		buf, err := json.Marshal(data)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Length", strconv.Itoa(len(buf)))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(buf)
	}))
}

// Start implements plugin.Plugin.
func (qs *QsPlugin) Start(context.Context) error {
	// you can add your startup logic here

	// start the ticker to collect requests per second metrics ( TODO: with enabled qs/graph endpoint )
	go qs.tickRequestsPerSecond()

	return nil
}

// Stop implements plugin.Plugin.
func (qs *QsPlugin) Stop(context.Context) error {
	// you can add your cleanup logic here

	qs.stopCh <- struct{}{}

	return nil
}

// tickRequestsPerSecond periodically collects and smooths the requests per second metrics.
func (qs *QsPlugin) tickRequestsPerSecond() {
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

	for {
		select {
		case <-qs.stopCh:
			return
		case <-ticker.C:
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
			qs.mu.Lock()
			for code, value := range tempData {
				qs.smoothedData[code] = value
			}
			qs.smoothedData["total"] = totalCounter
			qs.mu.Unlock()
		}
	}
}

func convRange(parts bitmap.Bitmap) string {
	nums := make([]int, 0, parts.Count())
	parts.Range(func(x uint32) {
		nums = append(nums, int(x))
	})

	if len(nums) == 0 {
		return ""
	}

	var result []string
	start := nums[0]
	prev := nums[0]

	for i := 1; i < len(nums); i++ {
		if nums[i] != prev+1 {
			// handle the previous range when numbers are not consecutive
			if start == prev {
				result = append(result, fmt.Sprintf("%d", start))
			} else {
				result = append(result, fmt.Sprintf("%d-%d", start, prev))
			}
			start = nums[i]
		}
		prev = nums[i]
	}

	// last range
	if start == prev {
		result = append(result, fmt.Sprintf("%d", start))
	} else {
		result = append(result, fmt.Sprintf("%d-%d", start, prev))
	}

	return strings.Join(result, ",")
}
