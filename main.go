package main

import (
	"os"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"

	"github.com/omalloc/tavern/contrib/config"
	"github.com/omalloc/tavern/contrib/config/provider/file"
	"github.com/omalloc/tavern/contrib/log"
	"github.com/omalloc/tavern/internal/conf"
	"github.com/omalloc/tavern/pkg/encoding"
	"github.com/omalloc/tavern/pkg/encoding/json"
)

var (
	id, _ = os.Hostname()

	// flagConf is the config flag.
	flagConf string = "config.yaml"
	// flagVerbose is the verbose flag.
	flagVerbose bool

	// Version is the version of the app.
	Version string = "no-set"
	GitHash string = "no-set"
	Built   string = "0"
)

func init() {
	// init flag
	encoding.SetDefaultCodec(json.JSONCodec{})

	// init logger
	log.SetLogger(log.With(log.DefaultLogger, "ts", log.Timestamp(time.RFC3339), "pid", os.Getpid()))

	// init prometheus
	prometheus.Unregister(collectors.NewGoCollector())
	registerer := prometheus.WrapRegistererWithPrefix("tr_tavern_", prometheus.DefaultRegisterer)
	registerer.MustRegister(collectors.NewGoCollector(collectors.WithGoCollectorMemStatsMetricsDisabled()))
}

func main() {
	c := config.New[conf.Bootstrap](config.WithSource(file.NewSource(flagConf)))
	defer c.Close()

	bc := &conf.Bootstrap{}
	if err := c.Scan(bc); err != nil {
		log.Fatal(err)
	}

	log.Debugf("conf = %#+v", bc)
}
