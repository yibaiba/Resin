package outbound

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Resinat/Resin/internal/config"
	"github.com/Resinat/Resin/internal/testutil"
	"github.com/sagernet/sing-box/adapter"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	M "github.com/sagernet/sing/common/metadata"

	mDNS "github.com/miekg/dns"
)

func newTestSingboxBuilder(t *testing.T) *SingboxBuilder {
	t.Helper()
	b, err := NewSingboxBuilderWithConfig(SingboxBuilderConfig{
		DNSUpstreams: config.DefaultNodeDNSUpstreams(),
	})
	if err != nil {
		t.Fatalf("NewSingboxBuilderWithConfig() error: %v", err)
	}
	return b
}

// ---------------------------------------------------------------------------
// SingboxBuilder constructor / teardown
// ---------------------------------------------------------------------------

func TestNewSingboxBuilderWithConfig(t *testing.T) {
	b := newTestSingboxBuilder(t)
	if err := b.Close(); err != nil {
		t.Fatalf("SingboxBuilder.Close() error: %v", err)
	}
}

func TestNewSingboxBuilder_ConfiguresSecureDNSChain(t *testing.T) {
	b := newTestSingboxBuilder(t)
	defer b.Close()

	defaultTransport := b.dnsTransportManager.Default()
	if defaultTransport == nil {
		t.Fatal("expected default DNS transport")
	}
	if defaultTransport.Tag() != secureDNSFailoverTransportTag {
		t.Fatalf("default DNS transport: got %q, want %q", defaultTransport.Tag(), secureDNSFailoverTransportTag)
	}

	for _, tag := range []string{
		localDNSTransportTag,
		customDNSUpstreamTransportTag(0),
		customDNSUpstreamTransportTag(1),
		customDNSUpstreamTransportTag(2),
		secureDNSFailoverTransportTag,
	} {
		transport, ok := b.dnsTransportManager.Transport(tag)
		if !ok || transport == nil {
			t.Fatalf("expected DNS transport %q to be registered", tag)
		}
	}

	dohPub, _ := b.dnsTransportManager.Transport(customDNSUpstreamTransportTag(0))
	if got := dohPub.Dependencies(); len(got) != 1 || got[0] != localDNSTransportTag {
		t.Fatalf("DoH pub dependencies: got %v, want [%s]", got, localDNSTransportTag)
	}

	aliDoH, _ := b.dnsTransportManager.Transport(customDNSUpstreamTransportTag(1))
	if got := aliDoH.Dependencies(); len(got) != 1 || got[0] != localDNSTransportTag {
		t.Fatalf("AliDNS DoH dependencies: got %v, want [%s]", got, localDNSTransportTag)
	}

	failover, _ := b.dnsTransportManager.Transport(secureDNSFailoverTransportTag)
	wantFailoverDeps := []string{
		customDNSUpstreamTransportTag(0),
		customDNSUpstreamTransportTag(1),
		customDNSUpstreamTransportTag(2),
		localDNSTransportTag,
	}
	if got := failover.Dependencies(); !equalStrings(got, wantFailoverDeps) {
		t.Fatalf("secure DNS dependencies: got %v, want %v", got, wantFailoverDeps)
	}
}

func TestNewSingboxBuilderWithConfig_ConfiguresCustomDNSChain(t *testing.T) {
	b, err := NewSingboxBuilderWithConfig(SingboxBuilderConfig{
		DNSUpstreams: []string{"udp://127.0.0.1", "local"},
	})
	if err != nil {
		t.Fatalf("NewSingboxBuilderWithConfig() error: %v", err)
	}
	defer b.Close()

	defaultTransport := b.dnsTransportManager.Default()
	if defaultTransport == nil {
		t.Fatal("expected default DNS transport")
	}
	if defaultTransport.Tag() != secureDNSFailoverTransportTag {
		t.Fatalf("default DNS transport: got %q, want %q", defaultTransport.Tag(), secureDNSFailoverTransportTag)
	}

	custom, ok := b.dnsTransportManager.Transport(customDNSUpstreamTransportTag(0))
	if !ok || custom == nil {
		t.Fatalf("expected custom DNS transport %q to be registered", customDNSUpstreamTransportTag(0))
	}
	if custom.Type() != C.DNSTypeUDP {
		t.Fatalf("custom DNS transport type: got %q, want %q", custom.Type(), C.DNSTypeUDP)
	}

	failover, _ := b.dnsTransportManager.Transport(secureDNSFailoverTransportTag)
	wantFailoverDeps := []string{customDNSUpstreamTransportTag(0), localDNSTransportTag}
	if got := failover.Dependencies(); !equalStrings(got, wantFailoverDeps) {
		t.Fatalf("custom DNS dependencies: got %v, want %v", got, wantFailoverDeps)
	}
}

