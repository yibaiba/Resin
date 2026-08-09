package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/netip"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Resinat/Resin/internal/node"
	"github.com/Resinat/Resin/internal/outbound"
	"github.com/sagernet/sing-box/adapter"
	M "github.com/sagernet/sing/common/metadata"
)

type recordingUDPDirectDialer struct {
	calls atomic.Int32
}

func (d *recordingUDPDirectDialer) ListenPacket(
	ctx context.Context,
	target string,
) (net.PacketConn, net.Addr, error) {
	d.calls.Add(1)
	return netUDPDirectPacketDialer{}.ListenPacket(ctx, target)
}

type failingUDPDirectDialer struct {
	calls atomic.Int32
	err   error
}

type udpListenFailureOutbound struct {
	adapter.Outbound
	err error
}

func (o *udpListenFailureOutbound) Network() []string {
	return []string{"tcp", "udp"}
}

func (o *udpListenFailureOutbound) ListenPacket(
	context.Context,
	M.Socksaddr,
) (net.PacketConn, error) {
	return nil, o.err
}

func (d *failingUDPDirectDialer) ListenPacket(
	context.Context,
	string,
) (net.PacketConn, net.Addr, error) {
	d.calls.Add(1)
	return nil, nil, d.err
}

type socks5UDPRelayTestEnv struct {
	proxyConn  *net.UDPConn
	clientConn *net.UDPConn
	echoConn   *net.UDPConn
	serveDone  chan error
}

func newSocks5UDPRelayTestEnv(
	t *testing.T,
	inbound *Socks5Inbound,
	scope string,
) *socks5UDPRelayTestEnv {
	t.Helper()
	proxyConn := listenLocalUDP(t)
	clientConn := listenLocalUDP(t)
	echoConn := listenLocalUDP(t)
	env := &socks5UDPRelayTestEnv{
		proxyConn: proxyConn, clientConn: clientConn, echoConn: echoConn,
		serveDone: make(chan error, 1),
	}
	go serveUDPEcho(echoConn)
	go func() {
		env.serveDone <- inbound.ServePacketConnContext(context.Background(), scope, proxyConn)
	}()
	t.Cleanup(func() {
		_ = clientConn.Close()
		_ = echoConn.Close()
		_ = proxyConn.Close()
		select {
		case err := <-env.serveDone:
			if err != nil && !errors.Is(err, net.ErrClosed) {
				t.Errorf("UDP relay shutdown: %v", err)
			}
		case <-time.After(time.Second):
			t.Error("UDP relay did not stop")
		}
	})
	return env
}

func listenLocalUDP(t *testing.T) *net.UDPConn {
	t.Helper()
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatalf("listen UDP: %v", err)
	}
	return conn
}

func serveUDPEcho(conn *net.UDPConn) {
	buffer := make([]byte, socks5UDPMaxDatagramSize)
	for {
		n, source, err := conn.ReadFromUDP(buffer)
		if err != nil {
			return
		}
		_, _ = conn.WriteToUDP(buffer[:n], source)
	}
}

func registerRelayTestAssociation(
	t *testing.T,
	inbound *Socks5Inbound,
	scope string,
	clientConn *net.UDPConn,
	platformName string,
	emitter EventEmitter,
) *socks5UDPAssociation {
	t.Helper()
	if err := inbound.OpenUDPAssociationScope(scope); err != nil {
		t.Fatalf("open UDP association scope: %v", err)
	}
	source := normalizeAddrPort(M.AddrPortFromNet(clientConn.LocalAddr()))
	var lifecycle *requestLifecycle
	if emitter != nil {
		lifecycle = newRequestLifecycleFromMetadata(
			emitter,
			clientConn.LocalAddr().String(),
			"UDP_ASSOCIATE",
			ProxyTypeSocks5Forward,
			true,
		)
		lifecycle.setAccount("account-a")
	}
	association, err := inbound.udpAssociations.register(socks5UDPAssociationOptions{
		endpointID:      scope,
		clientIP:        source.Addr(),
		requestedSource: netip.AddrPortFrom(netip.IPv4Unspecified(), source.Port()),
		platformName:    platformName,
		account:         "account-a",
		lifecycle:       lifecycle,
	})
	if err != nil {
		t.Fatalf("register UDP association: %v", err)
	}
	return association
}

