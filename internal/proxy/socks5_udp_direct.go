package proxy

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strconv"
)

// UDPDirectPacketDialer opens direct UDP flows for RESIN_PROXY_BYPASS targets.
// Implementations return both the local packet socket and its resolved target.
type UDPDirectPacketDialer interface {
	ListenPacket(ctx context.Context, target string) (net.PacketConn, net.Addr, error)
}

type udpIPResolver interface {
	LookupIPAddr(context.Context, string) ([]net.IPAddr, error)
}

type netUDPDirectPacketDialer struct {
	resolver udpIPResolver
}

func (d netUDPDirectPacketDialer) ListenPacket(
	ctx context.Context,
	target string,
) (net.PacketConn, net.Addr, error) {
	destination, err := d.resolveTarget(ctx, target)
	if err != nil {
		return nil, nil, fmt.Errorf("resolve UDP target %q: %w", target, err)
	}
	var listenConfig net.ListenConfig
	packetConn, err := listenConfig.ListenPacket(ctx, "udp", "")
	if err != nil {
		return nil, nil, fmt.Errorf("open direct UDP socket: %w", err)
	}
	return packetConn, destination, nil
}

func (d netUDPDirectPacketDialer) resolveTarget(
	ctx context.Context,
	target string,
) (*net.UDPAddr, error) {
	host, portText, err := net.SplitHostPort(target)
	if err != nil {
		return nil, err
	}
	port, err := strconv.ParseUint(portText, 10, 16)
	if err != nil {
		return nil, fmt.Errorf("parse UDP target port: %w", err)
	}
	if address, parseErr := netip.ParseAddr(host); parseErr == nil {
		return net.UDPAddrFromAddrPort(netip.AddrPortFrom(address, uint16(port))), nil
	}
	resolver := d.resolver
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	addresses, err := resolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	if len(addresses) == 0 {
		return nil, errors.New("UDP target resolved without addresses")
	}
	address, err := selectUDPIPAddr(addresses)
	if err != nil {
		return nil, err
	}
	return &net.UDPAddr{
		IP: address.IP, Port: int(port), Zone: address.Zone,
	}, nil
}

func selectUDPIPAddr(addresses []net.IPAddr) (net.IPAddr, error) {
	// A UDP flow gets one destination and cannot detect a silent first-address miss.
	// Prefer IPv4 for dual-stack names while retaining IPv6-only support.
	var firstIPv6 net.IPAddr
	for _, address := range addresses {
		if ipv4 := address.IP.To4(); ipv4 != nil {
			return net.IPAddr{IP: ipv4}, nil
		}
		if firstIPv6.IP == nil && address.IP.To16() != nil {
			firstIPv6 = address
		}
	}
	if firstIPv6.IP != nil {
		return firstIPv6, nil
	}
	return net.IPAddr{}, errors.New("UDP target resolved without valid IP addresses")
}
