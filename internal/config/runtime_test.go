package config

import (
	"encoding/json"
	"testing"
	"time"
)

func TestNewDefaultRuntimeConfig(t *testing.T) {
	cfg := NewDefaultRuntimeConfig()

	if cfg.RequestLogEnabled != true {
		t.Errorf("RequestLogEnabled: got %v, want true", cfg.RequestLogEnabled)
	}
	if cfg.MaxConsecutiveFailures != 3 {
		t.Errorf("MaxConsecutiveFailures: got %d, want 3", cfg.MaxConsecutiveFailures)
	}
	if cfg.CacheFlushDirtyThreshold != 1000 {
		t.Errorf("CacheFlushDirtyThreshold: got %d, want 1000", cfg.CacheFlushDirtyThreshold)
	}
	if len(cfg.LatencyAuthorities) != 4 {
		t.Errorf("LatencyAuthorities: got %d items, want 4", len(cfg.LatencyAuthorities))
	}
	if cfg.UDPProbeTarget != DefaultUDPProbeTarget {
		t.Errorf("UDPProbeTarget: got %q, want %q", cfg.UDPProbeTarget, DefaultUDPProbeTarget)
	}
	if time.Duration(cfg.MaxUDPTestInterval) != DefaultMaxUDPTestInterval {
		t.Errorf("MaxUDPTestInterval: got %v, want %v", cfg.MaxUDPTestInterval, DefaultMaxUDPTestInterval)
	}
	wantDNS := DefaultNodeDNSUpstreams()
	if len(cfg.NodeDNSUpstreams) != len(wantDNS) {
		t.Fatalf("NodeDNSUpstreams: got %d items, want %d", len(cfg.NodeDNSUpstreams), len(wantDNS))
	}
	for i, upstream := range wantDNS {
		if cfg.NodeDNSUpstreams[i] != upstream {
			t.Errorf("NodeDNSUpstreams[%d]: got %q, want %q", i, cfg.NodeDNSUpstreams[i], upstream)
		}
	}
}

func TestRuntimeConfig_JSONRoundTrip(t *testing.T) {
	original := NewDefaultRuntimeConfig()

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}

	var decoded RuntimeConfig
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}

	// Spot-check key fields after round-trip
	if decoded.MaxConsecutiveFailures != original.MaxConsecutiveFailures {
		t.Errorf("MaxConsecutiveFailures: got %d, want %d", decoded.MaxConsecutiveFailures, original.MaxConsecutiveFailures)
	}
}

func TestDuration_JSON(t *testing.T) {
	d := Duration(5 * time.Minute)

	data, err := json.Marshal(d)
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}
	if string(data) != `"5m0s"` {
		t.Errorf("marshal: got %s, want %q", data, "5m0s")
	}

	var decoded Duration
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	if time.Duration(decoded) != 5*time.Minute {
		t.Errorf("unmarshal: got %v, want 5m", time.Duration(decoded))
	}
}

func TestDuration_JSONInvalid(t *testing.T) {
	var d Duration
	err := json.Unmarshal([]byte(`"not-a-duration"`), &d)
	if err == nil {
		t.Fatal("expected error for invalid duration string")
	}

	err = json.Unmarshal([]byte(`123`), &d)
	if err == nil {
		t.Fatal("expected error for non-string duration")
	}
}

func TestRuntimeConfig_JSONFieldNames(t *testing.T) {
	cfg := NewDefaultRuntimeConfig()
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}

	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("unmarshal to map error: %v", err)
	}

	// Check that JSON keys match the DESIGN.md GET /system/config response
	expectedKeys := []string{
		"request_log_enabled",
		"reverse_proxy_log_detail_enabled",
		"reverse_proxy_log_req_headers_max_bytes",
		"reverse_proxy_log_req_body_max_bytes",
		"reverse_proxy_log_resp_headers_max_bytes",
		"reverse_proxy_log_resp_body_max_bytes",
		"max_consecutive_failures",
		"max_latency_test_interval",
		"max_authority_latency_test_interval",
		"max_egress_test_interval",
		"max_udp_test_interval",
		"latency_test_url",
		"latency_authorities",
		"udp_probe_target",
		"node_dns_upstreams",
		"p2c_latency_window",
		"latency_decay_window",
		"cache_flush_interval",
		"cache_flush_dirty_threshold",
	}

	for _, key := range expectedKeys {
		if _, ok := m[key]; !ok {
			t.Errorf("missing JSON key: %q", key)
		}
	}
}

func TestParseUDPProbeTarget(t *testing.T) {
	tests := []struct {
		name      string
		value     string
		want      string
		wantError bool
	}{
		{name: "IPv4", value: " 127.0.0.1:5353 ", want: "127.0.0.1:5353"},
		{name: "IPv6", value: "[2001:db8::1]:53", want: "[2001:db8::1]:53"},
		{name: "hostname", value: "dns.example:53", wantError: true},
		{name: "zero port", value: "127.0.0.1:0", wantError: true},
		{name: "missing port", value: "127.0.0.1", wantError: true},
		{name: "IPv6 zone", value: "[fe80::1%lo0]:53", wantError: true},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			got, err := ParseUDPProbeTarget(testCase.value)
			if testCase.wantError {
				if err == nil {
					t.Fatalf("ParseUDPProbeTarget(%q) unexpectedly succeeded", testCase.value)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseUDPProbeTarget(%q): %v", testCase.value, err)
			}
			if got.String() != testCase.want {
				t.Fatalf("target: got %q, want %q", got, testCase.want)
			}
		})
	}
}