func sendSocks5UDPAndReadResponse(
	t *testing.T,
	env *socks5UDPRelayTestEnv,
	payload []byte,
) socks5UDPDatagram {
	t.Helper()
	return sendSocks5UDPToTarget(t, env, env.echoConn.LocalAddr().String(), payload)
}

func sendSocks5UDPToTarget(
	t *testing.T,
	env *socks5UDPRelayTestEnv,
	target string,
	payload []byte,
) socks5UDPDatagram {
	t.Helper()
	packet, err := encodeSocks5UDPDatagram(target, payload)
	if err != nil {
		t.Fatalf("encode UDP datagram: %v", err)
	}
	if _, err := env.clientConn.WriteToUDP(packet, env.proxyConn.LocalAddr().(*net.UDPAddr)); err != nil {
		t.Fatalf("send UDP datagram: %v", err)
	}
	if err := env.clientConn.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatalf("set UDP read deadline: %v", err)
	}
	response := make([]byte, socks5UDPMaxDatagramSize)
	n, _, err := env.clientConn.ReadFromUDP(response)
	if err != nil {
		t.Fatalf("read UDP response: %v", err)
	}
	decoded, err := decodeSocks5UDPDatagram(response[:n])
	if err != nil {
		t.Fatalf("decode UDP response: %v", err)
	}
	return decoded
}

func TestSocks5UDPRelayBypassUsesInjectedDirectDialer(t *testing.T) {
	dialer := &recordingUDPDirectDialer{}
	metrics := newCountingConnTestSink()
	emitter := newMockEventEmitter()
	inbound := NewSocks5Inbound(Socks5InboundConfig{
		Events:           emitter,
		MetricsSink:      metrics,
		ProxyBypassRules: []string{"127.0.0.0/8"},
		UDPDirectDialer:  dialer,
	})
	t.Cleanup(func() { _ = inbound.CloseUDPAssociations() })
	env := newSocks5UDPRelayTestEnv(t, inbound, "bypass-scope")
	association := registerRelayTestAssociation(
		t, inbound, "bypass-scope", env.clientConn, "", emitter,
	)

	payload := []byte("direct UDP echo")
	response := sendSocks5UDPAndReadResponse(t, env, payload)
	if string(response.payload) != string(payload) {
		t.Fatalf("UDP response payload: got %q, want %q", response.payload, payload)
	}
	if dialer.calls.Load() != 1 {
		t.Fatalf("direct packet dialer calls: got %d, want 1", dialer.calls.Load())
	}
	assertSocks5UDPTraffic(t, metrics.traffic, int64(len(payload)))
	if err := association.Close(); err != nil {
		t.Fatalf("close association: %v", err)
	}
	select {
	case entry := <-emitter.logCh:
		if entry.ProxyType != ProxyTypeSocks5Forward || entry.HTTPMethod != "UDP_ASSOCIATE" {
			t.Fatalf("UDP request log identity: %+v", entry)
		}
		if entry.IngressBytes != int64(len(payload)) || entry.EgressBytes != int64(len(payload)) {
			t.Fatalf("UDP request log traffic: ingress=%d egress=%d", entry.IngressBytes, entry.EgressBytes)
		}
	case <-time.After(time.Second):
		t.Fatal("UDP association did not emit request log")
	}
}

