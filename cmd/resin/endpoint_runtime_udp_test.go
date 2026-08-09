package main

import (
	"context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/Resinat/Resin/internal/model"
	"github.com/Resinat/Resin/internal/proxy"
)

type endpointPacketStart struct {
	scope string
	addr  string
}

type endpointRuntimeSocksHandler struct {
	packetStarts chan endpointPacketStart
	openedScopes chan string
	closedScopes chan string
	policies     chan proxy.InboundPolicy

	mu           sync.Mutex
	activeScopes map[string]struct{}
	openErr      error
}

type scopeGatedEndpointHandler struct {
	started     chan struct{}
	release     chan struct{}
	scopeClose  chan struct{}
	releaseOnce sync.Once
	closeOnce   sync.Once
}

func newScopeGatedEndpointHandler() *scopeGatedEndpointHandler {
	return &scopeGatedEndpointHandler{
		started: make(chan struct{}), release: make(chan struct{}), scopeClose: make(chan struct{}),
	}
}

func (h *scopeGatedEndpointHandler) ServeConnContext(context.Context, net.Conn) {}

func (h *scopeGatedEndpointHandler) ServePacketConnContext(
	context.Context,
	string,
	net.PacketConn,
) error {
	close(h.started)
	<-h.release
	return nil
}

func (h *scopeGatedEndpointHandler) CloseUDPAssociationScope(string) error {
	h.closeOnce.Do(func() { close(h.scopeClose) })
	h.releaseOnce.Do(func() { close(h.release) })
	return nil
}

func (h *scopeGatedEndpointHandler) OpenUDPAssociationScope(string) error { return nil }

func (h *scopeGatedEndpointHandler) forceRelease() {
	h.releaseOnce.Do(func() { close(h.release) })
}

type stubbornEndpointHandler struct {
	started chan struct{}
	release chan struct{}
}

func (h *stubbornEndpointHandler) ServeConnContext(context.Context, net.Conn) {}

func (h *stubbornEndpointHandler) ServePacketConnContext(
	context.Context,
	string,
	net.PacketConn,
) error {
	close(h.started)
	<-h.release
	return nil
}

func (h *stubbornEndpointHandler) CloseUDPAssociationScope(string) error { return nil }
func (h *stubbornEndpointHandler) OpenUDPAssociationScope(string) error  { return nil }

func newEndpointRuntimeSocksHandler() *endpointRuntimeSocksHandler {
	return &endpointRuntimeSocksHandler{
		packetStarts: make(chan endpointPacketStart, 8),
		openedScopes: make(chan string, 8),
		closedScopes: make(chan string, 8),
		policies:     make(chan proxy.InboundPolicy, 8),
		activeScopes: make(map[string]struct{}),
	}
}

func (h *endpointRuntimeSocksHandler) OpenUDPAssociationScope(scope string) error {
	h.openedScopes <- scope
	return h.openErr
}

func (h *endpointRuntimeSocksHandler) ServeConnContext(ctx context.Context, conn net.Conn) {
	h.policies <- proxy.InboundPolicyFromContext(ctx)
	_ = conn.Close()
}

func (h *endpointRuntimeSocksHandler) ServePacketConnContext(
	ctx context.Context,
	scope string,
	packetConn net.PacketConn,
) error {
	h.mu.Lock()
	h.activeScopes[scope] = struct{}{}
	h.mu.Unlock()
	h.packetStarts <- endpointPacketStart{scope: scope, addr: packetConn.LocalAddr().String()}
	<-ctx.Done()
	h.mu.Lock()
	delete(h.activeScopes, scope)
	h.mu.Unlock()
	return ctx.Err()
}

func (h *endpointRuntimeSocksHandler) CloseUDPAssociationScope(scope string) error {
	h.closedScopes <- scope
	return nil
}

func reserveDualTransportPort(t *testing.T) int {
	t.Helper()
	tcpListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve TCP port: %v", err)
	}
	port := tcpListener.Addr().(*net.TCPAddr).Port
	udpConn, err := net.ListenPacket("udp", formatListenAddress("127.0.0.1", port))
	if err != nil {
		_ = tcpListener.Close()
		t.Fatalf("reserve matching UDP port: %v", err)
	}
	_ = udpConn.Close()
	_ = tcpListener.Close()
	return port
}

