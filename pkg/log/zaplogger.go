package log

import (
	"io"
	"net/url"
	"os"
	"runtime"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

var levels = map[Level]zapcore.Level{
	DebugLevel: zapcore.DebugLevel,
	InfoLevel:  zapcore.InfoLevel,
	WarnLevel:  zapcore.WarnLevel,
	ErrorLevel: zapcore.ErrorLevel,
	PanicLevel: zapcore.PanicLevel,
	FatalLevel: zapcore.FatalLevel,
}

// Below patch is needed due to https://github.com/uber-go/zap/issues/621
func newWinFileSink(u *url.URL) (zap.Sink, error) {
	// Remove leading slash left by url.Parse()
	return os.OpenFile(u.Path[1:], os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0644) // #nosec
}

func init() {
	if runtime.GOOS == "windows" {
		err := zap.RegisterSink("winfile", newWinFileSink)
		if err != nil {
			panic(err)
		}
	}
}

// refers back level field in logger.go, which enables changing level in run time
func enabler(lvl zapcore.Level) bool {
	return lvl >= levels[level]
}

func newZapLogger(w io.Writer) *zap.Logger {
	encoder := zapcore.NewConsoleEncoder(zap.NewDevelopmentEncoderConfig())
	sync := zapcore.AddSync(w)
	core := zapcore.NewCore(encoder, sync, zap.LevelEnablerFunc(enabler))
	return zap.New(core, zap.AddCallerSkip(1), zap.AddStacktrace(zap.FatalLevel), zap.AddCaller())
}
