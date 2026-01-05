package example

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/goccy/go-json"
	"github.com/kelindar/bitmap"
	configv1 "github.com/omalloc/tavern/api/defined/v1/plugin"
	"github.com/omalloc/tavern/api/defined/v1/storage/object"
	"github.com/omalloc/tavern/contrib/log"
	"github.com/omalloc/tavern/plugin"
	"github.com/omalloc/tavern/storage"
)

var _ configv1.Plugin = (*ExamplePlugin)(nil)

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
	return next
}

// AddRouter implements plugin.Plugin.
func (e *ExamplePlugin) AddRouter(router *http.ServeMux) {
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

	// 本设备的所有缓存信息，精简版
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
			// 当数字不连续时，处理之前的范围
			if start == prev {
				result = append(result, fmt.Sprintf("%d", start))
			} else {
				result = append(result, fmt.Sprintf("%d-%d", start, prev))
			}
			start = nums[i]
		}
		prev = nums[i]
	}

	// 处理最后一个范围
	if start == prev {
		result = append(result, fmt.Sprintf("%d", start))
	} else {
		result = append(result, fmt.Sprintf("%d-%d", start, prev))
	}

	return strings.Join(result, ",")
}
