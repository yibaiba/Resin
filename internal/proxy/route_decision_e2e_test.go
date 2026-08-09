package proxy

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/Resinat/Resin/internal/routing"
	N "github.com/sagernet/sing/common/network"
)

type fixedRouteDecisioner struct {
	action routing.DecisionAction
	calls  atomic.Int32
}

func (d *fixedRouteDecisioner) Decide(input routing.DecisionInput) (routing.Decision, error) {
	d.calls.Add(1)
	return routing.Decision{Action: d.action, PlatformName: input.PlatformName, Reason: "test"}, nil
}

func TestForwardProxy_RoutingPolicyRejectsHTTPAndCONNECTBeforeRouting(t *testing.T) {
	for _, method := range []string{http.MethodGet, http.MethodConnect} {
		t.Run(method, func(t *testing.T) {
			decisioner := &fixedRouteDecisioner{action: routing.DecisionReject}
			proxy := NewForwardProxy(ForwardProxyConfig{
				ProxyToken: "tok", Decisioner: decisioner, Events: NoOpEventEmitter{},
			})
			request := httptest.NewRequest(method, "http://example.com:443/path", nil)
			request.Host = "example.com:443"
			request.Header.Set("Proxy-Authorization", basicAuth("plat", "tok"))
			response := httptest.NewRecorder()

			proxy.ServeHTTP(response, request)

			if response.Code != http.StatusForbidden || response.Header().Get("X-Resin-Error") != "ROUTE_REJECTED" {
				t.Fatalf("status=%d error=%q body=%q", response.Code, response.Header().Get("X-Resin-Error"), response.Body.String())
			}
			if decisioner.calls.Load() != 1 {
				t.Fatalf("decision calls=%d, want 1", decisioner.calls.Load())
			}
		})
	}
}

func TestReverseProxy_RoutingPolicyRejectsBeforeRouting(t *testing.T) {
	decisioner := &fixedRouteDecisioner{action: routing.DecisionReject}
	proxy := NewReverseProxy(ReverseProxyConfig{
		ProxyToken: "tok", Decisioner: decisioner, Events: NoOpEventEmitter{},
	})
	request := httptest.NewRequest(http.MethodGet, "/tok/plat:acct/https/example.com/path", nil)
	response := httptest.NewRecorder()

	proxy.ServeHTTP(response, request)

	if response.Code != http.StatusForbidden || response.Header().Get("X-Resin-Error") != "ROUTE_REJECTED" {
		t.Fatalf("status=%d error=%q body=%q", response.Code, response.Header().Get("X-Resin-Error"), response.Body.String())
	}
	if decisioner.calls.Load() != 1 {
		t.Fatalf("decision calls=%d, want 1", decisioner.calls.Load())
	}
}

func TestSocks5Connect_RoutingPolicyUsesConnectionDeniedReply(t *testing.T) {
	decisioner := &fixedRouteDecisioner{action: routing.DecisionReject}
	inbound := NewSocks5Inbound(Socks5InboundConfig{
		ProxyToken: "tok", Decisioner: decisioner, Events: NoOpEventEmitter{},
	})
	client, reader, done := startSocks5Session(t, inbound)
	defer client.Close()

	writeAll(t, client, []byte{socks5Version, 1, socks5MethodUserPass})
	_ = readExactly(t, reader, 2)
	writeAll(t, client, socks5UserPassPacket("plat.acct", "tok"))
	_ = readExactly(t, reader, 2)
	writeAll(t, client, socks5ConnectIPv4Packet("127.0.0.1:443"))
	reply := readExactly(t, reader, 10)

	if reply[1] != socks5ReplyConnectionDenied {
		t.Fatalf("reply=%v, want connection denied", reply)
	}
	_ = client.Close()
	<-done
	if decisioner.calls.Load() != 1 {
		t.Fatalf("decision calls=%d, want 1", decisioner.calls.Load())
	}
}

func TestSocks5UDPFlow_RoutingPolicyRejectsBeforeOpeningPacketFlow(t *testing.T) {
	decisioner := &fixedRouteDecisioner{action: routing.DecisionReject}
	inbound := NewSocks5Inbound(Socks5InboundConfig{Decisioner: decisioner})
	result := inbound.newUDPRelayFlow(socks5UDPFlowRequest{
		ctx: context.Background(),
		association: &socks5UDPAssociation{
			platformName: "plat", account: "acct",
		},
		target: "127.0.0.1:53",
	})

	if result.flow != nil || result.proxyErr != ErrRouteRejected || result.stage != "udp_route" {
		t.Fatalf("unexpected result: flow=%v err=%v stage=%q", result.flow, result.proxyErr, result.stage)
	}
	if decisioner.calls.Load() != 1 {
		t.Fatalf("decision calls=%d, want 1", decisioner.calls.Load())
	}
}

func TestResolveOutboundPlan_PassesRequiredNetworkToDecisioner(t *testing.T) {
	decisioner := &capturingDecisioner{action: routing.DecisionDirect}
	plan, proxyErr := resolveOutboundPlan(decisioner, nil, nil, routing.RouteOptions{
		PlatformName: "plat", Account: "acct", Target: "example.com:53", RequiredNetwork: N.NetworkUDP,
	})
	if proxyErr != nil || !plan.IsDirect() {
		t.Fatalf("plan=%+v err=%v", plan, proxyErr)
	}
	if decisioner.input.RequiredNetwork != N.NetworkUDP {
		t.Fatalf("network=%q, want udp", decisioner.input.RequiredNetwork)
	}
}

type capturingDecisioner struct {
	action routing.DecisionAction
	input  routing.DecisionInput
}

func (d *capturingDecisioner) Decide(input routing.DecisionInput) (routing.Decision, error) {
	d.input = input
	return routing.Decision{Action: d.action, Reason: fmt.Sprintf("captured-%s", input.RequiredNetwork)}, nil
}
