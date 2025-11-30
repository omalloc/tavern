package main

import (
	"flag"
	"os"
	"strings"
	"time"

	"github.com/cloudflare/tableflip"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"

	pluginv1 "github.com/omalloc/tavern/api/defined/v1/plugin"
	"github.com/omalloc/tavern/conf"
	"github.com/omalloc/tavern/contrib/config"
	"github.com/omalloc/tavern/contrib/config/provider/file"
	"github.com/omalloc/tavern/contrib/kratos"
	"github.com/omalloc/tavern/contrib/log"
	"github.com/omalloc/tavern/contrib/transport"
	"github.com/omalloc/tavern/pkg/encoding"
	"github.com/omalloc/tavern/pkg/encoding/json"
	"github.com/omalloc/tavern/plugin"
	"github.com/omalloc/tavern/server"
	_ "github.com/omalloc/tavern/server/middleware/caching"
	_ "github.com/omalloc/tavern/server/middleware/recovery"
	_ "github.com/omalloc/tavern/server/middleware/rewrite"
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
	flag.StringVar(&flagConf, "c", "config.yaml", "config file path")
	flag.BoolVar(&flagVerbose, "v", false, "enable verbose log")

	// init global encoding
	encoding.SetDefaultCodec(json.JSONCodec{})

	// init logger
	log.SetLogger(log.With(log.DefaultLogger, "ts", log.Timestamp(time.RFC3339), "pid", os.Getpid()))

	// init prometheus
	prometheus.Unregister(collectors.NewGoCollector())
	registerer := prometheus.WrapRegistererWithPrefix("tr_tavern_", prometheus.DefaultRegisterer)
	registerer.MustRegister(collectors.NewGoCollector(collectors.WithGoCollectorMemStatsMetricsDisabled()))
}

func main() {
	flag.Parse()

	c := config.New[conf.Bootstrap](config.WithSource(file.NewSource(flagConf)))
	defer c.Close()

	bc := &conf.Bootstrap{}
	if err := c.Scan(bc); err != nil {
		log.Fatal(err)
	}

	log.Debugf("conf = %#+v", bc)

	app, err := newApp(bc)
	if err != nil {
		log.Fatal(err)
	}

	if err := app.Run(); err != nil {
		log.Fatal(err)
	}
}

func newApp(bc *conf.Bootstrap) (*kratos.App, error) {
	stopTimeout := 120 * time.Second

	// graceful upgrade
	flip, err := tableflip.New(tableflip.Options{
		PIDFile:        bc.PidFile,
		UpgradeTimeout: stopTimeout,
	})
	if err != nil {
		panic(err)
	}

	// graceful upgrade if we have not parent process
	// remove unix socket file.
	if !flip.HasParent() {
		if strings.HasSuffix(bc.Server.Addr, ".sock") {
			_ = os.Remove(bc.Server.Addr) // remove unix socket
		}
	}
	// load plugin
	plugins := loadPlugin(log.DefaultLogger, bc)

	// trasnport server
	servers := make([]transport.Server, 0)

	srv := server.NewServer(flip, plugins)
	servers = append(servers, srv)

	for _, plugin := range plugins {
		servers = append(servers, plugin)
	}

	return kratos.New(
		kratos.ID(id),
		kratos.Name("tavern"),
		kratos.Version(Version),
		kratos.StopTimeout(stopTimeout),
		kratos.Logger(log.DefaultLogger),
		kratos.Server(servers...),
	), nil
}

func loadPlugin(logger log.Logger, bc *conf.Bootstrap) []pluginv1.Plugin {
	ctxlog := log.NewHelper(logger)

	plugins := make([]pluginv1.Plugin, 0, len(bc.Plugin))
	for _, plug := range bc.Plugin {
		instance, err := plugin.Create(plug, ctxlog)
		if err != nil {
			ctxlog.Errorf("load plugin %s failed: %v", plug.Name, err)
			continue
		}
		ctxlog.Debugf("plugin %s loaded", plug.PluginName())
		plugins = append(plugins, instance)
	}
	return plugins
}