func TestSocks5UDPRelayUsesSingBoxPacketOutbound(t *testing.T) {
	routeEnv := newProxyE2EEnv(t)
	metrics := newCountingConnTestSink()
	inbound := NewSocks5Inbound(Socks5InboundConfig{
		Router: routeEnv.router, Pool: routeEnv.pool, Health: routeEnv.pool, MetricsSink: metrics,
	})
	t.Cleanup(func() { _ = inbound.CloseUDPAssociations() })
	env := newSocks5UDPRelayTestEnv(t, inbound, "routed-scope")
	association := registerRelayTestAssociation(
		t, inbound, "routed-scope", env.clientConn, "plat", nil,
	)

	payload := []byte("sing-box UDP echo")
	response := sendSocks5UDPAndReadResponse(t, env, payload)
	if string(response.payload) != string(payload) {
		t.Fatalf("UDP response payload: got %q, want %q", response.payload, payload)
	}
	if _, ok := association.flow(env.echoConn.LocalAddr().String()); !ok {
		t.Fatal("expected reusable UDP flow for routed target")
	}
	assertSocks5UDPTraffic(t, metrics.traffic, int64(len(payload)))
}

func TestSocks5UDPRelayUsesRealSingBoxDirectOutbound(t *testing.T) {
	routeEnv := newProxyE2EEnv(t)
	builder, err := outbound.NewSingboxBuilderWithConfig(outbound.SingboxBuilderConfig{
		DNSUpstreams: []string{"local"},
	})
	if err != nil {
		t.Fatalf("create sing-box builder: %v", err)
	}
	t.Cleanup(func() { _ = builder.Close() })
	realOutbound, err := builder.Build(json.RawMessage(`{"type":"direct","tag":"udp-e2e"}`))
	if err != nil {
		t.Fatalf("build sing-box direct outbound: %v", err)
	}
	t.Cleanup(func() {
		if closer, ok := realOutbound.(interface{ Close() error }); ok {
			_ = closer.Close()
		}
	})
	replaceProxyE2EOutbound(t, routeEnv, realOutbound)

	inbound := NewSocks5Inbound(Socks5InboundConfig{
		Router: routeEnv.router, Pool: routeEnv.pool, Health: routeEnv.pool,
	})
	t.Cleanup(func() { _ = inbound.CloseUDPAssociations() })
	env := newSocks5UDPRelayTestEnv(t, inbound, "real-sing-box-scope")
	registerRelayTestAssociation(
		t, inbound, "real-sing-box-scope", env.clientConn, "plat", nil,
	)

	payload := []byte("real sing-box direct UDP echo")
	target := net.JoinHostPort("localhost", strconv.Itoa(env.echoConn.LocalAddr().(*net.UDPAddr).Port))
	response := sendSocks5UDPToTarget(t, env, target, payload)
	if string(response.payload) != string(payload) {
		t.Fatalf("UDP response payload: got %q, want %q", response.payload, payload)
	}
}

func replaceProxyE2EOutbound(
	t *testing.T,
	env *proxyE2EEnv,
	replacement adapter.Outbound,
) {
	t.Helper()
	raw := json.RawMessage(`{"type":"stub","server":"127.0.0.1","server_port":1}`)
	hash := node.HashFromRawOptions(raw)
	entry, ok := env.pool.GetEntry(hash)
	if !ok {
		t.Fatal("proxy E2E node not found")
	}
	entry.Outbound.Store(&replacement)
	entry.LastUDPProbeSuccess.Store(time.Now().UnixNano())
	env.pool.NotifyNodeDirty(hash)
	platform, ok := env.pool.GetPlatformByName("plat")
	if !ok || !platform.NetworkView("udp").Contains(hash) {
		t.Fatal("real sing-box outbound is not in the UDP routable view")
	}
}

