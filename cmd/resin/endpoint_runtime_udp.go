package main

import (
	"context"
	"errors"
	"fmt"
	"net"
)

type endpointPacketHandler interface {
	ServePacketConnContext(context.Context, string, net.PacketConn) error
	OpenUDPAssociationScope(string) error
	CloseUDPAssociationScope(string) error
}

func (r *managedEndpointRuntime) attachUDP(
	packetConn net.PacketConn,
	packetHandler endpointPacketHandler,
	associationScope string,
) {
	r.udpMu.Lock()
	r.packetConn = packetConn
	r.packetHandler = packetHandler
	r.associationScope = associationScope
	r.udpMu.Unlock()
}

func (r *managedEndpointRuntime) currentAssociationScope() string {
	if r == nil {
		return ""
	}
	r.udpMu.Lock()
	defer r.udpMu.Unlock()
	return r.associationScope
}

func (r *managedEndpointRuntime) stopUDP(ctx context.Context) error {
	if r == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	r.udpMu.Lock()
	cancel := r.udpCancel
	packetConn := r.packetConn
	done := r.udpDone
	associationScope := r.associationScope
	packetHandler := r.packetHandler
	r.udpCancel = nil
	r.udpDone = nil
	r.packetConn = nil
	r.udpStarted = false
	r.udpMu.Unlock()
	if cancel != nil {
		cancel()
	}
	var closeErr error
	if packetConn != nil {
		closeErr = packetConn.Close()
	}
	var associationErr error
	if packetHandler != nil {
		// Closing flows can unblock the relay while it is handling a datagram.
		associationErr = packetHandler.CloseUDPAssociationScope(associationScope)
	}
	var waitErr error
	if done != nil {
		select {
		case <-done:
		case <-ctx.Done():
			waitErr = ctx.Err()
		}
	}
	return errors.Join(ignoreClosedNetworkError(closeErr), associationErr, waitErr)
}

func (m *endpointRuntimeManager) bindEndpointUDP(
	port int,
) (net.PacketConn, endpointPacketHandler, error) {
	handler, ok := m.socks5.(endpointPacketHandler)
	if !ok || handler == nil {
		return nil, nil, errors.New("SOCKS5 UDP packet handler is unavailable")
	}
	packetConn, err := net.ListenPacket("udp", formatListenAddress(m.listenAddress, port))
	if err != nil {
		return nil, nil, err
	}
	return packetConn, handler, nil
}

func (m *endpointRuntimeManager) startUDPLocked(runtime *managedEndpointRuntime) {
	if runtime == nil {
		return
	}
	runtime.udpMu.Lock()
	if runtime.udpStarted || runtime.packetConn == nil || runtime.packetHandler == nil {
		runtime.udpMu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	runtime.udpCancel = cancel
	runtime.udpDone = done
	runtime.udpStarted = true
	packetConn := runtime.packetConn
	handler := runtime.packetHandler
	scope := runtime.associationScope
	runtime.udpMu.Unlock()

	go func() {
		defer close(done)
		err := handler.ServePacketConnContext(ctx, scope, packetConn)
		if err == nil || errors.Is(err, net.ErrClosed) || errors.Is(err, context.Canceled) {
			return
		}
		m.handleRuntimeError(runtime, fmt.Errorf("SOCKS5 UDP relay: %w", err))
	}()
}
