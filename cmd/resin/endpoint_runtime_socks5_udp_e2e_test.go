package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"net"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Resinat/Resin/internal/model"
	"github.com/Resinat/Resin/internal/proxy"
)

const socks5UDPTestReadTimeout = 150 * time.Millisecond

type endpointUDPEventEmitter struct {
	logs chan proxy.RequestLogEntry
}

func (e *endpointUDPEventEmitter) EmitRequestFinished(proxy.RequestFinishedEvent) {}
func (e *endpointUDPEventEmitter) EmitRequestLog(entry proxy.RequestLogEntry) {
	e.logs <- entry
}

type recordingUDPEcho struct {
	conn     *net.UDPConn
	received atomic.Int64
}

func startRecordingUDPEcho(t *testing.T, network string, ip net.IP) *recordingUDPEcho {
	t.Helper()
	conn, err := net.ListenUDP(network, &net.UDPAddr{IP: ip})
	if err != nil {
		t.Fatalf("listen %s echo: %v", network, err)
	}
	echo := &recordingUDPEcho{conn: conn}
	go func() {
		buffer := make([]byte, 65535)
		for {
			n, source, err := conn.ReadFromUDP(buffer)
			if err != nil {
				return
			}
			echo.received.Add(1)
			_, _ = conn.WriteToUDP(buffer[:n], source)
		}
	}()
	t.Cleanup(func() { _ = conn.Close() })
	return echo
}

type socks5UDPExchange struct {
	client    *net.UDPConn
	proxyAddr *net.UDPAddr
	target    string
	payload   []byte
}

func TestEndpointRuntimeSOCKS5UDPAuthenticatedEndToEnd(t *testing.T) {
	port := reserveDualTransportPort(t)
	v4Echo := startRecordingUDPEcho(t, "udp4", net.IPv4(127, 0, 0, 1))
	v6Echo := startRecordingUDPEcho(t, "udp6", net.ParseIP("::1"))
	emitter := &endpointUDPEventEmitter{logs: make(chan proxy.RequestLogEntry, 4)}
	socksInbound := proxy.NewSocks5Inbound(proxy.Socks5InboundConfig{
		ProxyToken: "token",
		Events:     emitter,
		ProxyBypassRules: []string{
			"127.0.0.0/8", "::1/128", "localhost",
		},
	})
	manager := newEndpointRuntimeManager(
		"127.0.0.1", "token", nil, nil, nil, nil, socksInbound, nil,
	)
	endpoint := model.Endpoint{
		ID: "udp-e2e", Port: port, Enabled: true,
		AllowProxy: true, AllowSOCKS5: true, RequireProxyAuthInfo: true,
	}
	if err := manager.ApplyEndpoint(endpoint); err != nil {
		t.Fatalf("ApplyEndpoint: %v", err)
	}
	manager.Start()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := manager.Shutdown(ctx); err != nil {
			t.Errorf("Shutdown: %v", err)
		}
	})

	proxyAddr := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: port}
	client := listenEndpointUDPClient(t)
	attacker := listenEndpointUDPClient(t)
	assertRejectedUDPDatagram(t, socks5UDPExchange{
		client: attacker, proxyAddr: proxyAddr,
		target: v4Echo.conn.LocalAddr().String(), payload: []byte("no association"),
	}, v4Echo)

	control := openAuthenticatedUDPAssociation(t, port, client.LocalAddr().(*net.UDPAddr).Port)
	for _, exchange := range []socks5UDPExchange{
		{
			client: client, proxyAddr: proxyAddr,
			target: v4Echo.conn.LocalAddr().String(), payload: []byte("IPv4 payload"),
		},
		{
			client: client, proxyAddr: proxyAddr,
			target:  net.JoinHostPort("localhost", strconv.Itoa(v4Echo.conn.LocalAddr().(*net.UDPAddr).Port)),
			payload: []byte("domain payload"),
		},
		{
			client: client, proxyAddr: proxyAddr,
			target: v6Echo.conn.LocalAddr().String(), payload: []byte("IPv6 payload"),
		},
	} {
		assertUDPExchange(t, exchange)
	}

	assertRejectedUDPDatagram(t, socks5UDPExchange{
		client: attacker, proxyAddr: proxyAddr,
		target: v4Echo.conn.LocalAddr().String(), payload: []byte("forged source"),
	}, v4Echo)
	fragmented := encodeSOCKS5UDPTestPacket(
		t, v4Echo.conn.LocalAddr().String(), []byte("fragmented"),
	)
	fragmented[2] = 1
	assertRejectedRawUDPDatagram(t, client, proxyAddr, fragmented, v4Echo)
	assertRejectedRawUDPDatagram(t, client, proxyAddr, []byte{0, 0, 0}, v4Echo)

	_ = control.Close()
	waitEndpointUDPLog(t, emitter.logs)
	assertRejectedUDPDatagram(t, socks5UDPExchange{
		client: client, proxyAddr: proxyAddr,
		target: v4Echo.conn.LocalAddr().String(), payload: []byte("closed control"),
	}, v4Echo)
}