func assertTransportPortBound(t *testing.T, network string, port int) {
	t.Helper()
	address := formatListenAddress("127.0.0.1", port)
	var closer interface{ Close() error }
	var err error
	if network == "tcp" {
		closer, err = net.Listen(network, address)
	} else {
		closer, err = net.ListenPacket(network, address)
	}
	if err == nil {
		_ = closer.Close()
		t.Fatalf("%s port %d is not bound", network, port)
	}
}

func assertDualTransportPortReleased(t *testing.T, port int) {
	t.Helper()
	address := formatListenAddress("127.0.0.1", port)
	tcpListener, err := net.Listen("tcp", address)
	if err != nil {
		t.Fatalf("rebind released TCP port: %v", err)
	}
	defer tcpListener.Close()
	udpConn, err := net.ListenPacket("udp", address)
	if err != nil {
		t.Fatalf("rebind released UDP port: %v", err)
	}
	_ = udpConn.Close()
}

func TestEndpointRuntimeBindsAndShutsDownTCPAndUDP(t *testing.T) {
	port := reserveDualTransportPort(t)
	handler := newEndpointRuntimeSocksHandler()
	manager := newEndpointRuntimeManager(
		"127.0.0.1", "", nil, nil, nil, nil, handler, nil,
	)
	endpoint := model.Endpoint{
		ID: "dual", Port: port, Enabled: true, AllowProxy: true, AllowSOCKS5: true,
	}
	if err := manager.ApplyEndpoint(endpoint); err != nil {
		t.Fatalf("ApplyEndpoint: %v", err)
	}
	assertTransportPortBound(t, "tcp", port)
	assertTransportPortBound(t, "udp", port)
	manager.Start()
	select {
	case started := <-handler.packetStarts:
		if started.scope == "" || started.addr != formatListenAddress("127.0.0.1", port) {
			t.Fatalf("UDP runtime start: %+v", started)
		}
	case <-time.After(time.Second):
		t.Fatal("UDP runtime did not start")
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := manager.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	assertDualTransportPortReleased(t, port)
}

func TestEndpointRuntimeClosesAssociationScopeBeforeWaitingForRelay(t *testing.T) {
	port := reserveDualTransportPort(t)
	handler := newScopeGatedEndpointHandler()
	manager := newEndpointRuntimeManager(
		"127.0.0.1", "", nil, nil, nil, nil, handler, nil,
	)
	endpoint := model.Endpoint{
		ID: "scope-before-wait", Port: port,
		Enabled: true, AllowProxy: true, AllowSOCKS5: true,
	}
	if err := manager.ApplyEndpoint(endpoint); err != nil {
		t.Fatalf("ApplyEndpoint: %v", err)
	}
	manager.Start()
	select {
	case <-handler.started:
	case <-time.After(time.Second):
		t.Fatal("UDP runtime did not start")
	}
	t.Cleanup(handler.forceRelease)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := manager.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	select {
	case <-handler.scopeClose:
	default:
		t.Fatal("Shutdown waited for relay before closing association scope")
	}
	assertDualTransportPortReleased(t, port)
}

func TestEndpointRuntimeUDPShutdownHonorsContext(t *testing.T) {
	port := reserveDualTransportPort(t)
	handler := &stubbornEndpointHandler{
		started: make(chan struct{}), release: make(chan struct{}),
	}
	manager := newEndpointRuntimeManager(
		"127.0.0.1", "", nil, nil, nil, nil, handler, nil,
	)
	endpoint := model.Endpoint{
		ID: "shutdown-context", Port: port,
		Enabled: true, AllowProxy: true, AllowSOCKS5: true,
	}
	if err := manager.ApplyEndpoint(endpoint); err != nil {
		t.Fatalf("ApplyEndpoint: %v", err)
	}
	manager.Start()
	select {
	case <-handler.started:
	case <-time.After(time.Second):
		t.Fatal("UDP runtime did not start")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	err := manager.Shutdown(ctx)
	close(handler.release)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Shutdown error: got %v, want context deadline", err)
	}
	assertDualTransportPortReleased(t, port)
}

func TestEndpointRuntimePortChangeKeepsOldRuntimeWhenUDPBindFails(t *testing.T) {
	oldPort := reserveDualTransportPort(t)
	newPort := reserveDualTransportPort(t)
	handler := newEndpointRuntimeSocksHandler()
	manager := newEndpointRuntimeManager(
		"127.0.0.1", "", nil, nil, nil, nil, handler, nil,
	)
	oldEndpoint := model.Endpoint{
		ID: "atomic", Port: oldPort, Enabled: true, AllowProxy: true, AllowSOCKS5: true,
	}
	if err := manager.ApplyEndpoint(oldEndpoint); err != nil {
		t.Fatalf("apply old endpoint: %v", err)
	}
	udpBlocker, err := net.ListenPacket("udp", formatListenAddress("127.0.0.1", newPort))
	if err != nil {
		t.Fatalf("occupy new UDP port: %v", err)
	}
	defer udpBlocker.Close()

	nextEndpoint := oldEndpoint
	nextEndpoint.Port = newPort
	if err := manager.ApplyEndpoint(nextEndpoint); err == nil {
		t.Fatal("port change unexpectedly succeeded while UDP port was occupied")
	}
	current := manager.runtimes[oldEndpoint.ID]
	if current == nil || current.current().Port != oldPort {
		t.Fatalf("old runtime was not preserved: runtime=%p config=%+v", current, current.current())
	}
	assertTransportPortBound(t, "tcp", oldPort)
	assertTransportPortBound(t, "udp", oldPort)
	newTCP, err := net.Listen("tcp", formatListenAddress("127.0.0.1", newPort))
	if err != nil {
		t.Fatalf("failed replacement leaked TCP listener: %v", err)
	}
	_ = newTCP.Close()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := manager.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
}

func TestEndpointRuntimePortChangeStartsNewPairBeforeStoppingOldPair(t *testing.T) {
	oldPort := reserveDualTransportPort(t)
	newPort := reserveDualTransportPort(t)
	handler := newEndpointRuntimeSocksHandler()
	manager := newEndpointRuntimeManager(
		"127.0.0.1", "", nil, nil, nil, nil, handler, nil,
	)
	endpoint := model.Endpoint{
		ID: "replace", Port: oldPort, Enabled: true, AllowProxy: true, AllowSOCKS5: true,
	}
	if err := manager.ApplyEndpoint(endpoint); err != nil {
		t.Fatalf("apply old endpoint: %v", err)
	}
	manager.Start()
	oldStart := waitEndpointPacketStart(t, handler.packetStarts)

	endpoint.Port = newPort
	if err := manager.ApplyEndpoint(endpoint); err != nil {
		t.Fatalf("apply replacement endpoint: %v", err)
	}
	newStart := waitEndpointPacketStart(t, handler.packetStarts)
	if oldStart.scope == newStart.scope {
		t.Fatalf("runtime replacement reused association scope %q", oldStart.scope)
	}
	assertTransportPortBound(t, "tcp", newPort)
	assertTransportPortBound(t, "udp", newPort)
	waitDualTransportPortReleased(t, oldPort)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := manager.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
}

func waitEndpointPacketStart(t *testing.T, starts <-chan endpointPacketStart) endpointPacketStart {
	t.Helper()
	select {
	case started := <-starts:
		return started
	case <-time.After(time.Second):
		t.Fatal("UDP runtime did not start")
		return endpointPacketStart{}
	}
}

func waitDualTransportPortReleased(t *testing.T, port int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		address := formatListenAddress("127.0.0.1", port)
		tcpListener, tcpErr := net.Listen("tcp", address)
		udpConn, udpErr := net.ListenPacket("udp", address)
		if tcpErr == nil && udpErr == nil {
			_ = tcpListener.Close()
			_ = udpConn.Close()
			return
		}
		if tcpListener != nil {
			_ = tcpListener.Close()
		}
		if udpConn != nil {
			_ = udpConn.Close()
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("TCP/UDP port %d was not released after runtime replacement", port)
}

func TestEndpointRuntimeSamePortSOCKS5ToggleIsAtomic(t *testing.T) {
	port := reserveDualTransportPort(t)
	handler := newEndpointRuntimeSocksHandler()
	manager := newEndpointRuntimeManager(
		"127.0.0.1", "", nil, nil, nil, nil, handler, nil,
	)
	endpoint := model.Endpoint{ID: "toggle", Port: port, Enabled: true, AllowProxy: true}
	if err := manager.ApplyEndpoint(endpoint); err != nil {
		t.Fatalf("apply TCP-only endpoint: %v", err)
	}
	udpBlocker, err := net.ListenPacket("udp", formatListenAddress("127.0.0.1", port))
	if err != nil {
		t.Fatalf("occupy UDP port: %v", err)
	}
	enabled := endpoint
	enabled.AllowSOCKS5 = true
	if err := manager.ApplyEndpoint(enabled); err == nil {
		t.Fatal("SOCKS5 enable unexpectedly succeeded while UDP port was occupied")
	}
	if manager.runtimes[endpoint.ID].current().AllowSOCKS5 {
		t.Fatal("failed SOCKS5 enable changed active config")
	}
	_ = udpBlocker.Close()

	if err := manager.ApplyEndpoint(enabled); err != nil {
		t.Fatalf("enable SOCKS5: %v", err)
	}
	assertTransportPortBound(t, "udp", port)
	if err := manager.ApplyEndpoint(endpoint); err != nil {
		t.Fatalf("disable SOCKS5: %v", err)
	}
	udpProbe, err := net.ListenPacket("udp", formatListenAddress("127.0.0.1", port))
	if err != nil {
		t.Fatalf("SOCKS5 disable did not release UDP port: %v", err)
	}
	_ = udpProbe.Close()
	select {
	case scope := <-handler.closedScopes:
		if scope == "" {
			t.Fatal("disabled runtime closed an empty association scope")
		}
	case <-time.After(time.Second):
		t.Fatal("SOCKS5 disable did not close its association scope")
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := manager.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
}

func TestEndpointRuntimeRemoveStopsUDPAndReleasesBothPorts(t *testing.T) {
	port := reserveDualTransportPort(t)
	handler := newEndpointRuntimeSocksHandler()
	manager := newEndpointRuntimeManager(
		"127.0.0.1", "", nil, nil, nil, nil, handler, nil,
	)
	endpoint := model.Endpoint{
		ID: "remove-dual", Port: port, Enabled: true, AllowProxy: true, AllowSOCKS5: true,
	}
	if err := manager.ApplyEndpoint(endpoint); err != nil {
		t.Fatalf("ApplyEndpoint: %v", err)
	}
	manager.Start()
	select {
	case <-handler.packetStarts:
	case <-time.After(time.Second):
		t.Fatal("UDP runtime did not start")
	}
	manager.RemoveEndpoint(endpoint.ID)
	assertDualTransportPortReleased(t, port)
	select {
	case <-handler.closedScopes:
	case <-time.After(time.Second):
		t.Fatal("endpoint removal did not close UDP association scope")
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := manager.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
}

func TestEndpointRuntimeSOCKS5RequiresPacketHandlerWithoutLeakingTCP(t *testing.T) {
	port := reserveDualTransportPort(t)
	manager := newEndpointRuntimeManager("127.0.0.1", "", nil, nil, nil, nil, nil, nil)
	endpoint := model.Endpoint{
		ID: "missing-handler", Port: port, Enabled: true, AllowProxy: true, AllowSOCKS5: true,
	}
	if err := manager.ApplyEndpoint(endpoint); err == nil {
		t.Fatal("SOCKS5 endpoint unexpectedly started without UDP packet handler")
	}
	assertDualTransportPortReleased(t, port)
}

func TestEndpointRuntimeScopeOpenFailureReleasesTCPAndUDP(t *testing.T) {
	port := reserveDualTransportPort(t)
	handler := newEndpointRuntimeSocksHandler()
	handler.openErr = errors.New("association scope unavailable")
	manager := newEndpointRuntimeManager(
		"127.0.0.1", "", nil, nil, nil, nil, handler, nil,
	)
	endpoint := model.Endpoint{
		ID: "scope-open-failure", Port: port,
		Enabled: true, AllowProxy: true, AllowSOCKS5: true,
	}
	if err := manager.ApplyEndpoint(endpoint); err == nil {
		t.Fatal("SOCKS5 endpoint started with a closed association manager")
	}
	assertDualTransportPortReleased(t, port)
}

func TestEndpointSocksGatePassesUDPPolicy(t *testing.T) {
	handler := newEndpointRuntimeSocksHandler()
	gate := &endpointSocksGate{
		current: func() model.Endpoint {
			return model.Endpoint{
				Port: 2260, AllowProxy: true, AllowSOCKS5: true, RequireProxyAuthInfo: true,
			}
		},
		next:             handler,
		associationScope: func() string { return "endpoint#42" },
	}
	client, server := net.Pipe()
	defer client.Close()
	go gate.ServeConnContext(context.Background(), server)
	select {
	case policy := <-handler.policies:
		if !policy.RequireProxyAuthInfo || policy.Socks5UDPPort != 2260 || policy.Socks5UDPAssociationScope != "endpoint#42" {
			t.Fatalf("SOCKS5 endpoint policy: %+v", policy)
		}
	case <-time.After(time.Second):
		t.Fatal("SOCKS5 gate did not invoke handler")
	}
}
