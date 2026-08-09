package proxy

import (
	"errors"
	"io"
	"net"
	"testing"
	"time"

	"github.com/Resinat/Resin/internal/node"
	"github.com/Resinat/Resin/internal/routing"
)

type scriptedTunnelConn struct {
	read  func([]byte) (int, error)
	write func([]byte) (int, error)
}

func (c *scriptedTunnelConn) Read(p []byte) (int, error) {
	return c.read(p)
}

func (c *scriptedTunnelConn) Write(p []byte) (int, error) {
	if c.write == nil {
		return len(p), nil
	}
	return c.write(p)
}

func (*scriptedTunnelConn) Close() error                     { return nil }
func (*scriptedTunnelConn) LocalAddr() net.Addr              { return testTunnelAddr("local") }
func (*scriptedTunnelConn) RemoteAddr() net.Addr             { return testTunnelAddr("remote") }
func (*scriptedTunnelConn) SetDeadline(time.Time) error      { return nil }
func (*scriptedTunnelConn) SetReadDeadline(time.Time) error  { return nil }
func (*scriptedTunnelConn) SetWriteDeadline(time.Time) error { return nil }

type testTunnelAddr string

func (a testTunnelAddr) Network() string { return "test" }
func (a testTunnelAddr) String() string  { return string(a) }

func TestPumpPreparedTunnelReader_AttributesTerminalHealthOnce(t *testing.T) {
	upstreamErr := errors.New("upstream transport failed")
	clientErr := errors.New("client connection reset")
	tests := []struct {
		name          string
		clientRead    func([]byte) (int, error)
		upstreamRead  func([]byte) (int, error)
		upstreamWrite func([]byte) (int, error)
		wantCalls     int32
		wantSuccess   int32
	}{
		{name: "upstream read before first byte", clientRead: scriptedRead(nil, io.EOF), upstreamRead: scriptedRead(nil, upstreamErr), wantCalls: 1},
		{name: "upstream write before first byte", clientRead: scriptedRead([]byte("request"), io.EOF), upstreamRead: scriptedRead(nil, io.EOF), upstreamWrite: scriptedWriteError(upstreamErr), wantCalls: 1},
		{name: "client read before first byte", clientRead: scriptedRead(nil, clientErr), upstreamRead: scriptedRead(nil, io.EOF)},
		{name: "first byte before upstream error", clientRead: scriptedRead(nil, io.EOF), upstreamRead: scriptedRead([]byte("response"), upstreamErr), wantCalls: 1, wantSuccess: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := &mockHealthRecorder{}
			health := newRequestHealthFeedback(recorder, routing.RouteResult{
				NodeHash: node.HashFromRawOptions([]byte(`{"type":"relay-health"}`)),
			}, "request-relay")
			client := &scriptedTunnelConn{read: test.clientRead}
			upstream := &scriptedTunnelConn{read: test.upstreamRead, write: test.upstreamWrite}

			pumpPreparedTunnelReader(client, client, &preparedTunnel{upstreamConn: upstream, health: health}, tunnelPumpOptions{})
			if got := recorder.resultCalls.Load(); got != test.wantCalls {
				t.Fatalf("health calls = %d, want %d", got, test.wantCalls)
			}
			if got := recorder.lastSuccess.Load(); got != test.wantSuccess {
				t.Fatalf("health success = %d, want %d", got, test.wantSuccess)
			}
		})
	}
}

func scriptedRead(data []byte, terminalErr error) func([]byte) (int, error) {
	delivered := false
	return func(p []byte) (int, error) {
		if delivered {
			return 0, io.EOF
		}
		delivered = true
		return copy(p, data), terminalErr
	}
}

func scriptedWriteError(err error) func([]byte) (int, error) {
	return func([]byte) (int, error) { return 0, err }
}
