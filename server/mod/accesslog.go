package mod

import (
	"net/http"
	"os"
	"path/filepath"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"gopkg.in/natefinch/lumberjack.v2"

	"github.com/omalloc/tavern/conf"
	"github.com/omalloc/tavern/contrib/log"
	"github.com/omalloc/tavern/metrics"
	xhttp "github.com/omalloc/tavern/pkg/x/http"
)

func HandleAccessLog(opt *conf.ServerAccessLog, next http.HandlerFunc) http.HandlerFunc {
	if !opt.Enabled {
		log.Infof("access-log is turned off")
		return next
	}

	if opt.Path == "" {
		log.Warnf("access-log `path` is empty, will be written to stdout")
		return wrap(next)
	}

	logWriter := newAccessLog(opt.Path)

	// 提前根据配置初始化是否加密
	// 避免每次请求都判断 opt.LogEncrypt
	defeaterWriter := func(buf []byte) {
		logWriter.Info(string(buf))
	}
	if opt.Encrypt.Enabled {
		defeaterWriter = func(buf []byte) {
			// TODO: 对日志进行加密处理
			// logWriter.Info()
		}
	}

	return func(w http.ResponseWriter, req *http.Request) {
		// 补全 request 结构
		fillRequest(req)

		req, _ = metrics.WithRequestMetric(req)
		recorder := xhttp.NewResponseRecorder(w)

		defer func() {
			// write access log
			defeaterWriter(WithNormalFields(req, recorder))
		}()

		next(recorder, req)
	}
}

func newAccessLog(path string) *zap.Logger {
	// initialize log file path
	_ = os.MkdirAll(filepath.Dir(path), 0o755)

	f := &lumberjack.Logger{
		Filename:   path,
		MaxSize:    100,
		MaxBackups: 3,
		MaxAge:     1,
		LocalTime:  true,
		Compress:   false,
	}

	cfg := zap.NewProductionConfig().EncoderConfig
	cfg.ConsoleSeparator = " "
	cfg.EncodeTime = func(t time.Time, enc zapcore.PrimitiveArrayEncoder) {}
	cfg.EncodeLevel = func(_ zapcore.Level, _ zapcore.PrimitiveArrayEncoder) {}

	logWriter := zap.New(zapcore.NewCore(
		zapcore.NewConsoleEncoder(cfg),
		zapcore.AddSync(f),
		zapcore.InfoLevel,
	))

	return logWriter
}
