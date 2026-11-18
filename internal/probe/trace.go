package probe

import (
	"bytes"
	"errors"
	"net/netip"
	"strings"
)

// ParseCloudflareTrace parses a Cloudflare /cdn-cgi/trace response body
// and extracts the "ip" field plus optional "loc" field.
//
// Example response body:
//
//	fl=123
//	h=1.2.3.4
//	ip=203.0.113.1
//	ts=1234567890
//	...
func ParseCloudflareTrace(body []byte) (netip.Addr, *string, error) {
	var (
		ip       netip.Addr
		ipFound  bool
		locValue string
		locSet   bool
	)

	for _, line := range bytes.Split(body, []byte("\n")) {
		if bytes.HasPrefix(line, []byte("ip=")) {
			raw := string(bytes.TrimSpace(line[3:]))
			addr, err := netip.ParseAddr(raw)
			if err != nil {
				return netip.Addr{}, nil, err
			}
			ip = addr
			ipFound = true
			continue
		}
		if bytes.HasPrefix(line, []byte("loc=")) {
			locValue = strings.ToLower(strings.TrimSpace(string(line[4:])))
			locSet = true
		}
	}

	if !ipFound {
		return netip.Addr{}, nil, errors.New("probe: ip field not found in trace response")
	}
	if !locSet || locValue == "" {
		return ip, nil, nil
	}
	return ip, &locValue, nil
}

// ParseEgressBody accepts a Cloudflare /cdn-cgi/trace body or a plain IP
// (ipify-style). Residential landings often cannot fetch cloudflare.com.
func ParseEgressBody(body []byte) (netip.Addr, *string, error) {
	if ip, loc, err := ParseCloudflareTrace(body); err == nil {
		return ip, loc, nil
	}
	raw := strings.TrimSpace(string(body))
	if addr, err := netip.ParseAddr(raw); err == nil {
		return addr, nil, nil
	}
	return ParseCloudflareTrace(body)
}
