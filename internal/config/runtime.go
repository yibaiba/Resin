package config

import (
	"fmt"
	"net/netip"
	"strings"
	"time"
)

const (
	DefaultUDPProbeTarget     = "1.1.1.1:53"
	DefaultMaxUDPTestInterval = 5 * time.Minute
)

// RuntimeConfig holds all hot-updatable global settings.
// These are persisted in the database and served via GET /system/config.
type RuntimeConfig struct {
	// Request log
	RequestLogEnabled                  bool `json:"request_log_enabled"`
	ReverseProxyLogDetailEnabled       bool `json:"reverse_proxy_log_detail_enabled"`
	ReverseProxyLogReqHeadersMaxBytes  int  `json:"reverse_proxy_log_req_headers_max_bytes"`
	ReverseProxyLogReqBodyMaxBytes     int  `json:"reverse_proxy_log_req_body_max_bytes"`
	ReverseProxyLogRespHeadersMaxBytes int  `json:"reverse_proxy_log_resp_headers_max_bytes"`
	ReverseProxyLogRespBodyMaxBytes    int  `json:"reverse_proxy_log_resp_body_max_bytes"`

	// Health check
	MaxConsecutiveFailures          int      `json:"max_consecutive_failures"`
	MaxLatencyTestInterval          Duration `json:"max_latency_test_interval"`
	MaxAuthorityLatencyTestInterval Duration `json:"max_authority_latency_test_interval"`
	MaxEgressTestInterval           Duration `json:"max_egress_test_interval"`
	MaxUDPTestInterval              Duration `json:"max_udp_test_interval"`

	// Probe
	LatencyTestURL     string   `json:"latency_test_url"`
	LatencyAuthorities []string `json:"latency_authorities"`
	UDPProbeTarget     string   `json:"udp_probe_target"`

	// Node DNS: selected upstream URIs queried in parallel by the sing-box builder.
	NodeDNSUpstreams []string `json:"node_dns_upstreams"`

	// P2C
	P2CLatencyWindow   Duration `json:"p2c_latency_window"`
	LatencyDecayWindow Duration `json:"latency_decay_window"`

	// Persistence
	CacheFlushInterval       Duration `json:"cache_flush_interval"`
	CacheFlushDirtyThreshold int      `json:"cache_flush_dirty_threshold"`
}

// NewDefaultRuntimeConfig returns a RuntimeConfig populated with the default
// values specified in DESIGN.md §运行时全局设置项.
func NewDefaultRuntimeConfig() *RuntimeConfig {
	return &RuntimeConfig{
		RequestLogEnabled:                  true,
		ReverseProxyLogDetailEnabled:       false,
		ReverseProxyLogReqHeadersMaxBytes:  4096,
		ReverseProxyLogReqBodyMaxBytes:     1024,
		ReverseProxyLogRespHeadersMaxBytes: 1024,
		ReverseProxyLogRespBodyMaxBytes:    1024,

		MaxConsecutiveFailures:          3,
		MaxLatencyTestInterval:          Duration(1 * time.Hour),
		MaxAuthorityLatencyTestInterval: Duration(3 * time.Hour),
		MaxEgressTestInterval:           Duration(24 * time.Hour),
		MaxUDPTestInterval:              Duration(DefaultMaxUDPTestInterval),

		LatencyTestURL:     "https://www.gstatic.com/generate_204",
		LatencyAuthorities: []string{"gstatic.com", "google.com", "cloudflare.com", "github.com"},
		UDPProbeTarget:     DefaultUDPProbeTarget,
		NodeDNSUpstreams:   DefaultNodeDNSUpstreams(),

		P2CLatencyWindow:   Duration(10 * time.Minute),
		LatencyDecayWindow: Duration(10 * time.Minute),

		CacheFlushInterval:       Duration(5 * time.Minute),
		CacheFlushDirtyThreshold: 1000,
	}
}

// ParseUDPProbeTarget validates an IP address with a non-zero UDP port.
func ParseUDPProbeTarget(value string) (netip.AddrPort, error) {
	target := strings.TrimSpace(value)
	addrPort, err := netip.ParseAddrPort(target)
	if err != nil {
		return netip.AddrPort{}, fmt.Errorf("must be an IP address with port: %w", err)
	}
	if addrPort.Port() == 0 {
		return netip.AddrPort{}, fmt.Errorf("port must be non-zero")
	}
	if addrPort.Addr().Zone() != "" {
		return netip.AddrPort{}, fmt.Errorf("IPv6 zones are not supported")
	}
	return addrPort, nil
}
