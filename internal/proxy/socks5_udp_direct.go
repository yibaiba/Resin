package proxy

import (
	"context"
	"fmt"
	"net"
)

// UDPDirectPacketDialer opens direct UDP flows for RESIN_PROXY_BYPASS targets.
// Implementations return both the local packet socket and its resolved target.
type UDPDirectPacketDialer interface {
	ListenPacket(ctx context.Context, target string) (net.PacketConn, net.Addr, error)
}

type netUDPDirectPacketDialer struct{}

func (netUDPDirectPacketDialer) ListenPacket(
	ctx context.Context,
	target string,
) (net.PacketConn, net.Addr, error) {
	destination, err := net.ResolveUDPAddr("udp", target)
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
