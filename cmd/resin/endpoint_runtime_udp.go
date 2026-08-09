package main

import (
	"context"
	"errors"
	"fmt"
	"net"
)

type endpointPacketHandler interface {
	ServePacketConnContext(context.Context, string, net.PacketConn) error
	CloseUDPAssociationScope(string) error
}

func (r *managedEndpointRuntime) attachPacketConn(packetConn net.PacketConn) {
	r.udpMu.Lock()
	r.packetConn = packetConn
	r.udpMu.Unlock()
}

func (r *managedEndpointRuntime) stopUDP() error {
	if r == nil {
		return nil
	}
	r.udpMu.Lock()
	cancel := r.udpCancel
	packetConn := r.packetConn
	done := r.udpDone
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
	if done != nil {
		<-done
	}
	var associationErr error
	if r.packetHandler != nil {
		associationErr = r.packetHandler.CloseUDPAssociationScope(r.associationScope)
	}
	return errors.Join(ignoreClosedNetworkError(closeErr), associationErr)
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
