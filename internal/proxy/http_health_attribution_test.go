package proxy

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"syscall"
	"testing"
	"time"
)

type routedHTTPExercise struct {
	reverse        bool
	upstreamURL    string
	requestMethod  string
	requestBody    io.Reader
	cancelOnRead   bool
	requestHeaders http.Header
	responseWriter http.ResponseWriter
}

type routedHTTPResult struct {
	response *httptest.ResponseRecorder
	health   *mockHealthRecorder
	log      RequestLogEntry
}

func exerciseRoutedHTTP(t *testing.T, exercise routedHTTPExercise) routedHTTPResult {
	t.Helper()
	env := newProxyE2EEnv(t)
	emitter := newMockEventEmitter()
	health := &mockHealthRecorder{}
	requestURL := exercise.upstreamURL

	var handler http.Handler
	if exercise.reverse {
		parsed, err := url.Parse(exercise.upstreamURL)
		if err != nil {
			t.Fatalf("parse upstream URL: %v", err)
		}
		requestURL = fmt.Sprintf("/tok/plat:acct/%s/%s%s", parsed.Scheme, parsed.Host, parsed.RequestURI())
		handler = NewReverseProxy(ReverseProxyConfig{
			ProxyToken:     "tok",
			Router:         env.router,
			Pool:           env.pool,
			PlatformLookup: env.pool,
			Health:         health,
			Events:         emitter,
		})
	} else {
		handler = NewForwardProxy(ForwardProxyConfig{
			ProxyToken: "tok",
			Router:     env.router,
			Pool:       env.pool,
			Health:     health,
			Events:     emitter,
		})
	}

	method := exercise.requestMethod
	if method == "" {
		method = http.MethodGet
	}
	req := httptest.NewRequest(method, requestURL, exercise.requestBody)
	if exercise.cancelOnRead {
		ctx, cancel := context.WithCancel(req.Context())
		req = req.WithContext(ctx)
		req.Body = cancelingResetRequestBody{cancel: cancel}
	}
	for name, values := range exercise.requestHeaders {
		for _, value := range values {
			req.Header.Add(name, value)
		}
	}
	if !exercise.reverse {
		req.Header.Set("Proxy-Authorization", basicAuth("plat", "tok"))
	}
	response := httptest.NewRecorder()
	responseWriter := exercise.responseWriter
	if responseWriter == nil {
		responseWriter = response
	}
	handler.ServeHTTP(responseWriter, req)

	select {
	case logEntry := <-emitter.logCh:
		return routedHTTPResult{response: response, health: health, log: logEntry}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timed out waiting for request log")
		return routedHTTPResult{}
	}
}

func TestHTTPProxies_ResponseHeadersRecordOneHealthSuccess(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("upstream unavailable"))
	}))
	defer upstream.Close()

	for _, reverse := range []bool{false, true} {
		name := "forward"
		if reverse {
			name = "reverse"
		}
		t.Run(name, func(t *testing.T) {
			result := exerciseRoutedHTTP(t, routedHTTPExercise{
				reverse:     reverse,
				upstreamURL: upstream.URL + "/status",
			})
			if result.response.Code != http.StatusServiceUnavailable {
				t.Fatalf("status = %d, want %d", result.response.Code, http.StatusServiceUnavailable)
			}
			assertOneHealthSuccess(t, result.health)
			if !result.log.NetOK {
				t.Fatal("HTTP error status must remain a network success")
			}
		})
	}
}

func TestHTTPProxies_PostHeaderReadFailureDoesNotPenalizeNode(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(writeTruncatedHTTPResponse))
	defer upstream.Close()

	for _, reverse := range []bool{false, true} {
		name := "forward"
		wantStage := "forward_upstream_to_client_copy"
		if reverse {
			name = "reverse"
			wantStage = "reverse_upstream_body_read"
		}
		t.Run(name, func(t *testing.T) {
			result := exerciseRoutedHTTP(t, routedHTTPExercise{
				reverse:     reverse,
				upstreamURL: upstream.URL + "/truncated",
			})
			assertOneHealthSuccess(t, result.health)
			if result.log.NetOK {
				t.Fatal("truncated response must remain a request transport failure")
			}
			if result.log.UpstreamStage != wantStage {
				t.Fatalf("upstream stage = %q, want %q", result.log.UpstreamStage, wantStage)
			}
		})
	}
}

