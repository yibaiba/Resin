package proxy

import (
	"bufio"
	"context"
	"net"
	"net/netip"
	"testing"
	"time"
)

type socks5AddressConn struct {
	net.Conn
	localAddr  net.Addr
	remoteAddr net.Addr
}

func (c *socks5AddressConn) LocalAddr() net.Addr  { return c.localAddr }
func (c *socks5AddressConn) RemoteAddr() net.Addr { return c.remoteAddr }

type socks5UDPControlTestOptions struct {
	ctx       context.Context
	scope     string
	proxyPort uint16
	clientIP  netip.Addr
	tcpPort   uint16
}

func startSocks5UDPControlSession(
	t *testing.T,
	inbound *Socks5Inbound,
	options socks5UDPControlTestOptions,
) (net.Conn, *bufio.Reader, <-chan struct{}) {
	t.Helper()
	clientConn, serverBase := net.Pipe()
	serverConn := &socks5AddressConn{
		Conn:       serverBase,
		localAddr:  net.TCPAddrFromAddrPort(netip.MustParseAddrPort("127.0.0.1:2260")),
		remoteAddr: net.TCPAddrFromAddrPort(netip.AddrPortFrom(options.clientIP, options.tcpPort)),
	}
	ctx := options.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	ctx = ContextWithInboundPolicy(ctx, InboundPolicy{
		RequireProxyAuthInfo:      InboundPolicyFromContext(ctx).RequireProxyAuthInfo,
		Socks5UDPAssociationScope: options.scope,
		Socks5UDPPort:             options.proxyPort,
	})
	done := make(chan struct{})
	go func() {
		defer close(done)
		inbound.ServeConnContext(ctx, serverConn)
	}()
	return clientConn, bufio.NewReader(clientConn), done
}

func writeSocks5UDPAssociateRequest(t *testing.T, conn net.Conn, target string) {
	t.Helper()
	request, err := appendSocks5AddrPort(
		[]byte{socks5Version, socks5CommandUDPAssociate, 0},
		target,
	)
	if err != nil {
		t.Fatalf("encode UDP ASSOCIATE request: %v", err)
	}
	writeAll(t, conn, request)
}

func readSocks5CommandReply(t *testing.T, reader *bufio.Reader) (byte, string) {
	t.Helper()
	header := readExactly(t, reader, 4)
	if header[0] != socks5Version || header[2] != 0 {
		t.Fatalf("invalid SOCKS5 command reply header: %v", header)
	}
	target, err := readSocks5AddrPort(reader, header[3])
	if err != nil {
		t.Fatalf("read SOCKS5 command reply address: %v", err)
	}
	return header[1], target
}

func authenticateSocks5UDPControl(
	t *testing.T,
	conn net.Conn,
	reader *bufio.Reader,
	username string,
	password string,
) {
	t.Helper()
	writeAll(t, conn, []byte{socks5Version, 1, socks5MethodUserPass})
	if reply := readExactly(t, reader, 2); reply[1] != socks5MethodUserPass {
		t.Fatalf("selected SOCKS5 method: got %v", reply)
	}
	writeAll(t, conn, socks5UserPassPacket(username, password))
	if reply := readExactly(t, reader, 2); reply[1] != socks5UserPassStatusSuccess {
		t.Fatalf("SOCKS5 auth reply: got %v", reply)
	}
}

func TestSocks5UDPAssociateInheritsAuthenticatedIdentity(t *testing.T) {
	emitter := newMockEventEmitter()
	inbound := NewSocks5Inbound(Socks5InboundConfig{ProxyToken: "token", Events: emitter})
	t.Cleanup(func() { _ = inbound.CloseUDPAssociations() })
	options := socks5UDPControlTestOptions{
		scope: "endpoint-generation", proxyPort: 2260,
		clientIP: netip.MustParseAddr("198.51.100.20"), tcpPort: 40100,
	}
	client, reader, done := startSocks5UDPControlSession(t, inbound, options)
	authenticateSocks5UDPControl(t, client, reader, "platform.account", "token")
	writeSocks5UDPAssociateRequest(t, client, "0.0.0.0:45000")
	replyCode, replyTarget := readSocks5CommandReply(t, reader)
	if replyCode != socks5ReplySucceeded || replyTarget != "0.0.0.0:2260" {
		t.Fatalf("UDP ASSOCIATE reply: code=%d target=%q", replyCode, replyTarget)
	}

	source := netip.MustParseAddrPort("198.51.100.20:45000")
	association, err := inbound.udpAssociations.match(options.scope, source)
	if err != nil {
		t.Fatalf("match registered UDP association: %v", err)
	}
	if association.platformName != "platform" || association.account != "account" {
		t.Fatalf("association identity: platform=%q account=%q", association.platformName, association.account)
	}

	_ = client.Close()
	waitSocks5ControlDone(t, done)
	if _, err := inbound.udpAssociations.match(options.scope, source); err == nil {
		t.Fatal("UDP association survived TCP control close")
	}
	select {
	case entry := <-emitter.logCh:
		if entry.HTTPMethod != "UDP_ASSOCIATE" || entry.ProxyType != ProxyTypeSocks5Forward {
			t.Fatalf("UDP association log identity: %+v", entry)
		}
		if entry.Account != "account" || !entry.NetOK {
			t.Fatalf("UDP association log account/success: %+v", entry)
		}
	case <-time.After(time.Second):
		t.Fatal("UDP association did not emit a request log")
	}
}