func TestSecureDNSTransportSpecs(t *testing.T) {
	specs, err := secureDNSTransportSpecsForUpstreams(config.DefaultNodeDNSUpstreams())
	if err != nil {
		t.Fatalf("secureDNSTransportSpecsForUpstreams() error: %v", err)
	}
	if len(specs) != 5 {
		t.Fatalf("default DNS specs length: got %d, want 5", len(specs))
	}

	specByTag := make(map[string]secureDNSTransportSpec, len(specs))
	for _, spec := range specs {
		specByTag[spec.tag] = spec
	}

	dohPubSpec, ok := specByTag[customDNSUpstreamTransportTag(0)]
	if !ok {
		t.Fatalf("missing spec for %q", customDNSUpstreamTransportTag(0))
	}
	dohPubOptions, ok := dohPubSpec.options.(*option.RemoteHTTPSDNSServerOptions)
	if !ok {
		t.Fatalf("DoH pub options type: got %T", dohPubSpec.options)
	}
	if dohPubOptions.Path != secureDNSQueryPath {
		t.Fatalf("DoH pub path: got %q, want %q", dohPubOptions.Path, secureDNSQueryPath)
	}
	if dohPubOptions.DomainResolver == nil || dohPubOptions.DomainResolver.Server != localDNSTransportTag {
		t.Fatalf("DoH pub bootstrap resolver: got %+v, want %q", dohPubOptions.DomainResolver, localDNSTransportTag)
	}

	aliDoTSpec, ok := specByTag[customDNSUpstreamTransportTag(2)]
	if !ok {
		t.Fatalf("missing spec for %q", customDNSUpstreamTransportTag(2))
	}
	aliDoTOptions, ok := aliDoTSpec.options.(*option.RemoteTLSDNSServerOptions)
	if !ok {
		t.Fatalf("AliDNS DoT options type: got %T", aliDoTSpec.options)
	}
	if aliDoTOptions.Server != "223.5.5.5" {
		t.Fatalf("AliDNS DoT server: got %q, want 223.5.5.5", aliDoTOptions.Server)
	}
	if aliDoTOptions.TLS == nil || aliDoTOptions.TLS.ServerName != "dns.alidns.com" {
		t.Fatalf("AliDNS DoT TLS server_name: got %+v, want dns.alidns.com", aliDoTOptions.TLS)
	}

	failoverSpec, ok := specByTag[secureDNSFailoverTransportTag]
	if !ok {
		t.Fatalf("missing spec for %q", secureDNSFailoverTransportTag)
	}
	failoverOptions, ok := failoverSpec.options.(*secureDNSFailoverOptions)
	if !ok {
		t.Fatalf("secure DNS failover options type: got %T", failoverSpec.options)
	}
	wantUpstreams := []string{
		customDNSUpstreamTransportTag(0),
		customDNSUpstreamTransportTag(1),
		customDNSUpstreamTransportTag(2),
		localDNSTransportTag,
	}
	if !equalStrings(failoverOptions.Upstreams, wantUpstreams) {
		t.Fatalf("secure DNS upstreams: got %v, want %v", failoverOptions.Upstreams, wantUpstreams)
	}
}

