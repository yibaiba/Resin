package outbound

import (
	"context"
	"fmt"
	"io"
	stdlog "log"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	singboxLog "github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
)

const defaultSingboxLogLevel = "warn"

var (
	urlPattern            = regexp.MustCompile(`(?i)\b[a-z][a-z0-9+.-]*://[^\s<>"']+`)
	sensitiveFieldPattern = regexp.MustCompile(
		`(?i)((?:"|')?(?:password|token|secret|private_key)(?:"|')?\s*(?:=|:)\s*)(?:"(?:\\.|[^"])*"|'[^']*'|[^\s,;&}\]]+)`,
	)
)

type logFactoryConstructor func(singboxLog.Options) (singboxLog.Factory, error)

func newResinSingboxLogFactory(
	ctx context.Context,
	level string,
	newFactory logFactoryConstructor,
) (singboxLog.Factory, error) {
	logOptions, err := singboxLogOptions(level)
	if err != nil {
		return nil, err
	}
	return newFactory(singboxLog.Options{
		Context:       ctx,
		Options:       logOptions,
		DefaultWriter: newSingboxLogWriter(stdlog.Default()),
		BaseTime:      time.Now(),
	})
}

func singboxLogOptions(level string) (option.LogOptions, error) {
	if level == "" {
		level = defaultSingboxLogLevel
	}
	if level == "disabled" {
		return option.LogOptions{Disabled: true, DisableColor: true}, nil
	}
	switch level {
	case "error", "warn", "info", "debug", "trace":
		return option.LogOptions{Level: level, DisableColor: true}, nil
	default:
		return option.LogOptions{}, fmt.Errorf("invalid sing-box log level %q", level)
	}
}

func outboundLogTag(outboundType, tag string) string {
	if tag == "" {
		return "outbound type=" + outboundType
	}
	return "outbound type=" + outboundType + " tag=" + tag
}

type singboxLogWriter struct {
	mu      sync.Mutex
	logger  *stdlog.Logger
	pending string
}

func newSingboxLogWriter(logger *stdlog.Logger) io.Writer {
	return &singboxLogWriter{logger: logger}
}

func (w *singboxLogWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	w.pending += string(p)
	for {
		lineEnd := strings.IndexByte(w.pending, '\n')
		if lineEnd < 0 {
			break
		}
		line := w.pending[:lineEnd]
		w.pending = w.pending[lineEnd+1:]
		if line != "" {
			w.logger.Print("[sing-box] ", sanitizeSingboxLog(line))
		}
	}
	return len(p), nil
}

func sanitizeSingboxLog(message string) string {
	message = urlPattern.ReplaceAllStringFunc(message, stripURLUserinfo)
	return sensitiveFieldPattern.ReplaceAllString(message, "${1}[REDACTED]")
}

func stripURLUserinfo(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.User == nil {
		return rawURL
	}
	parsed.User = nil
	return parsed.String()
}