func TestSocks5UDPRelayDoesNotRetryFailedFlowCreation(t *testing.T) {
	dialErr := errors.New("direct UDP socket unavailable")
	dialer := &failingUDPDirectDialer{err: dialErr}
	emitter := newMockEventEmitter()
	inbound := NewSocks5Inbound(Socks5InboundConfig{
		Events:           emitter,
		ProxyBypassRules: []string{"127.0.0.0/8"},
		UDPDirectDialer:  dialer,
	})
	t.Cleanup(func() { _ = inbound.CloseUDPAssociations() })
	clientConn := listenLocalUDP(t)
	defer clientConn.Close()
	association := registerRelayTestAssociation(
		t, inbound, "failure-scope", clientConn, "", emitter,
	)
	client := &socks5UDPClientWriter{packetConn: clientConn}
	packet, err := encodeSocks5UDPDatagram("127.0.0.1:53", []byte("request"))
	if err != nil {
		t.Fatalf("encode UDP datagram: %v", err)
	}

	for attempt := 1; attempt <= 2; attempt++ {
		inbound.handleUDPDatagram(socks5UDPInboundPacket{
			ctx:              context.Background(),
			associationScope: "failure-scope",
			client:           client,
			source:           clientConn.LocalAddr(),
			packet:           packet,
		})
		if got := dialer.calls.Load(); got != int32(attempt) {
			t.Fatalf("dial calls after packet %d: got %d, want %d", attempt, got, attempt)
		}
	}
	if err := association.Close(); err != nil {
		t.Fatalf("close association: %v", err)
	}
	select {
	case entry := <-emitter.logCh:
		if entry.UpstreamStage != "udp_direct_listen" {
			t.Fatalf("upstream stage: got %q, want udp_direct_listen", entry.UpstreamStage)
		}
		if entry.NetOK || entry.ResinError != ErrUpstreamRequestFailed.ResinError {
			t.Fatalf("failure log: NetOK=%v ResinError=%q", entry.NetOK, entry.ResinError)
		}
	case <-time.After(time.Second):
		t.Fatal("failed UDP association did not emit request log")
	}
}

func TestSocks5UDPRelayRejectsExpiredAssociation(t *testing.T) {
	const idleTimeout = 80 * time.Millisecond
	inbound := NewSocks5Inbound(Socks5InboundConfig{
		ProxyBypassRules: []string{"127.0.0.0/8"},
		UDPIdleTimeout:   idleTimeout,
	})
	t.Cleanup(func() { _ = inbound.CloseUDPAssociations() })
	env := newSocks5UDPRelayTestEnv(t, inbound, "expired-scope")
	association := registerRelayTestAssociation(
		t, inbound, "expired-scope", env.clientConn, "", nil,
	)
	payload := []byte("before expiry")
	_ = sendSocks5UDPAndReadResponse(t, env, payload)

	deadline := time.Now().Add(time.Second)
	for !association.closed.Load() && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if !association.closed.Load() {
		t.Fatal("UDP association did not expire")
	}
	packet, err := encodeSocks5UDPDatagram(env.echoConn.LocalAddr().String(), []byte("after expiry"))
	if err != nil {
		t.Fatalf("encode UDP datagram: %v", err)
	}
	if _, err := env.clientConn.WriteToUDP(packet, env.proxyConn.LocalAddr().(*net.UDPAddr)); err != nil {
		t.Fatalf("send expired UDP datagram: %v", err)
	}
	_ = env.clientConn.SetReadDeadline(time.Now().Add(2 * idleTimeout))
	buffer := make([]byte, socks5UDPMaxDatagramSize)
	if _, _, err := env.clientConn.ReadFromUDP(buffer); err == nil {
		t.Fatal("expired UDP association unexpectedly relayed a response")
	} else if netErr, ok := err.(net.Error); !ok || !netErr.Timeout() {
		t.Fatalf("read after association expiry: %v", err)
	}
}