func TestSocks5UDPAssociateRequireProxyAuthInfo(t *testing.T) {
	inbound := NewSocks5Inbound(Socks5InboundConfig{})
	t.Cleanup(func() { _ = inbound.CloseUDPAssociations() })
	baseCtx := ContextWithInboundPolicy(
		context.Background(),
		InboundPolicy{RequireProxyAuthInfo: true},
	)
	options := socks5UDPControlTestOptions{
		ctx: baseCtx, scope: "required-auth", proxyPort: 2260,
		clientIP: netip.MustParseAddr("198.51.100.21"), tcpPort: 40101,
	}
	client, reader, done := startSocks5UDPControlSession(t, inbound, options)
	authenticateSocks5UDPControl(t, client, reader, "platform.account", "password")
	writeSocks5UDPAssociateRequest(t, client, "0.0.0.0:45001")
	if replyCode, _ := readSocks5CommandReply(t, reader); replyCode != socks5ReplySucceeded {
		t.Fatalf("UDP ASSOCIATE reply code: got %d", replyCode)
	}
	association, err := inbound.udpAssociations.match(
		options.scope,
		netip.MustParseAddrPort("198.51.100.21:45001"),
	)
	if err != nil || association.platformName != "platform" || association.account != "account" {
		t.Fatalf("required auth identity: association=%p err=%v", association, err)
	}
	_ = client.Close()
	waitSocks5ControlDone(t, done)
}

func TestSocks5UDPAssociateRejectsConcurrentPendingSessions(t *testing.T) {
	inbound := NewSocks5Inbound(Socks5InboundConfig{ProxyToken: "token"})
	t.Cleanup(func() { _ = inbound.CloseUDPAssociations() })
	options := socks5UDPControlTestOptions{
		scope: "pending-scope", proxyPort: 2260,
		clientIP: netip.MustParseAddr("198.51.100.22"), tcpPort: 40102,
	}
	first, firstReader, firstDone := startSocks5UDPControlSession(t, inbound, options)
	authenticateSocks5UDPControl(t, first, firstReader, "platform.first", "token")
	writeSocks5UDPAssociateRequest(t, first, "0.0.0.0:0")
	if code, _ := readSocks5CommandReply(t, firstReader); code != socks5ReplySucceeded {
		t.Fatalf("first pending reply: got %d", code)
	}

	options.tcpPort++
	second, secondReader, secondDone := startSocks5UDPControlSession(t, inbound, options)
	authenticateSocks5UDPControl(t, second, secondReader, "platform.second", "token")
	writeSocks5UDPAssociateRequest(t, second, "0.0.0.0:0")
	if code, _ := readSocks5CommandReply(t, secondReader); code != socks5ReplyGeneralFailure {
		t.Fatalf("second pending reply: got %d, want general failure", code)
	}
	_ = second.Close()
	waitSocks5ControlDone(t, secondDone)
	_ = first.Close()
	waitSocks5ControlDone(t, firstDone)
}