func listenEndpointUDPClient(t *testing.T) *net.UDPConn {
	t.Helper()
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatalf("listen UDP client: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func openAuthenticatedUDPAssociation(t *testing.T, proxyPort int, clientPort int) net.Conn {
	t.Helper()
	conn, err := net.Dial("tcp", formatListenAddress("127.0.0.1", proxyPort))
	if err != nil {
		t.Fatalf("dial SOCKS5 endpoint: %v", err)
	}
	reader := bufio.NewReader(conn)
	writeEndpointTestBytes(t, conn, []byte{5, 1, 2})
	if reply := readEndpointTestBytes(t, reader, 2); !bytes.Equal(reply, []byte{5, 2}) {
		t.Fatalf("SOCKS5 method reply: %v", reply)
	}
	credentials := []byte{1, byte(len("platform.account"))}
	credentials = append(credentials, "platform.account"...)
	credentials = append(credentials, byte(len("token")))
	credentials = append(credentials, "token"...)
	writeEndpointTestBytes(t, conn, credentials)
	if reply := readEndpointTestBytes(t, reader, 2); !bytes.Equal(reply, []byte{1, 0}) {
		t.Fatalf("SOCKS5 auth reply: %v", reply)
	}
	request := []byte{5, 3, 0, 1, 0, 0, 0, 0}
	request = binary.BigEndian.AppendUint16(request, uint16(clientPort))
	writeEndpointTestBytes(t, conn, request)
	replyCode, replyTarget := readEndpointSOCKS5Reply(t, reader)
	if replyCode != 0 || replyTarget != formatListenAddress("0.0.0.0", proxyPort) {
		t.Fatalf("UDP ASSOCIATE reply: code=%d target=%q", replyCode, replyTarget)
	}
	return conn
}

func readEndpointSOCKS5Reply(t *testing.T, reader io.Reader) (byte, string) {
	t.Helper()
	header := readEndpointTestBytes(t, reader, 4)
	host := readEndpointSOCKS5Host(t, reader, header[3])
	portBytes := readEndpointTestBytes(t, reader, 2)
	return header[1], net.JoinHostPort(host, strconv.Itoa(int(binary.BigEndian.Uint16(portBytes))))
}

func readEndpointSOCKS5Host(t *testing.T, reader io.Reader, addressType byte) string {
	t.Helper()
	switch addressType {
	case 1:
		return net.IP(readEndpointTestBytes(t, reader, net.IPv4len)).String()
	case 4:
		return net.IP(readEndpointTestBytes(t, reader, net.IPv6len)).String()
	case 3:
		length := readEndpointTestBytes(t, reader, 1)[0]
		return string(readEndpointTestBytes(t, reader, int(length)))
	default:
		t.Fatalf("unsupported SOCKS5 address type %d", addressType)
		return ""
	}
}

func assertUDPExchange(t *testing.T, exchange socks5UDPExchange) {
	t.Helper()
	packet := encodeSOCKS5UDPTestPacket(t, exchange.target, exchange.payload)
	if _, err := exchange.client.WriteToUDP(packet, exchange.proxyAddr); err != nil {
		t.Fatalf("write SOCKS5 UDP packet: %v", err)
	}
	_ = exchange.client.SetReadDeadline(time.Now().Add(time.Second))
	response := make([]byte, 65535)
	n, _, err := exchange.client.ReadFromUDP(response)
	if err != nil {
		t.Fatalf("read SOCKS5 UDP response for %s: %v", exchange.target, err)
	}
	if payload := decodeSOCKS5UDPTestPayload(t, response[:n]); !bytes.Equal(payload, exchange.payload) {
		t.Fatalf("UDP response payload: got %q, want %q", payload, exchange.payload)
	}
	_ = exchange.client.SetReadDeadline(time.Time{})
}

func assertRejectedUDPDatagram(t *testing.T, exchange socks5UDPExchange, echo *recordingUDPEcho) {
	t.Helper()
	packet := encodeSOCKS5UDPTestPacket(t, exchange.target, exchange.payload)
	assertRejectedRawUDPDatagram(t, exchange.client, exchange.proxyAddr, packet, echo)
}

func assertRejectedRawUDPDatagram(
	t *testing.T,
	client *net.UDPConn,
	proxyAddr *net.UDPAddr,
	packet []byte,
	echo *recordingUDPEcho,
) {
	t.Helper()
	before := echo.received.Load()
	if _, err := client.WriteToUDP(packet, proxyAddr); err != nil {
		t.Fatalf("write rejected UDP packet: %v", err)
	}
	_ = client.SetReadDeadline(time.Now().Add(socks5UDPTestReadTimeout))
	buffer := make([]byte, 65535)
	if _, _, err := client.ReadFromUDP(buffer); err == nil {
		t.Fatal("rejected UDP packet unexpectedly received a response")
	} else if netErr, ok := err.(net.Error); !ok || !netErr.Timeout() {
		t.Fatalf("read rejected UDP response: %v", err)
	}
	if got := echo.received.Load(); got != before {
		t.Fatalf("rejected UDP packet reached upstream: before=%d after=%d", before, got)
	}
	_ = client.SetReadDeadline(time.Time{})
}

func encodeSOCKS5UDPTestPacket(t *testing.T, target string, payload []byte) []byte {
	t.Helper()
	host, portText, err := net.SplitHostPort(target)
	if err != nil {
		t.Fatalf("split UDP target: %v", err)
	}
	packet := []byte{0, 0, 0}
	if ip := net.ParseIP(host); ip != nil {
		if ip4 := ip.To4(); ip4 != nil {
			packet = append(packet, 1)
			packet = append(packet, ip4...)
		} else {
			packet = append(packet, 4)
			packet = append(packet, ip.To16()...)
		}
	} else {
		packet = append(packet, 3, byte(len(host)))
		packet = append(packet, host...)
	}
	port, err := strconv.ParseUint(portText, 10, 16)
	if err != nil {
		t.Fatalf("parse UDP target port: %v", err)
	}
	packet = binary.BigEndian.AppendUint16(packet, uint16(port))
	return append(packet, payload...)
}

func decodeSOCKS5UDPTestPayload(t *testing.T, packet []byte) []byte {
	t.Helper()
	if len(packet) < 4 || packet[0] != 0 || packet[1] != 0 || packet[2] != 0 {
		t.Fatalf("invalid SOCKS5 UDP response: %v", packet)
	}
	reader := bytes.NewReader(packet[4:])
	_ = readEndpointSOCKS5Host(t, reader, packet[3])
	_ = readEndpointTestBytes(t, reader, 2)
	payload, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read UDP response payload: %v", err)
	}
	return payload
}

func writeEndpointTestBytes(t *testing.T, writer io.Writer, payload []byte) {
	t.Helper()
	if _, err := writer.Write(payload); err != nil {
		t.Fatalf("write test bytes: %v", err)
	}
}

func readEndpointTestBytes(t *testing.T, reader io.Reader, size int) []byte {
	t.Helper()
	payload := make([]byte, size)
	if _, err := io.ReadFull(reader, payload); err != nil {
		t.Fatalf("read %d test bytes: %v", size, err)
	}
	return payload
}

func waitEndpointUDPLog(t *testing.T, logs <-chan proxy.RequestLogEntry) {
	t.Helper()
	select {
	case entry := <-logs:
		if entry.HTTPMethod != "UDP_ASSOCIATE" || entry.ProxyType != proxy.ProxyTypeSocks5Forward {
			t.Fatalf("UDP request log: %+v", entry)
		}
	case <-time.After(time.Second):
		t.Fatal("UDP association request log was not emitted")
	}
}
