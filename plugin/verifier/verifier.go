package verifier

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"hash/crc32"
	"net"
	"net/http"
	"net/http/httputil"
	"strconv"
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
}

func init() {
	plugin.Register("verifier", NewVerifierPlugin)
}

func NewVerifierPlugin(opts pluginv1.Option, log *log.Helper) (pluginv1.Plugin, error) {
	opt := &verifierOptions{
		Endpoint:    "http://verifier.default.svc.cluster.local:8080/report",
		Timeout:     5,
		ReportRatio: 1, // percent 1%
	}

	if err := opts.Unmarshal(opt); err != nil {
		return nil, err
	}

	log.Debugf("load config %#+v", opt)

	return &verifier{
		reportClient: &http.Client{
			Timeout: time.Second * time.Duration(opt.Timeout),
			Transport: &http.Transport{
				Proxy: http.ProxyFromEnvironment,
				DialContext: (&net.Dialer{
					Timeout:   time.Second * time.Duration(opt.Timeout),
					KeepAlive: 30 * time.Second,
				}).DialContext,
			},
		},
		opt: opt,
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

	go v.handleEvent()

	return nil
}

// Stop implements [plugin.Plugin].
func (v *verifier) Stop(context.Context) error {
	return nil
}

func (v *verifier) handleEvent() {
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

	upsRatio := payload.ReportRatio()

	// disable report
	if upsRatio == -1 {
		log.Debugf("verifier report disabled for store key: %s", payload.StoreKey())
		return
	}

	// use plugin config `ratio``
	if upsRatio == 0 {
		upsRatio = v.opt.ReportRatio
	}

	// check ratio has skip
	if ratio >= upsRatio {
		log.Debugf("skip report verifier for store key: %s", payload.StoreKey())
		return
	}

	// calculate file md5 hash
	hash, err := ReadAndSumHash(payload.StorePath(), payload.StoreKey(), payload.ChunkCount(), payload.ChunkSize())
	if err != nil {
		log.Errorf("check cache-file xxhash failed %v", err)
		return
	}

	reportData := ReportPayload{
		Url:  payload.StoreUrl(),
		Lm:   payload.LastModified(),
		Cl:   uint64(payload.ContentLength()),
		Hash: hash,
	}

	if err := v.doReport(reportData); err != nil {
		log.Errorf("report verifier failed: %v", err)
		return
	}

	log.Infof("report verifier success for url: %s", payload.StoreUrl())
}

func (v *verifier) doReport(payload ReportPayload) error {

	buf, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal report payload failed: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second*time.Duration(v.opt.Timeout))
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, v.opt.Endpoint, bytes.NewReader(buf))
	if err != nil {
		return fmt.Errorf("create report request failed: %w", err)
	}
	defer req.Body.Close()

	// add headers
	req.Header.Set("Authorization", v.opt.ApiKey)
	req.Header.Set("Content-Type", "application/json")

	if log.Enabled(log.LevelDebug) {
		dump, _ := httputil.DumpRequest(req, true)
		log.Debugf("dump verifier request: %s", dump)
	}

	// do request to verifier center.
	resp, err := v.reportClient.Do(req)
	if err != nil {
		_metricVerifierRequestsTotal.WithLabelValues("0").Inc()
		return fmt.Errorf("send report request failed: %w", err)
	}
	defer func() {
		if resp.Body != nil {
			_ = resp.Body.Close()
		}

		_metricVerifierRequestsTotal.WithLabelValues(strconv.Itoa(resp.StatusCode)).Inc()
	}()

	if resp.StatusCode == http.StatusConflict {
		return fmt.Errorf("report verifier CRC hash conflict: %s", resp.Status)
	}

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("report verifier result failed: %s", resp.Status)
	}

	return nil
}

type ReportPayload struct {
	Url  string `json:"url"`
	Hash string `json:"hash"`
	Lm   string `json:"lm"`
	Cl   uint64 `json:"cl"`
}