func TestSocks5UDPAssociateContextCancelClosesAssociation(t *testing.T) {
	inbound := NewSocks5Inbound(Socks5InboundConfig{})
	t.Cleanup(func() { _ = inbound.CloseUDPAssociations() })
	ctx, cancel := context.WithCancel(context.Background())
	options := socks5UDPControlTestOptions{
		ctx: ctx, scope: "cancel-scope", proxyPort: 2260,
		clientIP: netip.MustParseAddr("198.51.100.23"), tcpPort: 40103,
	}
	client, reader, done := startSocks5UDPControlSession(t, inbound, options)
	writeAll(t, client, []byte{socks5Version, 1, socks5MethodNoAuth})
	_ = readExactly(t, reader, 2)
	writeSocks5UDPAssociateRequest(t, client, "0.0.0.0:45003")
	if code, _ := readSocks5CommandReply(t, reader); code != socks5ReplySucceeded {
		t.Fatalf("UDP ASSOCIATE reply: got %d", code)
	}
	cancel()
	waitSocks5ControlDone(t, done)
	_ = client.Close()
	if _, err := inbound.udpAssociations.match(
		options.scope,
		netip.MustParseAddrPort("198.51.100.23:45003"),
	); err == nil {
		t.Fatal("UDP association survived control context cancellation")
	}
}

func TestSocks5UDPAssociateRegistrationFailureFinishesLifecycle(t *testing.T) {
	emitter := newMockEventEmitter()
	inbound := NewSocks5Inbound(Socks5InboundConfig{Events: emitter})
	if err := inbound.CloseUDPAssociations(); err != nil {
		t.Fatalf("close association manager: %v", err)
	}
	options := socks5UDPControlTestOptions{
		scope: "closed-manager", proxyPort: 2260,
		clientIP: netip.MustParseAddr("198.51.100.24"), tcpPort: 40104,
	}
	client, reader, done := startSocks5UDPControlSession(t, inbound, options)
	writeAll(t, client, []byte{socks5Version, 1, socks5MethodNoAuth})
	_ = readExactly(t, reader, 2)
	writeSocks5UDPAssociateRequest(t, client, "0.0.0.0:45004")
	if code, _ := readSocks5CommandReply(t, reader); code != socks5ReplyGeneralFailure {
		t.Fatalf("UDP ASSOCIATE reply: got %d, want general failure", code)
	}
	waitSocks5ControlDone(t, done)
	_ = client.Close()

	select {
	case entry := <-emitter.logCh:
		if entry.HTTPMethod != "UDP_ASSOCIATE" || entry.NetOK {
			t.Fatalf("registration failure request log: %+v", entry)
		}
		if entry.UpstreamStage != "socks5_udp_association_register" {
			t.Fatalf("registration failure stage: got %q", entry.UpstreamStage)
		}
	case <-time.After(time.Second):
		t.Fatal("registration failure did not finish lifecycle")
	}
	assertNoUDPAssociationLifecycle(t, emitter)
}

func TestSocks5UDPAssociateStartsIdleTimeoutAfterSuccessReply(t *testing.T) {
	const idleTimeout = 5 * time.Millisecond
	emitter := newMockEventEmitter()
	inbound := NewSocks5Inbound(Socks5InboundConfig{
		Events: emitter, UDPIdleTimeout: idleTimeout,
	})
	t.Cleanup(func() { _ = inbound.CloseUDPAssociations() })
	options := socks5UDPControlTestOptions{
		scope: "deferred-idle", proxyPort: 2260,
		clientIP: netip.MustParseAddr("198.51.100.25"), tcpPort: 40105,
	}
	client, reader, done := startSocks5UDPControlSession(t, inbound, options)
	writeAll(t, client, []byte{socks5Version, 1, socks5MethodNoAuth})
	_ = readExactly(t, reader, 2)
	writeSocks5UDPAssociateRequest(t, client, "0.0.0.0:45005")

	time.Sleep(10 * idleTimeout)
	select {
	case entry := <-emitter.logCh:
		t.Fatalf("association expired before success reply: %+v", entry)
	default:
	}
	if code, _ := readSocks5CommandReply(t, reader); code != socks5ReplySucceeded {
		t.Fatalf("UDP ASSOCIATE reply: got %d", code)
	}
	select {
	case entry := <-emitter.logCh:
		if entry.HTTPMethod != "UDP_ASSOCIATE" || !entry.NetOK {
			t.Fatalf("idle-expired association request log: %+v", entry)
		}
	case <-time.After(time.Second):
		t.Fatal("association did not expire after success reply")
	}
	_ = client.Close()
	waitSocks5ControlDone(t, done)
	assertNoUDPAssociationLifecycle(t, emitter)
}

func waitSocks5ControlDone(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("SOCKS5 control connection did not close")
	}
}
