package outbound

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	stdlog "log"
	"strings"
	"sync/atomic"
	"testing"

	singboxLog "github.com/sagernet/sing-box/log"
)

func TestSingboxLogFactoryLevels(t *testing.T) {
	tests := []struct {
		level   string
		present []string
		absent  []string
	}{
		{level: "disabled", absent: []string{"trace-message", "error-message"}},
		{level: "error", present: []string{"error-message"}, absent: []string{"warn-message"}},
		{level: "warn", present: []string{"warn-message", "error-message"}, absent: []string{"info-message"}},
		{level: "info", present: []string{"info-message"}, absent: []string{"debug-message"}},
		{level: "debug", present: []string{"debug-message"}, absent: []string{"trace-message"}},
		{level: "trace", present: []string{"trace-message"}},
	}

	for _, tc := range tests {
		t.Run(tc.level, func(t *testing.T) {
			output := captureStandardLog(t)
			factory := newStartedTestLogFactory(t, tc.level)
			defer factory.Close()

			logger := factory.NewLogger(outboundLogTag("socks", "node-1"))
			logger.Trace("trace-message")
			logger.Debug("debug-message")
			logger.Info("info-message")
			logger.Warn("warn-message")
			logger.Error("error-message")

			for _, want := range tc.present {
				assertLogContains(t, output.String(), want)
			}
			for _, unwanted := range tc.absent {
				assertLogExcludes(t, output.String(), unwanted)
			}
		})
	}
}

func TestSingboxLogIncludesContextAndRedactsSecrets(t *testing.T) {
	output := captureStandardLog(t)
	factory := newStartedTestLogFactory(t, "warn")
	defer factory.Close()

	factory.NewLogger(outboundLogTag("vmess", "edge-1")).Warn(
		`password=hunter2 token:abc "secret":"def" private_key='ghi' url=https://alice:pw@example.com/path`,
	)

	got := output.String()
	for _, want := range []string{"[sing-box]", "outbound type=vmess tag=edge-1", "https://example.com/path", "[REDACTED]"} {
		assertLogContains(t, got, want)
	}
	for _, secret := range []string{"hunter2", "abc", "def", "ghi", "alice", "pw@example.com"} {
		assertLogExcludes(t, got, secret)
	}
}

func TestSingboxBuilderDoesNotLogRawOptions(t *testing.T) {
	output := captureStandardLog(t)
	b, err := NewSingboxBuilderWithConfig(SingboxBuilderConfig{
		DNSUpstreams:    []string{"local"},
		SingboxLogLevel: "trace",
	})
	if err != nil {
		t.Fatalf("NewSingboxBuilderWithConfig() error: %v", err)
	}
	defer b.Close()

	raw := json.RawMessage(`{"type":"unknown-with-secret","tag":"secret-node","password":"raw-option-secret"}`)
	if _, err := b.Build(raw); err == nil {
		t.Fatal("expected invalid SOCKS outbound to fail")
	}
	assertLogExcludes(t, output.String(), "raw-option-secret")
}

func TestSingboxBuilderPropagatesLogFactoryErrors(t *testing.T) {
	createErr := errors.New("create log factory")
	_, err := newSingboxBuilderWithLogFactory(SingboxBuilderConfig{}, func(singboxLog.Options) (singboxLog.Factory, error) {
		return nil, createErr
	})
	if !errors.Is(err, createErr) {
		t.Fatalf("constructor error: got %v, want %v", err, createErr)
	}

	startErr := errors.New("start log factory")
	closeErr := errors.New("close after start failure")
	factory := &lifecycleLogFactory{Factory: singboxLog.NewNOPFactory(), startErr: startErr, closeErr: closeErr}
	_, err = newSingboxBuilderWithLogFactory(SingboxBuilderConfig{}, func(singboxLog.Options) (singboxLog.Factory, error) {
		return factory, nil
	})
	if !errors.Is(err, startErr) || !errors.Is(err, closeErr) {
		t.Fatalf("start error: got %v, want joined start and close errors", err)
	}
	if factory.closeCalls.Load() != 1 {
		t.Fatalf("close calls after start failure: got %d, want 1", factory.closeCalls.Load())
	}
}

func TestSingboxBuilderPropagatesLogFactoryCloseError(t *testing.T) {
	closeErr := errors.New("close log factory")
	factory := &lifecycleLogFactory{Factory: singboxLog.NewNOPFactory(), closeErr: closeErr}
	b, err := newSingboxBuilderWithLogFactory(SingboxBuilderConfig{DNSUpstreams: []string{"local"}}, func(singboxLog.Options) (singboxLog.Factory, error) {
		return factory, nil
	})
	if err != nil {
		t.Fatalf("newSingboxBuilderWithLogFactory() error: %v", err)
	}

	if err := b.Close(); !errors.Is(err, closeErr) {
		t.Fatalf("Close() error: got %v, want %v", err, closeErr)
	}
	if err := b.Close(); !errors.Is(err, closeErr) {
		t.Fatalf("second Close() error: got %v, want %v", err, closeErr)
	}
	if factory.closeCalls.Load() != 1 {
		t.Fatalf("close calls: got %d, want 1", factory.closeCalls.Load())
	}
}

type lifecycleLogFactory struct {
	singboxLog.Factory
	startErr   error
	closeErr   error
	closeCalls atomic.Int32
}

func (f *lifecycleLogFactory) Start() error {
	return f.startErr
}

func (f *lifecycleLogFactory) Close() error {
	f.closeCalls.Add(1)
	return f.closeErr
}

func newStartedTestLogFactory(t *testing.T, level string) singboxLog.Factory {
	t.Helper()
	factory, err := newResinSingboxLogFactory(context.Background(), level, singboxLog.New)
	if err != nil {
		t.Fatalf("newResinSingboxLogFactory() error: %v", err)
	}
	if err := factory.Start(); err != nil {
		t.Fatalf("log factory Start() error: %v", err)
	}
	return factory
}

func captureStandardLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var output bytes.Buffer
	logger := stdlog.Default()
	previousWriter := logger.Writer()
	previousFlags := logger.Flags()
	previousPrefix := logger.Prefix()
	logger.SetOutput(&output)
	logger.SetFlags(0)
	logger.SetPrefix("")
	t.Cleanup(func() {
		logger.SetOutput(previousWriter)
		logger.SetFlags(previousFlags)
		logger.SetPrefix(previousPrefix)
	})
	return &output
}

func assertLogContains(t *testing.T, output, want string) {
	t.Helper()
	if !strings.Contains(output, want) {
		t.Errorf("log output %q does not contain %q", output, want)
	}
}

func assertLogExcludes(t *testing.T, output, unwanted string) {
	t.Helper()
	if strings.Contains(output, unwanted) {
		t.Errorf("log output %q contains sensitive or filtered value %q", output, unwanted)
	}
}
