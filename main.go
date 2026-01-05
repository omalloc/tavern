package main

import (
	"context"
	"flag"
	stdlog "log"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/cloudflare/tableflip"
	"github.com/omalloc/proxy/selector"
	"github.com/omalloc/proxy/selector/once"
	"github.com/omalloc/tavern/metrics"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"gopkg.in/natefinch/lumberjack.v2"

	pluginv1 "github.com/omalloc/tavern/api/defined/v1/plugin"
	"github.com/omalloc/tavern/conf"
	"github.com/omalloc/tavern/contrib/config"
	"github.com/omalloc/tavern/contrib/config/provider/file"
	"github.com/omalloc/tavern/contrib/kratos"
	"github.com/omalloc/tavern/contrib/log"
	"github.com/omalloc/tavern/contrib/transport"
	"github.com/omalloc/tavern/pkg/encoding"
	"github.com/omalloc/tavern/pkg/encoding/json"
	"github.com/omalloc/tavern/pkg/x/runtime"
	"github.com/omalloc/tavern/plugin"
	_ "github.com/omalloc/tavern/plugin/example"
	_ "github.com/omalloc/tavern/plugin/purge"
	"github.com/omalloc/tavern/proxy"
	"github.com/omalloc/tavern/server"
	"github.com/omalloc/tavern/storage"
)

var (
	id, _ = os.Hostname()

	// flagConf is the config flag.
	flagConf string = "config.yaml"
	// flagVerbose is the verbose flag.
	flagVerbose bool
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
	prometheus.Unregister(collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))

	registerer := prometheus.WrapRegistererWithPrefix("tr_tavern_", prometheus.DefaultRegisterer)
	registerer.MustRegister(collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	registerer.MustRegister(collectors.NewGoCollector())
}

func main() {
	flag.Parse()

	c := config.New[conf.Bootstrap](config.WithSource(file.NewSource(flagConf)))
	defer c.Close()

	bc := &conf.Bootstrap{}
	if err := c.Scan(bc); err != nil {
		log.Fatal(err)
	}

	logger := newLogger(bc.Logger)
	log.SetLogger(logger)

	app, err := newApp(bc, logger)
	if err != nil {
		log.Fatal(err)
	}

	if err := app.Run(); err != nil {
		log.Fatal(err)
	}
}

func newApp(bc *conf.Bootstrap, logger log.Logger) (*kratos.App, error) {
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

	// init storage
	st, err := storage.New(bc.Storage, log.GetLogger())
	if err != nil {
		log.Fatalf("failed to initialize storage: %v", err)
	}
	storage.SetDefault(st)

	// init upstream
	nodes := make([]selector.Node, 0, len(bc.Upstream.Address))
	for _, addr := range bc.Upstream.Address {
		u, err := url.Parse(addr)
		if err != nil {
			log.Errorf("parsed upstream.address failed %v", err)
			continue
		}
		log.Infof("add upstream scheme: %s, host: %s", u.Scheme, u.Host)
		nodes = append(nodes, selector.NewNode(u.Scheme, u.Host, selector.RawMetadata("weight", "1")))
	}
	proxy.SetDefault(proxy.New(
		proxy.WithSelector(once.New()),
		proxy.WithInitialNodes(nodes),
	))

	// load plugin
	plugins := loadPlugin(log.GetLogger(), bc)

	// trasnport server
	servers := make([]transport.Server, 0)

	srv := server.NewServer(flip, bc, plugins)
	servers = append(servers, srv)

	for _, p := range plugins {
		servers = append(servers, p)
	}

	return kratos.New(
		kratos.ID(id),
		kratos.Name("tavern"),
		kratos.Version(runtime.BuildInfo.VcsRevision),
		kratos.StopTimeout(stopTimeout),
		kratos.Logger(logger),
		kratos.Server(servers...),
		kratos.BeforeStop(func(_ context.Context) error {
			return storage.Close()
		}),
	), nil
}

func newLogger(cl *conf.Logger) log.Logger {
	w := log.NewStdLogger(stdlog.Writer())

	if cl.Path != "" {
		_ = os.MkdirAll(filepath.Dir(cl.Path), 0o755)
		f := &lumberjack.Logger{
			Filename:   cl.Path,
			MaxSize:    cl.MaxSize,
			MaxBackups: cl.MaxBackups,
			MaxAge:     cl.MaxAge,
			Compress:   cl.Compress,
		}
		w = log.NewStdLogger(f)
	}
	// append option
	opts := make([]interface{}, 0, 8)
	opts = append(opts, "ts", log.Timestamp(time.RFC3339))
	if !cl.NoPid {
		opts = append(opts, "pid", os.Getpid())
	}
	if cl.Caller {
		opts = append(opts, "caller", log.Caller(5))
	}
	if cl.TraceID {
		opts = append(opts, "request.id", metrics.RequestID())
	}

	logger := log.NewFilter(
		log.With(w, opts...),
		log.FilterLevel(log.ParseLevel(cl.Level)),
	)
	log.SetLogger(logger)
	return logger
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