func TestSocks5UDPRelayOnlyPenalizesExplicitOutboundFailures(t *testing.T) {
	for _, testCase := range []struct {
		name         string
		listenErr    error
		wantFailures int32
	}{
		{name: "transport error", listenErr: errors.New("UDP transport failed"), wantFailures: 1},
		{name: "context canceled", listenErr: context.Canceled, wantFailures: 0},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			routeEnv := newProxyE2EEnv(t)
			replaceProxyE2EOutbound(t, routeEnv, &udpListenFailureOutbound{err: testCase.listenErr})
			health := &mockPassiveHealthRecorder{done: make(chan struct{}, 1)}
			inbound := NewSocks5Inbound(Socks5InboundConfig{
				Router: routeEnv.router, Pool: routeEnv.pool, Health: health,
			})
			t.Cleanup(func() { _ = inbound.CloseUDPAssociations() })
			clientConn := listenLocalUDP(t)
			defer clientConn.Close()
			registerRelayTestAssociation(
				t, inbound, "health-scope", clientConn, "plat", nil,
			)
			packet, err := encodeSocks5UDPDatagram("127.0.0.1:53", []byte("health"))
			if err != nil {
				t.Fatalf("encode UDP datagram: %v", err)
			}
			inbound.handleUDPDatagram(socks5UDPInboundPacket{
				ctx: context.Background(), associationScope: "health-scope",
				client: &socks5UDPClientWriter{packetConn: clientConn},
				source: clientConn.LocalAddr(), packet: packet,
			})
			if testCase.wantFailures > 0 {
				select {
				case <-health.done:
				case <-time.After(time.Second):
					t.Fatal("explicit UDP transport failure was not recorded")
				}
			} else {
				select {
				case <-health.done:
					t.Fatal("context cancellation was recorded as a node failure")
				case <-time.After(100 * time.Millisecond):
				}
			}
			if got := health.passiveCalls.Load(); got != testCase.wantFailures {
				t.Fatalf("passive failure calls: got %d, want %d", got, testCase.wantFailures)
			}
		})
	}
}

func TestSocks5UDPRelayNoResponseDoesNotPenalizeNode(t *testing.T) {
	routeEnv := newProxyE2EEnv(t)
	health := &mockPassiveHealthRecorder{done: make(chan struct{}, 1)}
	inbound := NewSocks5Inbound(Socks5InboundConfig{
		Router: routeEnv.router, Pool: routeEnv.pool, Health: health,
	})
	t.Cleanup(func() { _ = inbound.CloseUDPAssociations() })
	clientConn := listenLocalUDP(t)
	defer clientConn.Close()
	association := registerRelayTestAssociation(
		t, inbound, "silent-scope", clientConn, "plat", nil,
	)
	silentTarget := listenLocalUDP(t)
	target := silentTarget.LocalAddr().String()
	packet, err := encodeSocks5UDPDatagram(target, []byte("no response expected"))
	if err != nil {
		t.Fatalf("encode UDP datagram: %v", err)
	}
	inbound.handleUDPDatagram(socks5UDPInboundPacket{
		ctx: context.Background(), associationScope: "silent-scope",
		client: &socks5UDPClientWriter{packetConn: clientConn},
		source: clientConn.LocalAddr(), packet: packet,
	})
	_ = silentTarget.SetReadDeadline(time.Now().Add(time.Second))
	received := make([]byte, socks5UDPMaxDatagramSize)
	n, _, err := silentTarget.ReadFromUDP(received)
	if err != nil {
		t.Fatalf("silent UDP target did not receive request: %v", err)
	}
	if got := string(received[:n]); got != "no response expected" {
		t.Fatalf("silent UDP target payload: got %q", got)
	}
	select {
	case <-health.done:
		t.Fatal("missing UDP response was recorded as a node result")
	case <-time.After(100 * time.Millisecond):
	}
	if got := health.passiveCalls.Load(); got != 0 {
		t.Fatalf("passive result calls without a UDP response: got %d", got)
	}
	if err := association.Close(); err != nil {
		t.Fatalf("close silent association: %v", err)
	}
}

func assertSocks5UDPTraffic(t *testing.T, traffic <-chan trafficDelta, payloadBytes int64) {
	t.Helper()
	var ingress, egress int64
	for ingress < payloadBytes || egress < payloadBytes {
		select {
		case delta := <-traffic:
			ingress += delta.ingress
			egress += delta.egress
		case <-time.After(time.Second):
			t.Fatalf("UDP traffic metrics: ingress=%d egress=%d, want %d each", ingress, egress, payloadBytes)
		}
	}
}
