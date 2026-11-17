package proxy

import (
	"bytes"
	"errors"
	"net"
	"strings"
	"testing"
)

func TestSocks5UDPDatagramRoundTrip(t *testing.T) {
	tests := []struct {
		name    string
		target  string
		payload []byte
	}{
		{name: "IPv4", target: "192.0.2.1:53", payload: []byte("ipv4")},
		{name: "IPv6", target: "[2001:db8::1]:5353", payload: []byte("ipv6")},
		{name: "domain", target: "example.com:443", payload: []byte("domain")},
		{name: "empty payload", target: "example.net:123", payload: nil},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			packet, err := encodeSocks5UDPDatagram(test.target, test.payload)
			if err != nil {
				t.Fatalf("encode: %v", err)
			}
			datagram, err := decodeSocks5UDPDatagram(packet)
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			if datagram.target != test.target {
				t.Fatalf("target: got %q, want %q", datagram.target, test.target)
			}
			if !bytes.Equal(datagram.payload, test.payload) {
				t.Fatalf("payload: got %q, want %q", datagram.payload, test.payload)
			}
			if len(datagram.payload) > 0 {
				packet[len(packet)-1] ^= 0xff
				if !bytes.Equal(datagram.payload, test.payload) {
					t.Fatal("decoded payload aliases the receive buffer")
				}
			}
		})
	}
}

func TestDecodeSocks5UDPDatagramRejectsMalformedPackets(t *testing.T) {
	tests := []struct {
		name   string
		packet []byte
		want   error
	}{
		{name: "short header", packet: []byte{0, 0, 0}, want: errSocks5TruncatedUDPDatagram},
		{name: "reserved", packet: []byte{0, 1, 0, socks5AddressTypeIPv4}, want: errSocks5InvalidReserved},
		{name: "fragment", packet: []byte{0, 0, 1, socks5AddressTypeIPv4}, want: errSocks5UDPFragmented},
		{name: "address type", packet: []byte{0, 0, 0, 0x09}, want: errSocks5AddressType},
		{name: "short IPv4", packet: []byte{0, 0, 0, socks5AddressTypeIPv4, 192, 0}, want: errSocks5TruncatedUDPDatagram},
		{name: "short domain", packet: []byte{0, 0, 0, socks5AddressTypeDomain, 3, 'a', 'b'}, want: errSocks5TruncatedUDPDatagram},
		{name: "empty domain", packet: []byte{0, 0, 0, socks5AddressTypeDomain, 0, 0, 53}, want: errSocks5InvalidAddress},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := decodeSocks5UDPDatagram(test.packet)
			if !errors.Is(err, test.want) {
				t.Fatalf("decode error: got %v, want %v", err, test.want)
			}
		})
	}
}

func TestEncodeSocks5UDPDatagramRejectsInvalidTargets(t *testing.T) {
	tests := []string{
		"missing-port",
		":53",
		"example.com:65536",
		net.JoinHostPort(strings.Repeat("a", 256), "53"),
	}
	for _, target := range tests {
		t.Run(target, func(t *testing.T) {
			if _, err := encodeSocks5UDPDatagram(target, nil); !errors.Is(err, errSocks5InvalidAddress) {
				t.Fatalf("encode error: got %v, want invalid address", err)
			}
		})
	}
}

func TestReadSocks5RequestSupportsCommandsAndAddresses(t *testing.T) {
	tests := []struct {
		name    string
		command byte
		target  string
	}{
		{name: "connect IPv4", command: socks5CommandConnect, target: "192.0.2.2:80"},
		{name: "bind IPv6", command: socks5CommandBind, target: "[2001:db8::2]:81"},
		{name: "associate domain", command: socks5CommandUDPAssociate, target: "client.example:5353"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			packet, err := appendSocks5AddrPort(
				[]byte{socks5Version, test.command, 0},
				test.target,
			)
			if err != nil {
				t.Fatalf("build request: %v", err)
			}
			request, reply, err := readSocks5Request(bytes.NewReader(packet))
			if err != nil || reply != 0 {
				t.Fatalf("read request: reply=%d err=%v", reply, err)
			}
			if request.command != test.command || request.target != test.target {
				t.Fatalf("request: got %+v, want command=%d target=%s", request, test.command, test.target)
			}
		})
	}
}