func TestHTTPProxies_PostHeaderClientWriteFailureDoesNotPenalizeNode(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("response body"))
	}))
	defer upstream.Close()

	for _, reverse := range []bool{false, true} {
		name := "forward"
		wantStage := "forward_upstream_to_client_copy"
		if reverse {
			name = "reverse"
			wantStage = "reverse_response_write"
		}
		t.Run(name, func(t *testing.T) {
			writer := &failingBodyResponseWriter{ResponseRecorder: httptest.NewRecorder()}
			result := exerciseRoutedHTTP(t, routedHTTPExercise{
				reverse:        reverse,
				upstreamURL:    upstream.URL + "/write-failure",
				responseWriter: writer,
			})
			assertOneHealthSuccess(t, result.health)
			if result.log.NetOK {
				t.Fatal("client write error must remain a request transport failure")
			}
			if result.log.UpstreamStage != wantStage {
				t.Fatalf("upstream stage = %q, want %q", result.log.UpstreamStage, wantStage)
			}
		})
	}
}

func TestReverseProxy_LocalHijackFailureIsHealthNeutral(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(writeUpgradeResponse))
	defer upstream.Close()

	result := exerciseRoutedHTTP(t, routedHTTPExercise{
		reverse:     true,
		upstreamURL: upstream.URL + "/upgrade",
		requestHeaders: http.Header{
			"Connection": {"Upgrade"},
			"Upgrade":    {"websocket"},
		},
	})
	if calls := result.health.resultCalls.Load(); calls != 0 {
		t.Fatalf("local Hijack failure health calls = %d, want 0", calls)
	}
	if result.log.NetOK {
		t.Fatal("local Hijack failure must remain a request failure")
	}
	if result.log.UpstreamStage != "reverse_hijack" {
		t.Fatalf("upstream stage = %q, error=%q, want reverse_hijack", result.log.UpstreamStage, result.log.UpstreamErrMsg)
	}
}

func TestReverseProxy_FirstResponseByteBeforeMalformedHeadersRecordsSuccess(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(writeMalformedHTTPResponse))
	defer upstream.Close()

	result := exerciseRoutedHTTP(t, routedHTTPExercise{
		reverse:     true,
		upstreamURL: upstream.URL + "/malformed",
	})
	assertOneHealthSuccess(t, result.health)
	if result.log.NetOK {
		t.Fatal("malformed upstream response must remain a request failure")
	}
	if result.log.UpstreamStage != "reverse_roundtrip" {
		t.Fatalf("upstream stage = %q, want reverse_roundtrip", result.log.UpstreamStage)
	}
}

func TestHTTPProxies_ClientRequestBodyReadFailureIsHealthNeutral(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer upstream.Close()

	for _, reverse := range []bool{false, true} {
		name := "forward"
		wantStage := "forward_client_body_read"
		if reverse {
			name = "reverse"
			wantStage = "reverse_client_body_read"
		}
		t.Run(name, func(t *testing.T) {
			result := exerciseRoutedHTTP(t, routedHTTPExercise{
				reverse:       reverse,
				upstreamURL:   upstream.URL + "/upload",
				requestMethod: http.MethodPost,
				requestBody:   resetRequestBody{},
			})
			if calls := result.health.resultCalls.Load(); calls != 0 {
				t.Fatalf("client body failure health calls = %d, want 0", calls)
			}
			if result.log.NetOK {
				t.Fatal("client body read error must remain a request failure")
			}
			if result.log.UpstreamStage != wantStage {
				t.Fatalf("upstream stage = %q, want %q", result.log.UpstreamStage, wantStage)
			}
		})
	}
}

