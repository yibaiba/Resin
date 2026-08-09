package routing

import (
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// Target describes a normalized routing destination. Port 0 means unknown.
type Target struct {
	Host string     `json:"host"`
	Port uint16     `json:"port"`
	Addr netip.Addr `json:"-"`
}

// ParseTarget accepts an absolute URL, authority, bracketed IPv6, or bare host.
func ParseTarget(raw string) (Target, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return Target{}, fmt.Errorf("target is required")
	}
	if strings.Contains(value, "://") || strings.HasPrefix(value, "//") {
		parsed, err := url.Parse(value)
		if err != nil || parsed.Host == "" {
			return Target{}, fmt.Errorf("invalid target URL")
		}
		return targetFromURL(parsed)
	}
	return targetFromAuthority(value)
}

func targetFromURL(parsed *url.URL) (Target, error) {
	port, err := parseTargetPort(parsed.Port())
	if err != nil {
		return Target{}, err
	}
	if port == 0 {
		switch strings.ToLower(parsed.Scheme) {
		case "http":
			port = 80
		case "https":
			port = 443
		}
	}
	return normalizedTarget(parsed.Hostname(), port)
}

func targetFromAuthority(value string) (Target, error) {
	if host, portText, err := net.SplitHostPort(value); err == nil {
		port, portErr := parseTargetPort(portText)
		if portErr != nil {
			return Target{}, portErr
		}
		return normalizedTarget(host, port)
	}
	if strings.HasPrefix(value, "[") && strings.HasSuffix(value, "]") {
		return normalizedTarget(strings.Trim(value, "[]"), 0)
	}
	if strings.ContainsAny(value, "/?#") {
		return Target{}, fmt.Errorf("target must be a URL, host, or host:port")
	}
	return normalizedTarget(value, 0)
}

func parseTargetPort(raw string) (uint16, error) {
	if raw == "" {
		return 0, nil
	}
	port, err := strconv.ParseUint(raw, 10, 16)
	if err != nil || port == 0 {
		return 0, fmt.Errorf("invalid target port %q", raw)
	}
	return uint16(port), nil
}

func normalizedTarget(host string, port uint16) (Target, error) {
	host = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(strings.Trim(host, "[]"))), ".")
	if host == "" {
		return Target{}, fmt.Errorf("target host is required")
	}
	addr, _ := netip.ParseAddr(stripIPv6Zone(host))
	return Target{Host: host, Port: port, Addr: addr}, nil
}

// TargetBypassMatcher decides whether a target should be dialed directly.
type TargetBypassMatcher struct {
	rules []targetBypassRule
}

type targetBypassRule struct {
	local bool
	cidr  netip.Prefix
	exact string
	glob  *regexp.Regexp
}

func NewTargetBypassMatcher(patterns []string) *TargetBypassMatcher {
	matcher := &TargetBypassMatcher{}
	for _, pattern := range patterns {
		if rule, ok := compileTargetBypassRule(pattern); ok {
			matcher.rules = append(matcher.rules, rule)
		}
	}
	if len(matcher.rules) == 0 {
		return nil
	}
	return matcher
}

func (m *TargetBypassMatcher) ShouldBypass(raw string) bool {
	if m == nil {
		return false
	}
	target, err := ParseTarget(raw)
	if err != nil {
		return false
	}
	for _, rule := range m.rules {
		if rule.matches(target) {
			return true
		}
	}
	return false
}

func (r targetBypassRule) matches(target Target) bool {
	switch {
	case r.local:
		return target.Host == "localhost" || (!target.Addr.IsValid() && !strings.Contains(target.Host, "."))
	case r.cidr.IsValid():
		return target.Addr.IsValid() && r.cidr.Contains(target.Addr)
	case r.glob != nil:
		return r.glob.MatchString(target.Host)
	default:
		return r.exact != "" && target.Host == r.exact
	}
}

func compileTargetBypassRule(raw string) (targetBypassRule, bool) {
	pattern := strings.TrimSpace(raw)
	if pattern == "" {
		return targetBypassRule{}, false
	}
	if strings.EqualFold(pattern, "<local>") {
		return targetBypassRule{local: true}, true
	}
	if prefix, err := netip.ParsePrefix(stripIPv6Zone(strings.Trim(pattern, "[]"))); err == nil {
		return targetBypassRule{cidr: prefix}, true
	}
	target, err := ParseTarget(pattern)
	if err != nil {
		return targetBypassRule{}, false
	}
	if strings.ContainsAny(target.Host, "*?") {
		quoted := regexp.QuoteMeta(target.Host)
		quoted = strings.ReplaceAll(quoted, `\*`, ".*")
		quoted = strings.ReplaceAll(quoted, `\?`, ".")
		return targetBypassRule{glob: regexp.MustCompile("^" + quoted + "$")}, true
	}
	return targetBypassRule{exact: target.Host}, true
}

func stripIPv6Zone(host string) string {
	if index := strings.LastIndexByte(host, '%'); index >= 0 {
		return host[:index]
	}
	return host
}
