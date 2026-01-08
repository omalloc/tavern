package verifier

import (
	"bytes"
	"context"
	"encoding/json"
	"hash"
	"hash/crc32"
	"net/http"
	"time"

	"github.com/omalloc/tavern/api/defined/v1/event"
	pluginv1 "github.com/omalloc/tavern/api/defined/v1/plugin"
	"github.com/omalloc/tavern/contrib/log"
	"github.com/omalloc/tavern/plugin"
)

var _ pluginv1.Plugin = (*verifier)(nil)

type ReportClient interface {
	Do() error
}

type verifierOptions struct {
	Endpoint    string `json:"endpoint"`
	Timeout     int    `json:"timeout"`
	ReportRatio int    `json:"report_ratio"`
	ApiKey      string `json:"api_key"`
}

type verifier struct {
	reportClient *http.Client
	opt          *verifierOptions
	crc          hash.Hash32
}

func init() {
	plugin.Register("verifier", NewVerifierPlugin)
}

func NewVerifierPlugin(opts pluginv1.Option, log *log.Helper) (pluginv1.Plugin, error) {
	opt := &verifierOptions{
		Endpoint:    "http://verifier.default.svc.cluster.local:8080/report",
		Timeout:     5,
		ReportRatio: 1,
	}

	if err := opts.Unmarshal(opt); err != nil {
		return nil, err
	}

	log.Debugf("load config %#+v", opt)

	return &verifier{
		reportClient: &http.Client{},
		opt:          opt,
		crc:          crc32.NewIEEE(),
	}, nil
}

// AddRouter implements [plugin.Plugin].
func (v *verifier) AddRouter(router *http.ServeMux) {
}

// HandleFunc implements [plugin.Plugin].
func (v *verifier) HandleFunc(next http.HandlerFunc) http.HandlerFunc {
	return nil
}

// Start implements [plugin.Plugin].
func (v *verifier) Start(context.Context) error {

	go v.listen()

	return nil
}

// Stop implements [plugin.Plugin].
func (v *verifier) Stop(context.Context) error {
	return nil
}

func (v *verifier) listen() {
	topic := event.NewTopicKey[event.CacheCompleted]("cache.completed")

	if err := event.Subscribe(topic, v.eventLoop); err != nil {
		log.Errorf("handle event cache.completed failed: %v", err)
	}
}

func (v *verifier) eventLoop(_ context.Context, payload event.CacheCompleted) {
	log.Debugf("receive cache.completed event: %+v", payload)

	// check report ratio
	hashCrc := crc32.ChecksumIEEE([]byte(payload.StoreKey()))
	ratio := int(hashCrc % 100)

	if ratio >= v.opt.ReportRatio {
		log.Debugf("skip report verifier for store key: %s", payload.StoreKey())
		return
	}

	// calculate file md5 hash
	reportPayload := ReportPayload{
		Url:  payload.StoreUrl(),
		Lm:   payload.LastModified(),
		Cl:   uint64(payload.ContentLength()),
		Hash: "CRC-32-PLACEHOLDER", // readFile() and calculate md5
	}

	buf, err := json.Marshal(reportPayload)
	if err != nil {
		log.Errorf("marshal report payload failed: %v", err)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second*time.Duration(v.opt.Timeout))
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, v.opt.Endpoint, bytes.NewReader(buf))
	if err != nil {
		log.Errorf("create report request failed: %v", err)
		return
	}
	defer req.Body.Close()

	req.Header.Set("Authorization", v.opt.ApiKey)
	req.Header.Set("Content-Type", "application/json")

	// do request to verifier center.
	resp, err := v.reportClient.Do(req)
	if err != nil {
		log.Errorf("send report request failed: %v", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusConflict {
		log.Errorf("report verifier CRC hash conflict: %s; PURGE ALL NODE", resp.Status)
		return
	}

	if resp.StatusCode != http.StatusOK {
		log.Errorf("report verifier result failed: %s", resp.Status)
		return
	}

	log.Debugf("report verifier result success: %s", resp.Status)
}

type ReportPayload struct {
	Url  string `json:"url"`
	Hash string `json:"hash"`
	Lm   string `json:"lm"`
	Cl   uint64 `json:"cl"`
}