func TestHTTPProxies_ClientBodyResetWithCanceledContextRemainsRequestFailure(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer upstream.Close()

	for _, reverse := range []bool{false, true} {
		name := "forward"
		wantStage := "forward_client_body_read"
		if reverse {
			name = "reverse"
			wantStage = "reverse_client_body_read"
		}
		t.Run(name, func(t *testing.T) {
			result := exerciseRoutedHTTP(t, routedHTTPExercise{
				reverse:       reverse,
				upstreamURL:   upstream.URL + "/upload-reset",
				requestMethod: http.MethodPost,
				requestBody:   strings.NewReader("payload"),
				cancelOnRead:  true,
			})
			assertClientBodyFailureIsNeutral(t, result, wantStage)
		})
	}
}

func TestReverseProxy_FlushFailureIsLoggedWithoutPenalizingNode(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: ready\n\n"))
	}))
	defer upstream.Close()

	writer := &flushErrorResponseWriter{ResponseRecorder: httptest.NewRecorder()}
	result := exerciseRoutedHTTP(t, routedHTTPExercise{
		reverse:        true,
		upstreamURL:    upstream.URL + "/events",
		responseWriter: writer,
	})
	assertOneHealthSuccess(t, result.health)
	if result.log.NetOK {
		t.Fatal("client flush error must remain a request failure")
	}
	if result.log.UpstreamStage != "reverse_response_flush" {
		t.Fatalf("upstream stage = %q, want reverse_response_flush", result.log.UpstreamStage)
	}
}

func writeTruncatedHTTPResponse(w http.ResponseWriter, _ *http.Request) {
	hijacker := w.(http.Hijacker)
	conn, buffered, err := hijacker.Hijack()
	if err != nil {
		return
	}
	defer conn.Close()
	_, _ = buffered.WriteString("HTTP/1.1 200 OK\r\nContent-Length: 64\r\n\r\npartial")
	_ = buffered.Flush()
}

func writeMalformedHTTPResponse(w http.ResponseWriter, _ *http.Request) {
	conn, buffered, err := w.(http.Hijacker).Hijack()
	if err != nil {
		return
	}
	defer conn.Close()
	_, _ = buffered.WriteString("HTTP/1.1 invalid status\r\n\r\n")
	_ = buffered.Flush()
}

func assertOneHealthSuccess(t *testing.T, health *mockHealthRecorder) {
	t.Helper()
	if calls := health.resultCalls.Load(); calls != 1 {
		t.Fatalf("health result calls = %d, want 1", calls)
	}
	if health.lastSuccess.Load() != 1 {
		t.Fatal("health result = failure, want success")
	}
}

func assertClientBodyFailureIsNeutral(t *testing.T, result routedHTTPResult, wantStage string) {
	t.Helper()
	if calls := result.health.resultCalls.Load(); calls != 0 {
		t.Fatalf("client body failure health calls = %d, want 0", calls)
	}
	if result.log.NetOK {
		t.Fatal("client body read error must remain a request failure")
	}
	if result.log.UpstreamStage != wantStage {
		t.Fatalf("upstream stage = %q, want %q", result.log.UpstreamStage, wantStage)
	}
}

type failingBodyResponseWriter struct {
	*httptest.ResponseRecorder
}

type resetRequestBody struct{}

func (resetRequestBody) Read([]byte) (int, error) {
	return 0, syscall.ECONNRESET
}

func (resetRequestBody) Close() error {
	return nil
}

type cancelingResetRequestBody struct {
	cancel context.CancelFunc
}

func (body cancelingResetRequestBody) Read([]byte) (int, error) {
	body.cancel()
	return 0, syscall.ECONNRESET
}

func (cancelingResetRequestBody) Close() error {
	return nil
}

type flushErrorResponseWriter struct {
	*httptest.ResponseRecorder
}

func (w *flushErrorResponseWriter) FlushError() error {
	return syscall.EPIPE
}

func (w *failingBodyResponseWriter) Write([]byte) (int, error) {
	return 0, syscall.EPIPE
}

func writeUpgradeResponse(w http.ResponseWriter, _ *http.Request) {
	conn, buffered, err := w.(http.Hijacker).Hijack()
	if err != nil {
		return
	}
	defer conn.Close()
	_, _ = buffered.WriteString("HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n\r\n")
	_ = buffered.Flush()
}