func TestSecureDNSTransportSpecsForUpstreams_CustomURIList(t *testing.T) {
	specs, err := secureDNSTransportSpecsForUpstreams([]string{
		"udp://10.0.0.53",
		"tls://223.5.5.5?sni=dns.alidns.com",
		"https://dns.example.com/dns-query",
		"local",
	})
	if err != nil {
		t.Fatalf("secureDNSTransportSpecsForUpstreams() error: %v", err)
	}
	if len(specs) != 5 {
		t.Fatalf("custom specs length: got %d, want 5", len(specs))
	}

	specByTag := make(map[string]secureDNSTransportSpec, len(specs))
	for _, spec := range specs {
		specByTag[spec.tag] = spec
	}
	if _, ok := specByTag[localDNSTransportTag]; !ok {
		t.Fatalf("missing local DNS transport for custom bootstrap")
	}

	udpSpec := specByTag[customDNSUpstreamTransportTag(0)]
	if udpSpec.transportType != C.DNSTypeUDP {
		t.Fatalf("udp transport type: got %q, want %q", udpSpec.transportType, C.DNSTypeUDP)
	}
	udpOptions, ok := udpSpec.options.(*option.RemoteDNSServerOptions)
	if !ok {
		t.Fatalf("udp options type: got %T", udpSpec.options)
	}
	if udpOptions.Server != "10.0.0.53" || udpOptions.ServerPort != 0 {
		t.Fatalf("udp server: got %s:%d", udpOptions.Server, udpOptions.ServerPort)
	}
	if udpOptions.DomainResolver != nil {
		t.Fatalf("udp IP upstream should not need bootstrap resolver: %+v", udpOptions.DomainResolver)
	}

	tlsSpec := specByTag[customDNSUpstreamTransportTag(1)]
	if tlsSpec.transportType != C.DNSTypeTLS {
		t.Fatalf("tls transport type: got %q, want %q", tlsSpec.transportType, C.DNSTypeTLS)
	}
	tlsOptions, ok := tlsSpec.options.(*option.RemoteTLSDNSServerOptions)
	if !ok {
		t.Fatalf("tls options type: got %T", tlsSpec.options)
	}
	if tlsOptions.Server != "223.5.5.5" {
		t.Fatalf("tls server: got %q", tlsOptions.Server)
	}
	if tlsOptions.TLS == nil || tlsOptions.TLS.ServerName != "dns.alidns.com" {
		t.Fatalf("tls server_name: got %+v, want dns.alidns.com", tlsOptions.TLS)
	}

	httpsSpec := specByTag[customDNSUpstreamTransportTag(2)]
	if httpsSpec.transportType != C.DNSTypeHTTPS {
		t.Fatalf("https transport type: got %q, want %q", httpsSpec.transportType, C.DNSTypeHTTPS)
	}
	httpsOptions, ok := httpsSpec.options.(*option.RemoteHTTPSDNSServerOptions)
	if !ok {
		t.Fatalf("https options type: got %T", httpsSpec.options)
	}
	if httpsOptions.Server != "dns.example.com" {
		t.Fatalf("https server: got %q", httpsOptions.Server)
	}
	if httpsOptions.Path != secureDNSQueryPath {
		t.Fatalf("https path: got %q, want %q", httpsOptions.Path, secureDNSQueryPath)
	}
	if httpsOptions.DomainResolver == nil || httpsOptions.DomainResolver.Server != localDNSTransportTag {
		t.Fatalf("https bootstrap resolver: got %+v, want %q", httpsOptions.DomainResolver, localDNSTransportTag)
	}

	failoverOptions, ok := specByTag[secureDNSFailoverTransportTag].options.(*secureDNSFailoverOptions)
	if !ok {
		t.Fatalf("failover options type: got %T", specByTag[secureDNSFailoverTransportTag].options)
	}
	wantUpstreams := []string{
		customDNSUpstreamTransportTag(0),
		customDNSUpstreamTransportTag(1),
		customDNSUpstreamTransportTag(2),
		localDNSTransportTag,
	}
	if !equalStrings(failoverOptions.Upstreams, wantUpstreams) {
		t.Fatalf("custom DNS upstreams: got %v, want %v", failoverOptions.Upstreams, wantUpstreams)
	}
}

func TestSecureDNSTransportSpecsForUpstreams_InvalidURI(t *testing.T) {
	_, err := secureDNSTransportSpecsForUpstreams([]string{"https://dns.example.com/dns-query?headers=x"})
	if err == nil {
		t.Fatal("expected invalid query parameter error")
	}
	if !strings.Contains(err.Error(), `unsupported query parameter "headers"`) {
		t.Fatalf("error: got %v", err)
	}
}

func TestSecureDNSFailoverTransport_MergesSuccessfulAnswers(t *testing.T) {
	first := newStaticDNSTransport("first", successDNSResponseWithA("node.example.", net.IPv4(132, 226, 73, 28), 300))
	second := newStaticDNSTransport("second", successDNSResponseWithA("node.example.", net.IPv4(161, 153, 92, 18), 5))
	manager := &stubDNSTransportManager{
		transports: map[string]adapter.DNSTransport{
			"first":  first,
			"second": second,
		},
	}
	transport := &secureDNSFailoverTransport{
		manager:      manager,
		tag:          secureDNSFailoverTransportTag,
		upstreamTags: []string{"first", "second"},
	}

	resp, err := transport.Exchange(context.Background(), dnsQuestion("example.com."))
	if err != nil {
		t.Fatalf("Exchange() error: %v", err)
	}
	got := ipv4Answers(resp)
	if !containsAllStrings(got, "132.226.73.28", "161.153.92.18") {
		t.Fatalf("merged A records: got %v", got)
	}
	if ttl := resp.Answer[0].Header().Ttl; ttl != 5 {
		t.Fatalf("merged TTL: got %d, want 5", ttl)
	}
	if first.calls.Load() != 1 {
		t.Fatalf("first transport calls: got %d, want 1", first.calls.Load())
	}
	if second.calls.Load() != 1 {
		t.Fatalf("second transport calls: got %d, want 1", second.calls.Load())
	}
}

