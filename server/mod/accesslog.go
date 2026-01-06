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
		return wrap(next)
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

func tickRotate(f *lumberjack.Logger) {
	go func() {
		// 初始计算到下一个整点的延迟
		now := time.Now()
		next := now.Truncate(time.Minute).Add(time.Minute)
		timer := time.NewTimer(time.Until(next))
		defer timer.Stop()

		for {
			select {
			case <-timer.C:
				_ = f.Rotate()
				// 任务完成后，重新对齐到下一个整点，实现抗漂移
				now = time.Now()
				next = now.Truncate(time.Minute).Add(time.Minute)
				timer.Reset(time.Until(next))
			}
		}
	}()
}

func newAccessLog(path string) *zap.Logger {
	// initialize log file path
	_ = os.MkdirAll(filepath.Dir(path), 0o755)

	f := &lumberjack.Logger{
		Filename:   path,
		MaxSize:    100,
		MaxBackups: 100,
		MaxAge:     1,
		LocalTime:  true,
		Compress:   false,
	}

	// 按分钟 rotate
	tickRotate(f)

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