func TestSocks5InboundRejectsBindWithCommandUnsupported(t *testing.T) {
	inbound := NewSocks5Inbound(Socks5InboundConfig{})
	client, reader, done := startSocks5Session(t, inbound)
	defer client.Close()
	writeAll(t, client, []byte{socks5Version, 1, socks5MethodNoAuth})
	if response := readExactly(t, reader, 2); response[1] != socks5MethodNoAuth {
		t.Fatalf("method response: %v", response)
	}

	packet, err := appendSocks5AddrPort(
		[]byte{socks5Version, socks5CommandBind, 0},
		"0.0.0.0:0",
	)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	writeAll(t, client, packet)
	response := readExactly(t, reader, 10)
	if response[1] != socks5ReplyCommandUnsupported {
		t.Fatalf("BIND reply: got %d, want %d", response[1], socks5ReplyCommandUnsupported)
	}
	<-done
}

func TestReadSocks5RequestMapsInvalidAddressType(t *testing.T) {
	packet := []byte{socks5Version, socks5CommandConnect, 0, 0x09}
	_, reply, err := readSocks5Request(bytes.NewReader(packet))
	if !errors.Is(err, errSocks5AddressType) || reply != socks5ReplyAddressUnsupported {
		t.Fatalf("result: reply=%d err=%v", reply, err)
	}
}

func TestWriteSocks5ReplyEncodesTCPAndUDPAddresses(t *testing.T) {
	tests := []struct {
		name   string
		addr   net.Addr
		target string
	}{
		{name: "TCP IPv4", addr: &net.TCPAddr{IP: net.ParseIP("192.0.2.3"), Port: 8080}, target: "192.0.2.3:8080"},
		{name: "TCP IPv6", addr: &net.TCPAddr{IP: net.ParseIP("2001:db8::3"), Port: 8081}, target: "[2001:db8::3]:8081"},
		{name: "UDP IPv4", addr: &net.UDPAddr{IP: net.IPv4zero, Port: 2260}, target: "0.0.0.0:2260"},
		{name: "IPv6 unspecified", addr: &net.UDPAddr{IP: net.IPv6unspecified, Port: 2260}, target: "[::]:2260"},
		{name: "unspecified", addr: nil, target: "0.0.0.0:0"},
		{name: "QUIC empty UDP host", addr: &net.UDPAddr{Port: 58802}, target: "0.0.0.0:58802"},
		{name: "QUIC empty TCP host", addr: &net.TCPAddr{Port: 443}, target: "0.0.0.0:443"},
		{name: "colon port string", addr: stubAddr(":58802"), target: "0.0.0.0:58802"},
		{name: "unparseable", addr: stubAddr("not-a-host-port"), target: "0.0.0.0:0"},
		{name: "empty string", addr: stubAddr(""), target: "0.0.0.0:0"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var response bytes.Buffer
			if err := writeSocks5Reply(&response, socks5ReplySucceeded, test.addr); err != nil {
				t.Fatalf("write reply: %v", err)
			}
			request, reply, err := readSocks5Request(bytes.NewReader(response.Bytes()))
			if err != nil || reply != 0 || request.command != socks5ReplySucceeded {
				t.Fatalf("decode reply: request=%+v reply=%d err=%v", request, reply, err)
			}
			if request.target != test.target {
				t.Fatalf("reply target: got %q, want %q", request.target, test.target)
			}
		})
	}
}

func TestSocks5ReplyBindTargetFallsBackForEmptyHost(t *testing.T) {
	if got := socks5ReplyBindTarget(&net.UDPAddr{Port: 58802}); got != "0.0.0.0:58802" {
		t.Fatalf("empty UDP host: got %q", got)
	}
	if got := socks5ReplyBindTarget(nil); got != "0.0.0.0:0" {
		t.Fatalf("nil addr: got %q", got)
	}
}