func TestSecureDNSFailoverTransport_DedupsMergedAddressRecords(t *testing.T) {
	first := newStaticDNSTransport("first", successDNSResponseWithA("node.example.", net.IPv4(132, 226, 73, 28), 300))
	second := newStaticDNSTransport("second", successDNSResponseWithA("node.example.", net.IPv4(132, 226, 73, 28), 5))
	transport := &secureDNSFailoverTransport{
		manager: &stubDNSTransportManager{
			transports: map[string]adapter.DNSTransport{
				"first":  first,
				"second": second,
			},
		},
		tag:          secureDNSFailoverTransportTag,
		upstreamTags: []string{"first", "second"},
	}

	resp, err := transport.Exchange(context.Background(), dnsQuestion("example.com."))
	if err != nil {
		t.Fatalf("Exchange() error: %v", err)
	}
	got := ipv4Answers(resp)
	if len(got) != 1 || got[0] != "132.226.73.28" {
		t.Fatalf("deduped A records: got %v", got)
	}
	if ttl := resp.Answer[0].Header().Ttl; ttl != 5 {
		t.Fatalf("deduped TTL: got %d, want 5", ttl)
	}
}

func TestSecureDNSFailoverTransport_TimeoutKeepsSuccessfulAnswers(t *testing.T) {
	first := newStaticDNSTransport("first", successDNSResponseWithA("node.example.", net.IPv4(132, 226, 73, 28), 60))
	second := newDelayedDNSTransport("second", time.Second, errors.New("slow upstream"))
	transport := &secureDNSFailoverTransport{
		manager: &stubDNSTransportManager{
			transports: map[string]adapter.DNSTransport{
				"first":  first,
				"second": second,
			},
		},
		tag:          secureDNSFailoverTransportTag,
		upstreamTags: []string{"first", "second"},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	resp, err := transport.Exchange(ctx, dnsQuestion("example.com."))
	if err != nil {
		t.Fatalf("Exchange() error: %v", err)
	}
	got := ipv4Answers(resp)
	if len(got) != 1 || got[0] != "132.226.73.28" {
		t.Fatalf("partial success A records: got %v", got)
	}
	if first.calls.Load() != 1 {
		t.Fatalf("first transport calls: got %d, want 1", first.calls.Load())
	}
	if second.calls.Load() != 1 {
		t.Fatalf("second transport calls: got %d, want 1", second.calls.Load())
	}
}

func TestMergeSecureDNSResponses_IncludesAAAA(t *testing.T) {
	primary := successDNSResponseWithA("node.example.", net.IPv4(132, 226, 73, 28), 300)
	extra := &mDNS.Msg{
		MsgHdr: mDNS.MsgHdr{Response: true, Rcode: mDNS.RcodeSuccess},
		Answer: []mDNS.RR{
			&mDNS.AAAA{
				Hdr: mDNS.RR_Header{
					Name:   "node.example.",
					Rrtype: mDNS.TypeAAAA,
					Class:  mDNS.ClassINET,
					Ttl:    10,
				},
				AAAA: net.ParseIP("2001:db8::1"),
			},
		},
	}

	merged := mergeSecureDNSResponses(primary, extra)
	gotA := ipv4Answers(merged)
	if len(gotA) != 1 || gotA[0] != "132.226.73.28" {
		t.Fatalf("merged A records: got %v", gotA)
	}
	var gotAAAA string
	for _, rr := range merged.Answer {
		aaaa, ok := rr.(*mDNS.AAAA)
		if !ok || aaaa.AAAA == nil {
			continue
		}
		gotAAAA = aaaa.AAAA.String()
	}
	if gotAAAA != "2001:db8::1" {
		t.Fatalf("merged AAAA: got %q", gotAAAA)
	}
	if ttl := merged.Answer[0].Header().Ttl; ttl != 10 {
		t.Fatalf("merged min TTL: got %d, want 10", ttl)
	}
}

func TestSecureDNSFailoverTransport_RcodeFailureFallsBack(t *testing.T) {
	firstTag := "first"
	manager := &stubDNSTransportManager{
		transports: map[string]adapter.DNSTransport{
			firstTag:             newStaticDNSTransport(firstTag, rcodeDNSResponse(mDNS.RcodeServerFailure)),
			localDNSTransportTag: newStaticDNSTransport(localDNSTransportTag, successDNSResponse("local.example.")),
		},
	}
	transport := &secureDNSFailoverTransport{
		manager:      manager,
		tag:          secureDNSFailoverTransportTag,
		upstreamTags: []string{firstTag, localDNSTransportTag},
	}

	resp, err := transport.Exchange(context.Background(), dnsQuestion("example.com."))
	if err != nil {
		t.Fatalf("Exchange() error: %v", err)
	}
	if resp == nil || resp.Rcode != mDNS.RcodeSuccess {
		t.Fatalf("response rcode: got %+v, want success", resp)
	}
	first, _ := manager.Transport(firstTag)
	second, _ := manager.Transport(localDNSTransportTag)
	if first.(*staticDNSTransport).calls.Load() != 1 {
		t.Fatalf("first transport calls: got %d, want 1", first.(*staticDNSTransport).calls.Load())
	}
	if second.(*staticDNSTransport).calls.Load() != 1 {
		t.Fatalf("local transport calls: got %d, want 1", second.(*staticDNSTransport).calls.Load())
	}
}

func TestSecureDNSFailoverTransport_NameErrorFallsBack(t *testing.T) {
	firstTag := "first"
	manager := &stubDNSTransportManager{
		transports: map[string]adapter.DNSTransport{
			firstTag:             newStaticDNSTransport(firstTag, rcodeDNSResponse(mDNS.RcodeNameError)),
			localDNSTransportTag: newStaticDNSTransport(localDNSTransportTag, successDNSResponse("local.example.")),
		},
	}
	transport := &secureDNSFailoverTransport{
		manager:      manager,
		tag:          secureDNSFailoverTransportTag,
		upstreamTags: []string{firstTag, localDNSTransportTag},
	}

	resp, err := transport.Exchange(context.Background(), dnsQuestion("example.com."))
	if err != nil {
		t.Fatalf("Exchange() error: %v", err)
	}
	if resp == nil || resp.Rcode != mDNS.RcodeSuccess {
		t.Fatalf("response rcode: got %+v, want success", resp)
	}
	first, _ := manager.Transport(firstTag)
	second, _ := manager.Transport(localDNSTransportTag)
	if first.(*staticDNSTransport).calls.Load() != 1 {
		t.Fatalf("first transport calls: got %d, want 1", first.(*staticDNSTransport).calls.Load())
	}
	if second.(*staticDNSTransport).calls.Load() != 1 {
		t.Fatalf("local transport calls: got %d, want 1", second.(*staticDNSTransport).calls.Load())
	}
}

func TestSecureDNSFailoverTransport_AllUpstreamsFail(t *testing.T) {
	dohPubTag := "doh-pub"
	aliDoHTag := "alidns-doh"
	aliDoTTag := "alidns-dot"
	manager := &stubDNSTransportManager{
		transports: map[string]adapter.DNSTransport{
			dohPubTag:            newErrorDNSTransport(dohPubTag, errors.New("doh.pub down")),
			aliDoHTag:            newErrorDNSTransport(aliDoHTag, errors.New("alidns doh down")),
			aliDoTTag:            newErrorDNSTransport(aliDoTTag, errors.New("alidns dot down")),
			localDNSTransportTag: newErrorDNSTransport(localDNSTransportTag, errors.New("local down")),
		},
	}
	transport := &secureDNSFailoverTransport{
		manager:      manager,
		tag:          secureDNSFailoverTransportTag,
		upstreamTags: []string{dohPubTag, aliDoHTag, aliDoTTag, localDNSTransportTag},
	}

	_, err := transport.Exchange(context.Background(), dnsQuestion("example.com."))
	if err == nil {
		t.Fatal("expected error when all upstreams fail")
	}
	for _, part := range []string{
		dohPubTag,
		aliDoHTag,
		aliDoTTag,
		localDNSTransportTag,
	} {
		if !strings.Contains(err.Error(), part) {
			t.Fatalf("error %q does not contain %q", err, part)
		}
	}
}

// ---------------------------------------------------------------------------
// Build: parse and create real outbound
// ---------------------------------------------------------------------------

func TestSingboxBuilder_ParseShadowsocks(t *testing.T) {
	b := newTestSingboxBuilder(t)
	defer b.Close()

	raw := json.RawMessage(`{
		"type": "shadowsocks",
		"tag":  "test-ss",
		"server": "127.0.0.1",
		"server_port": 8388,
		"method": "aes-256-gcm",
		"password": "test-password"
	}`)
	ob, err := b.Build(raw)
	if err != nil {
		t.Fatalf("Build(shadowsocks) error: %v", err)
	}

	// Should implement io.Closer (sing-box outbounds do)
	closer, ok := ob.(io.Closer)
	if !ok {
		t.Fatal("expected outbound to implement io.Closer")
	}
	if err := closer.Close(); err != nil {
		t.Fatalf("outbound Close() error: %v", err)
	}
}

func TestSingboxBuilder_ParseExtendedProtocols(t *testing.T) {
	b := newTestSingboxBuilder(t)
	defer b.Close()

	cases := []struct {
		name               string
		raw                json.RawMessage
		missingFeatureHint string
	}{
		{
			name: "socks",
			raw: json.RawMessage(`{
				"type":"socks",
				"tag":"test-socks",
				"server":"127.0.0.1",
				"server_port":1080,
				"version":"5",
				"username":"user",
				"password":"pass"
			}`),
		},
		{
			name: "http",
			raw: json.RawMessage(`{
				"type":"http",
				"tag":"test-http",
				"server":"127.0.0.1",
				"server_port":8080,
				"username":"user",
				"password":"pass"
			}`),
		},
		{
			name: "wireguard",
			raw: json.RawMessage(`{
				"type":"wireguard",
				"tag":"test-wg",
				"server":"127.0.0.1",
				"server_port":2480,
				"local_address":["172.16.0.2/32","fd01::1/128"],
				"private_key":"eCtXsJZ27+4PbhDkHnB923tkUn2Gj59wZw5wFA75MnU=",
				"peer_public_key":"Cr8hWlKvtDt7nrvf+f0brNQQzabAqrjfBvas9pmowjo="
			}`),
			missingFeatureHint: "WireGuard is not included in this build",
		},
		{
			name: "hysteria",
			raw: json.RawMessage(`{
				"type":"hysteria",
				"tag":"test-hysteria",
				"server":"127.0.0.1",
				"server_port":443,
				"auth_str":"password",
				"up_mbps":30,
				"down_mbps":200,
				"tls":{"enabled":true,"insecure":true,"server_name":"example.com"}
			}`),
			missingFeatureHint: "QUIC is not included in this build",
		},
		{
			name: "tuic",
			raw: json.RawMessage(`{
				"type":"tuic",
				"tag":"test-tuic",
				"server":"127.0.0.1",
				"server_port":443,
				"uuid":"00000000-0000-0000-0000-000000000001",
				"password":"password",
				"tls":{"enabled":true,"insecure":true,"server_name":"example.com"}
			}`),
			missingFeatureHint: "QUIC is not included in this build",
		},
		{
			name: "anytls",
			raw: json.RawMessage(`{
				"type":"anytls",
				"tag":"test-anytls",
				"server":"127.0.0.1",
				"server_port":443,
				"password":"password",
				"tls":{"enabled":true,"insecure":true,"server_name":"example.com"}
			}`),
		},
		{
			name: "ssh",
			raw: json.RawMessage(`{
				"type":"ssh",
				"tag":"test-ssh",
				"server":"127.0.0.1",
				"server_port":22,
				"user":"root",
				"password":"password"
			}`),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ob, err := b.Build(tc.raw)
			if err != nil {
				if tc.missingFeatureHint != "" && strings.Contains(err.Error(), tc.missingFeatureHint) {
					t.Skipf("skipping %s: %v", tc.name, err)
					return
				}
				t.Fatalf("Build(%s) error: %v", tc.name, err)
			}
			if ob == nil {
				t.Fatalf("Build(%s) returned nil outbound", tc.name)
			}
			if closer, ok := ob.(io.Closer); ok {
				if err := closer.Close(); err != nil {
					t.Fatalf("Build(%s) outbound Close() error: %v", tc.name, err)
				}
			}
		})
	}
}

