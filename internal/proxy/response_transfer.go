package proxy

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"sync/atomic"
)

type responseTransferFailure struct {
	stage string
	err   error
}

type responseTransferObserver struct {
	failure atomic.Pointer[responseTransferFailure]
}

func (o *responseTransferObserver) record(stage string, err error) {
	if o == nil || !isResponseTransferFailure(err) {
		return
	}
	failure := &responseTransferFailure{stage: stage, err: err}
	o.failure.CompareAndSwap(nil, failure)
}

func (o *responseTransferObserver) firstFailure() *responseTransferFailure {
	if o == nil {
		return nil
	}
	return o.failure.Load()
}

func recordClientRequestBodyFailure(
	lifecycle *requestLifecycle,
	feedback *requestHealthFeedback,
	failure *responseTransferFailure,
) {
	lifecycle.setProxyError(ErrUpstreamRequestFailed)
	lifecycle.setUpstreamError(failure.stage, failure.err)
	lifecycle.setHTTPStatus(ErrUpstreamRequestFailed.HTTPCode)
	lifecycle.setNetOK(false)
	feedback.record(passiveHealthNeutral)
}

func isResponseTransferFailure(err error) bool {
	return err != nil &&
		!errors.Is(err, io.EOF) &&
		!errors.Is(err, context.Canceled) &&
		!errors.Is(err, net.ErrClosed)
}

type observedResponseWriter struct {
	http.ResponseWriter
	observer *responseTransferObserver
	onHijack func()
}

func (w *observedResponseWriter) Write(payload []byte) (int, error) {
	n, err := w.ResponseWriter.Write(payload)
	w.observer.record("reverse_response_write", err)
	return n, err
}

func (w *observedResponseWriter) Flush() {
	err := http.NewResponseController(w.ResponseWriter).Flush()
	if errors.Is(err, http.ErrNotSupported) {
		return
	}
	w.observer.record("reverse_response_flush", err)
}

func (w *observedResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	conn, buffered, err := http.NewResponseController(w.ResponseWriter).Hijack()
	if err != nil {
		w.observer.record("reverse_hijack", err)
		return nil, nil, err
	}
	if w.onHijack != nil {
		w.onHijack()
	}
	return wrapObservedClientConn(conn, w.observer), buffered, nil
}

func (w *observedResponseWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

type observedClientConn struct {
	net.Conn
	observer *responseTransferObserver
}

func wrapObservedClientConn(conn net.Conn, observer *responseTransferObserver) net.Conn {
	observed := &observedClientConn{Conn: conn, observer: observer}
	closeWriter, ok := conn.(interface{ CloseWrite() error })
	if !ok {
		return observed
	}
	return &observedCloseWriterConn{observedClientConn: observed, closeWriter: closeWriter}
}

func (c *observedClientConn) Read(payload []byte) (int, error) {
	n, err := c.Conn.Read(payload)
	c.observer.record("reverse_client_read", err)
	return n, err
}

func (c *observedClientConn) Write(payload []byte) (int, error) {
	n, err := c.Conn.Write(payload)
	c.observer.record("reverse_client_write", err)
	return n, err
}

type observedCloseWriterConn struct {
	*observedClientConn
	closeWriter interface{ CloseWrite() error }
}

func (c *observedCloseWriterConn) CloseWrite() error {
	err := c.closeWriter.CloseWrite()
	c.observer.record("reverse_client_close_write", err)
	return err
}
