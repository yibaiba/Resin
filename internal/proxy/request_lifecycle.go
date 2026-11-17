package proxy

import (
	"errors"
	"net"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/Resinat/Resin/internal/routing"
	"github.com/google/uuid"
)

// requestLifecycle captures mutable per-request telemetry and emits both
// metrics and request-log events on completion.
type requestLifecycle struct {
	startedAt           time.Time
	events              EventEmitter
	finished            RequestFinishedEvent
	log                 RequestLogEntry
	firstByteDurationNs atomic.Int64

	reqBodyCapture  *payloadCaptureReadCloser
	respBodyCapture *payloadCaptureReadCloser
	healthFeedback  *requestHealthFeedback
	tunnelRelay     *tunnelRelayResult
	chainNodeHashes []string
}

func newRequestLifecycleFromMetadata(
	events EventEmitter,
	clientRemoteAddr string,
	method string,
	proxyType ProxyType,
	isConnect bool,
) *requestLifecycle {
	clientIP := ""
	if host, _, err := net.SplitHostPort(clientRemoteAddr); err == nil {
		clientIP = host
	} else {
		clientIP = clientRemoteAddr // fallback: bare IP or unparseable
	}
	now := time.Now()
	return &requestLifecycle{
		startedAt: now,
		events:    events,
		finished: RequestFinishedEvent{
			ProxyType: proxyType,
			IsConnect: isConnect,
		},
		log: RequestLogEntry{
			ID:          uuid.NewString(),
			StartedAtNs: now.UnixNano(),
			ProxyType:   proxyType,
			ClientIP:    clientIP,
			HTTPMethod:  method,
		},
	}
}

func newRequestLifecycle(
	events EventEmitter,
	r *http.Request,
	proxyType ProxyType,
	isConnect bool,
) *requestLifecycle {
	method := ""
	clientRemoteAddr := ""
	if r != nil {
		method = r.Method
		clientRemoteAddr = r.RemoteAddr
	}
	return newRequestLifecycleFromMetadata(events, clientRemoteAddr, method, proxyType, isConnect)
}

func (l *requestLifecycle) finish() {
	if l.reqBodyCapture != nil {
		l.log.ReqBody = l.reqBodyCapture.Payload()
		l.log.ReqBodyLen = l.reqBodyCapture.TotalLen()
		l.log.ReqBodyTruncated = l.reqBodyCapture.Truncated()
	}
	if l.respBodyCapture != nil {
		l.log.RespBody = l.respBodyCapture.Payload()
		l.log.RespBodyLen = l.respBodyCapture.TotalLen()
		l.log.RespBodyTruncated = l.respBodyCapture.Truncated()
	}

	durationNs := time.Since(l.startedAt).Nanoseconds()
	l.finished.DurationNs = durationNs
	l.log.DurationNs = durationNs
	l.log.FirstByteDurationNs = l.firstByteDurationNs.Load()
	logProxyFailure(l)
	l.events.EmitRequestFinished(l.finished)
	l.events.EmitRequestLog(l.log)
}

func (l *requestLifecycle) markFirstByteReceived() {
	if l == nil || l.startedAt.IsZero() {
		return
	}
	durationNs := time.Since(l.startedAt).Nanoseconds()
	if durationNs <= 0 {
		durationNs = 1
	}
	// 首字耗时只记录第一次观测到上游响应字节的时间，避免重试或多回调覆盖。
	l.firstByteDurationNs.CompareAndSwap(0, durationNs)
}

func (l *requestLifecycle) setHTTPStatus(code int) {
	l.log.HTTPStatus = code
}

func (l *requestLifecycle) setProxyError(pe *ProxyError) {
	if pe == nil {
		return
	}
	l.log.ResinError = pe.ResinError
}

func (l *requestLifecycle) setRouteFailure(pe *ProxyError) {
	if l == nil || pe == nil {
		return
	}
	l.setProxyError(pe)
	l.setUpstreamError("route", errors.New(pe.ResinError))
	if pe.HTTPCode != 0 {
		l.setHTTPStatus(pe.HTTPCode)
	}
	l.setNetOK(false)
}

func (l *requestLifecycle) setUpstreamError(stage string, err error) {
	if l.log.UpstreamStage == "" && stage != "" {
		l.log.UpstreamStage = stage
	}
	if err == nil || l.log.UpstreamErrMsg != "" {
		return
	}
	detail := summarizeUpstreamError(err)
	l.log.UpstreamErrKind = detail.Kind
	l.log.UpstreamErrno = detail.Errno
	l.log.UpstreamErrMsg = detail.Message
}

func (l *requestLifecycle) addIngressBytes(n int64) {
	if n > 0 {
		l.log.IngressBytes += n
	}
}

func (l *requestLifecycle) addEgressBytes(n int64) {
	if n > 0 {
		l.log.EgressBytes += n
	}
}

func (l *requestLifecycle) setNetOK(ok bool) {
	l.finished.NetOK = ok
	l.log.NetOK = ok
}

func (l *requestLifecycle) setAccount(account string) {
	l.log.Account = account
}

func (l *requestLifecycle) setRequestedPlatformName(platformName string) {
	l.log.RequestedPlatformName = platformName
}

func (l *requestLifecycle) setTarget(host, rawURL string) {
	l.log.TargetHost = host
	l.log.TargetURL = rawURL
}

func (l *requestLifecycle) setReqHeadersCaptured(reqHeaders []byte, totalLen int, truncated bool) {
	l.log.ReqHeaders = reqHeaders
	l.log.ReqHeadersLen = totalLen
	l.log.ReqHeadersTruncated = truncated
}

func (l *requestLifecycle) setReqBodyCapture(c *payloadCaptureReadCloser) {
	l.reqBodyCapture = c
}

func (l *requestLifecycle) setRespHeadersCaptured(respHeaders []byte, totalLen int, truncated bool) {
	l.log.RespHeaders = respHeaders
	l.log.RespHeadersLen = totalLen
	l.log.RespHeadersTruncated = truncated
}

func (l *requestLifecycle) setRespBodyCapture(c *payloadCaptureReadCloser) {
	l.respBodyCapture = c
}

func (l *requestLifecycle) setHealthFeedback(feedback *requestHealthFeedback) {
	l.healthFeedback = feedback
}

func (l *requestLifecycle) setTunnelRelay(result tunnelRelayResult) {
	l.tunnelRelay = &result
}

func (l *requestLifecycle) setRouteResult(result routing.RouteResult) {
	l.finished.PlatformID = result.PlatformID
	l.log.PlatformID = result.PlatformID
	l.log.PlatformName = result.PlatformName
	l.log.NodeHash = result.NodeHash.Hex()
	l.log.NodeTag = result.NodeTag
	l.log.EgressIP = result.EgressIP.String()
	if len(result.ChainHops) == 0 {
		l.chainNodeHashes = nil
		return
	}
	l.chainNodeHashes = make([]string, 0, len(result.ChainHops))
	for _, hop := range result.ChainHops {
		l.chainNodeHashes = append(l.chainNodeHashes, hop.NodeHash.Hex())
	}
}

func (l *requestLifecycle) clearTargetAndRoute() {
	l.finished.PlatformID = ""
	l.log.TargetHost = ""
	l.log.TargetURL = ""
	l.log.PlatformID = ""
	l.log.PlatformName = ""
	l.log.NodeHash = ""
	l.log.NodeTag = ""
	l.log.EgressIP = ""
	l.chainNodeHashes = nil
}