func TestSingboxBuilder_ParseDomainServerProtocols(t *testing.T) {
	b := newTestSingboxBuilder(t)
	defer b.Close()

	cases := []struct {
		name               string
		raw                json.RawMessage
		missingFeatureHint string
	}{
		{
			name: "socks-domain",
			raw: json.RawMessage(`{
				"type":"socks",
				"tag":"test-socks-domain",
				"server":"proxy.example.com",
				"server_port":1080,
				"version":"5"
			}`),
		},
		{
			name: "anytls-domain",
			raw: json.RawMessage(`{
				"type":"anytls",
				"tag":"test-anytls-domain",
				"server":"edge.example.com",
				"server_port":443,
				"password":"password",
				"tls":{"enabled":true,"insecure":true,"server_name":"edge.example.com"}
			}`),
		},
		{
			name: "wireguard-domain",
			raw: json.RawMessage(`{
				"type":"wireguard",
				"tag":"test-wg-domain",
				"server":"wg.example.com",
				"server_port":2480,
				"local_address":["172.16.0.2/32","fd01::1/128"],
				"private_key":"eCtXsJZ27+4PbhDkHnB923tkUn2Gj59wZw5wFA75MnU=",
				"peer_public_key":"Cr8hWlKvtDt7nrvf+f0brNQQzabAqrjfBvas9pmowjo="
			}`),
			missingFeatureHint: "WireGuard is not included in this build",
		},
		{
			name: "ssh-domain",
			raw: json.RawMessage(`{
				"type":"ssh",
				"tag":"test-ssh-domain",
				"server":"ssh.example.com",
				"server_port":22,
				"user":"root",
				"password":"password"
			}`),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ob, err := b.Build(tc.raw)
			if err != nil {
				if tc.missingFeatureHint != "" && strings.Contains(err.Error(), tc.missingFeatureHint) {
					t.Skipf("skipping %s: %v", tc.name, err)
					return
				}
				t.Fatalf("Build(%s) error: %v", tc.name, err)
			}
			if ob == nil {
				t.Fatalf("Build(%s) returned nil outbound", tc.name)
			}
			if closer, ok := ob.(io.Closer); ok {
				if err := closer.Close(); err != nil {
					t.Fatalf("Build(%s) outbound Close() error: %v", tc.name, err)
				}
			}
		})
	}
}

