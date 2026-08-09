package proxy

import (
	"log"
	"strconv"

	"github.com/Resinat/Resin/internal/diagnosticlog"
)

func logProxyFailure(lifecycle *requestLifecycle) {
	line := proxyFailureLine(lifecycle)
	if line != "" {
		log.Print(line)
	}
}

func proxyFailureLine(lifecycle *requestLifecycle) string {
	prefix := proxyFailurePrefix(lifecycle)
	if prefix == "" || lifecycle.log.NetOK {
		return ""
	}
	entry := lifecycle.log
	fields := []diagnosticlog.Field{
		diagnosticlog.F("request_id", entry.ID),
		diagnosticlog.F("proxy_type", proxyTypeLabel(entry.ProxyType, lifecycle.finished.IsConnect)),
		diagnosticlog.F("platform", entry.PlatformName),
		diagnosticlog.F("target_host", entry.TargetHost),
		diagnosticlog.F("node_hash", entry.NodeHash),
		diagnosticlog.F("node_tag", entry.NodeTag),
		diagnosticlog.F("duration_ns", strconv.FormatInt(entry.DurationNs, 10)),
		diagnosticlog.F("first_byte_ns", strconv.FormatInt(entry.FirstByteDurationNs, 10)),
		diagnosticlog.F("ingress_bytes", strconv.FormatInt(entry.IngressBytes, 10)),
		diagnosticlog.F("egress_bytes", strconv.FormatInt(entry.EgressBytes, 10)),
		diagnosticlog.F("health_attribution", healthOutcomeLabel(lifecycle.healthFeedback.outcome())),
		diagnosticlog.F("stage", entry.UpstreamStage),
		diagnosticlog.F("error_kind", entry.UpstreamErrKind),
		diagnosticlog.F("errno", entry.UpstreamErrno),
		diagnosticlog.F("error", entry.UpstreamErrMsg),
	}
	fields = appendTunnelDiagnosticFields(fields, lifecycle.tunnelRelay)
	return diagnosticlog.Line(prefix, fields...)
}

func proxyFailurePrefix(lifecycle *requestLifecycle) string {
	if lifecycle == nil {
		return ""
	}
	if lifecycle.finished.IsConnect {
		return "[proxy-connect]"
	}
	if lifecycle.log.ProxyType == ProxyTypeForward || lifecycle.log.ProxyType == ProxyTypeReverse {
		return "[proxy-http]"
	}
	return ""
}

func proxyTypeLabel(proxyType ProxyType, connect bool) string {
	switch {
	case proxyType == ProxyTypeForward && connect:
		return "http_connect"
	case proxyType == ProxyTypeSocks5Forward && connect:
		return "socks5_connect"
	case proxyType == ProxyTypeForward:
		return "forward_http"
	case proxyType == ProxyTypeReverse:
		return "reverse_http"
	default:
		return "unknown"
	}
}

func healthOutcomeLabel(outcome passiveHealthOutcome) string {
	switch outcome {
	case passiveHealthSuccess:
		return "success"
	case passiveHealthFailure:
		return "failure"
	default:
		return "neutral"
	}
}

func appendTunnelDiagnosticFields(
	fields []diagnosticlog.Field,
	relay *tunnelRelayResult,
) []diagnosticlog.Field {
	if relay == nil {
		return fields
	}
	fields = append(fields, diagnosticlog.F("first_ended", string(relay.firstEnded)))
	fields = appendDirectionDiagnosticFields(fields, "upstream_to_client", relay.ingress)
	return appendDirectionDiagnosticFields(fields, "client_to_upstream", relay.egress)
}

func appendDirectionDiagnosticFields(
	fields []diagnosticlog.Field,
	prefix string,
	result tunnelDirectionResult,
) []diagnosticlog.Field {
	return append(fields,
		diagnosticlog.F(prefix+"_bytes", strconv.FormatInt(result.bytes, 10)),
		diagnosticlog.F(prefix+"_end_order", strconv.FormatUint(uint64(result.endOrder), 10)),
		diagnosticlog.F(prefix+"_operation", string(result.operation)),
		diagnosticlog.F(prefix+"_error_kind", result.errorKind),
		diagnosticlog.F(prefix+"_errno", result.errno),
		diagnosticlog.F(prefix+"_error", result.errorMessage),
		diagnosticlog.F(prefix+"_close_write_error_kind", result.closeWriteErrorKind),
		diagnosticlog.F(prefix+"_close_write_errno", result.closeWriteErrno),
		diagnosticlog.F(prefix+"_close_write_error", result.closeWriteErrorMessage),
	)
}

type lifecycleDiagnosticWriter struct {
	lifecycle *requestLifecycle
}

func (w lifecycleDiagnosticWriter) Write(payload []byte) (int, error) {
	entry := w.lifecycle.log
	diagnosticlog.Print("[proxy-http]",
		diagnosticlog.F("event", "transport_internal"),
		diagnosticlog.F("request_id", entry.ID),
		diagnosticlog.F("proxy_type", proxyTypeLabel(entry.ProxyType, false)),
		diagnosticlog.F("platform", entry.PlatformName),
		diagnosticlog.F("target_host", entry.TargetHost),
		diagnosticlog.F("node_hash", entry.NodeHash),
		diagnosticlog.F("node_tag", entry.NodeTag),
		diagnosticlog.F("error", string(payload)),
	)
	return len(payload), nil
}

func newLifecycleDiagnosticLogger(lifecycle *requestLifecycle) *log.Logger {
	return log.New(lifecycleDiagnosticWriter{lifecycle: lifecycle}, "", 0)
}
