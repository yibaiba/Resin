package proxy

import (
	"bytes"
	"io"
	"net/http"
	"sync/atomic"
)

// captureRequestHeaders serializes headers to canonical wire format for
// request-log payload capture.
func captureRequestHeaders(header http.Header) []byte {
	if header == nil {
		return nil
	}
	var buf bytes.Buffer
	_ = header.Clone().Write(&buf)
	return buf.Bytes()
}

// headerWireLen returns canonical wire-format header bytes length.
func headerWireLen(header http.Header) int64 {
	if header == nil || len(header) == 0 {
		return 0
	}
	var buf bytes.Buffer
	_ = header.Write(&buf)
	return int64(buf.Len())
}

func captureHeadersWithLimit(header http.Header, maxBytes int) ([]byte, int, bool) {
	payload := captureRequestHeaders(header)
	totalLen := len(payload)
	if totalLen == 0 {
		return nil, 0, false
	}
	if maxBytes >= 0 && totalLen > maxBytes {
		return payload[:maxBytes], totalLen, true
	}
	return payload, totalLen, false
}

type payloadCaptureReadCloser struct {
	rc       io.ReadCloser
	maxBytes int
	payload  bytes.Buffer
	totalLen int
}

func newPayloadCaptureReadCloser(rc io.ReadCloser, maxBytes int) *payloadCaptureReadCloser {
	return &payloadCaptureReadCloser{
		rc:       rc,
		maxBytes: maxBytes,
	}
}

func (c *payloadCaptureReadCloser) Read(p []byte) (int, error) {
	n, err := c.rc.Read(p)
	if n > 0 {
		c.totalLen += n
		if c.maxBytes < 0 {
			_, _ = c.payload.Write(p[:n])
		} else {
			remaining := c.maxBytes - c.payload.Len()
			if remaining > 0 {
				if n <= remaining {
					_, _ = c.payload.Write(p[:n])
				} else {
					_, _ = c.payload.Write(p[:remaining])
				}
			}
		}
	}
	return n, err
}

func (c *payloadCaptureReadCloser) Close() error {
	return c.rc.Close()
}

func (c *payloadCaptureReadCloser) Payload() []byte {
	return c.payload.Bytes()
}

func (c *payloadCaptureReadCloser) TotalLen() int {
	return c.totalLen
}

func (c *payloadCaptureReadCloser) Truncated() bool {
	return c.totalLen > c.payload.Len()
}

// countingReadCloser wraps a body stream and records total read bytes.
type countingReadCloser struct {
	rc          io.ReadCloser
	total       atomic.Int64
	onReadError func(error)
}

func newCountingReadCloser(rc io.ReadCloser) *countingReadCloser {
	return &countingReadCloser{rc: rc}
}

func newObservedCountingReadCloser(rc io.ReadCloser, onReadError func(error)) *countingReadCloser {
	return &countingReadCloser{rc: rc, onReadError: onReadError}
}

func (c *countingReadCloser) Read(p []byte) (int, error) {
	n, err := c.rc.Read(p)
	if n > 0 {
		c.total.Add(int64(n))
	}
	if err != nil && c.onReadError != nil {
		c.onReadError(err)
	}
	return n, err
}

func (c *countingReadCloser) Close() error {
	return c.rc.Close()
}

func (c *countingReadCloser) Total() int64 {
	return c.total.Load()
}

// countingReadWriteCloser wraps a bidirectional stream and records
// bytes read/written independently.
type countingReadWriteCloser struct {
	rwc          io.ReadWriteCloser
	totalRead    atomic.Int64
	totalWrite   atomic.Int64
	onReadError  func(error)
	onWriteError func(error)
}

func newCountingReadWriteCloser(rwc io.ReadWriteCloser) *countingReadWriteCloser {
	return &countingReadWriteCloser{rwc: rwc}
}

type streamErrorCallbacks struct {
	read  func(error)
	write func(error)
}

func newObservedCountingReadWriteCloser(
	rwc io.ReadWriteCloser,
	callbacks streamErrorCallbacks,
) *countingReadWriteCloser {
	return &countingReadWriteCloser{
		rwc:          rwc,
		onReadError:  callbacks.read,
		onWriteError: callbacks.write,
	}
}

func (c *countingReadWriteCloser) Read(p []byte) (int, error) {
	n, err := c.rwc.Read(p)
	if n > 0 {
		c.totalRead.Add(int64(n))
	}
	if err != nil && c.onReadError != nil {
		c.onReadError(err)
	}
	return n, err
}

func (c *countingReadWriteCloser) Write(p []byte) (int, error) {
	n, err := c.rwc.Write(p)
	if n > 0 {
		c.totalWrite.Add(int64(n))
	}
	if err != nil && c.onWriteError != nil {
		c.onWriteError(err)
	}
	return n, err
}

func (c *countingReadWriteCloser) Close() error {
	return c.rwc.Close()
}

func (c *countingReadWriteCloser) preserveCloseWriter() io.ReadWriteCloser {
	closeWriter, ok := c.rwc.(interface{ CloseWrite() error })
	if !ok {
		return c
	}
	return &countingCloseWriter{countingReadWriteCloser: c, closeWriter: closeWriter}
}

func (c *countingReadWriteCloser) TotalRead() int64 {
	return c.totalRead.Load()
}

func (c *countingReadWriteCloser) TotalWrite() int64 {
	return c.totalWrite.Load()
}

type countingCloseWriter struct {
	*countingReadWriteCloser
	closeWriter interface{ CloseWrite() error }
}

func (c *countingCloseWriter) CloseWrite() error {
	return c.closeWriter.CloseWrite()
}