func TestSingboxBuilder_UnknownType(t *testing.T) {
	b := newTestSingboxBuilder(t)
	defer b.Close()

	raw := json.RawMessage(`{"type": "totally-fake-protocol-xyz", "tag": "x"}`)
	_, err := b.Build(raw)
	if err == nil {
		t.Fatal("expected error for unknown outbound type, got nil")
	}
}

func TestSingboxBuilder_InvalidJSON(t *testing.T) {
	b := newTestSingboxBuilder(t)
	defer b.Close()

	raw := json.RawMessage(`{invalid`)
	_, err := b.Build(raw)
	if err == nil {
		t.Fatal("expected error for invalid JSON, got nil")
	}
}

func TestStubOutboundBuilder_Build(t *testing.T) {
	ob, err := (&testutil.StubOutboundBuilder{}).Build(nil)
	if err != nil {
		t.Fatalf("StubOutboundBuilder.Build() error: %v", err)
	}
	if ob == nil {
		t.Fatal("expected non-nil outbound")
	}
	if ob.Type() != "stub" {
		t.Fatalf("unexpected outbound type: %s", ob.Type())
	}
}

type staticDNSTransport struct {
	tag      string
	response *mDNS.Msg
	err      error
	delay    time.Duration
	calls    atomic.Int32
}

func newStaticDNSTransport(tag string, response *mDNS.Msg) *staticDNSTransport {
	return &staticDNSTransport{tag: tag, response: response}
}

func newErrorDNSTransport(tag string, err error) *staticDNSTransport {
	return &staticDNSTransport{tag: tag, err: err}
}

func newDelayedDNSTransport(tag string, delay time.Duration, err error) *staticDNSTransport {
	return &staticDNSTransport{tag: tag, err: err, delay: delay}
}

func (t *staticDNSTransport) Type() string {
	return "stub"
}

func (t *staticDNSTransport) Tag() string {
	return t.tag
}

func (t *staticDNSTransport) Dependencies() []string {
	return nil
}

func (t *staticDNSTransport) Start(stage adapter.StartStage) error {
	return nil
}

func (t *staticDNSTransport) Close() error {
	return nil
}

func (t *staticDNSTransport) Exchange(ctx context.Context, message *mDNS.Msg) (*mDNS.Msg, error) {
	t.calls.Add(1)
	if t.delay > 0 {
		timer := time.NewTimer(t.delay)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			if t.err != nil {
				return nil, t.err
			}
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
	if t.err != nil {
		return nil, t.err
	}
	if t.response == nil {
		return nil, errors.New("empty response")
	}
	return t.response.Copy(), nil
}

type stubDNSTransportManager struct {
	transports map[string]adapter.DNSTransport
}

func (m *stubDNSTransportManager) Start(stage adapter.StartStage) error {
	return nil
}

func (m *stubDNSTransportManager) Close() error {
	return nil
}

func (m *stubDNSTransportManager) Transports() []adapter.DNSTransport {
	list := make([]adapter.DNSTransport, 0, len(m.transports))
	for _, transport := range m.transports {
		list = append(list, transport)
	}
	return list
}

func (m *stubDNSTransportManager) Transport(tag string) (adapter.DNSTransport, bool) {
	transport, ok := m.transports[tag]
	return transport, ok
}

func (m *stubDNSTransportManager) Default() adapter.DNSTransport {
	return nil
}

func (m *stubDNSTransportManager) FakeIP() adapter.FakeIPTransport {
	return nil
}

func (m *stubDNSTransportManager) Remove(tag string) error {
	delete(m.transports, tag)
	return nil
}

func (m *stubDNSTransportManager) Create(ctx context.Context, logger log.ContextLogger, tag string, outboundType string, options any) error {
	return errors.New("not implemented")
}

func successDNSResponse(answerName string) *mDNS.Msg {
	return successDNSResponseWithA(answerName, net.IPv4(1, 1, 1, 1), 60)
}

func successDNSResponseWithA(answerName string, ip net.IP, ttl uint32) *mDNS.Msg {
	return &mDNS.Msg{
		MsgHdr: mDNS.MsgHdr{
			Response: true,
			Rcode:    mDNS.RcodeSuccess,
		},
		Answer: []mDNS.RR{
			&mDNS.A{
				Hdr: mDNS.RR_Header{
					Name:   answerName,
					Rrtype: mDNS.TypeA,
					Class:  mDNS.ClassINET,
					Ttl:    ttl,
				},
				A: ip.To4(),
			},
		},
	}
}

func ipv4Answers(msg *mDNS.Msg) []string {
	if msg == nil {
		return nil
	}
	var out []string
	for _, rr := range msg.Answer {
		a, ok := rr.(*mDNS.A)
		if !ok || a.A == nil {
			continue
		}
		out = append(out, a.A.String())
	}
	return out
}

func containsAllStrings(got []string, want ...string) bool {
	have := make(map[string]struct{}, len(got))
	for _, value := range got {
		have[value] = struct{}{}
	}
	for _, value := range want {
		if _, ok := have[value]; !ok {
			return false
		}
	}
	return true
}

func rcodeDNSResponse(rcode int) *mDNS.Msg {
	return &mDNS.Msg{
		MsgHdr: mDNS.MsgHdr{
			Response: true,
			Rcode:    rcode,
		},
	}
}

func dnsQuestion(name string) *mDNS.Msg {
	return &mDNS.Msg{
		MsgHdr: mDNS.MsgHdr{
			RecursionDesired: true,
		},
		Question: []mDNS.Question{{
			Name:   name,
			Qtype:  mDNS.TypeA,
			Qclass: mDNS.ClassINET,
		}},
	}
}

func equalStrings(got []string, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// ---------------------------------------------------------------------------
// CAS loser close
// ---------------------------------------------------------------------------

// closableBuilder builds closable outbounds that track Close() calls.
type closableBuilder struct {
	mu    sync.Mutex
	built []*trackCloser
}

type trackCloser struct {
	closed atomic.Bool
}

func (c *trackCloser) Close() error {
	c.closed.Store(true)
	return nil
}

func (c *trackCloser) Type() string {
	return "track-closer"
}

func (c *trackCloser) Tag() string {
	return "track-closer"
}

func (c *trackCloser) Network() []string {
	return []string{"tcp", "udp"}
}

func (c *trackCloser) Dependencies() []string {
	return nil
}

func (c *trackCloser) DialContext(context.Context, string, M.Socksaddr) (net.Conn, error) {
	return nil, errors.New("track-closer: dial not supported")
}

func (c *trackCloser) ListenPacket(context.Context, M.Socksaddr) (net.PacketConn, error) {
	return nil, errors.New("track-closer: listen packet not supported")
}

func (b *closableBuilder) Build(_ json.RawMessage) (adapter.Outbound, error) {
	tc := &trackCloser{}
	b.mu.Lock()
	b.built = append(b.built, tc)
	b.mu.Unlock()
	return tc, nil
}

func TestEnsureNodeOutbound_CASLoserClose(t *testing.T) {
	entry := newTestEntry(`{"type":"test"}`)
	pool := &mockPool{}
	pool.addEntry(entry)

	cb := &closableBuilder{}
	mgr := NewOutboundManager(pool, cb)

	// Run many concurrent EnsureNodeOutbound calls. Only the CAS winner's
	// outbound survives; all losers must be closed.
	const N = 50
	var wg sync.WaitGroup
	wg.Add(N)
	for i := 0; i < N; i++ {
		go func() {
			defer wg.Done()
			mgr.EnsureNodeOutbound(entry.Hash)
		}()
	}
	wg.Wait()

	if entry.Outbound.Load() == nil {
		t.Fatal("expected outbound to be set")
	}

	cb.mu.Lock()
	total := len(cb.built)
	closedCount := 0
	for _, tc := range cb.built {
		if tc.closed.Load() {
			closedCount++
		}
	}
	cb.mu.Unlock()

	// With N concurrent goroutines, some pass the fast-path nil check before
	// the winner's CAS succeeds. Those losers must all be closed.
	if total > 1 && closedCount != total-1 {
		t.Errorf("expected %d closed outbounds, got %d (total built: %d)", total-1, closedCount, total)
	}
}

// ---------------------------------------------------------------------------
// Remove close
// ---------------------------------------------------------------------------

func TestRemoveNodeOutbound_Closes(t *testing.T) {
	tc := &trackCloser{}
	entry := newTestEntry(`{"type":"test"}`)
	var wrapped adapter.Outbound = tc
	entry.Outbound.Store(&wrapped)

	pool := &mockPool{}
	mgr := NewOutboundManager(pool, &testutil.StubOutboundBuilder{})

	mgr.RemoveNodeOutbound(entry)

	if !tc.closed.Load() {
		t.Fatal("expected outbound to be closed after RemoveNodeOutbound")
	}
	if entry.Outbound.Load() != nil {
		t.Fatal("expected outbound to be nil after RemoveNodeOutbound")
	}
}
