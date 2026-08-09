package main

import (
	"context"
	"fmt"
	"net"
	"net/http"

	"github.com/Resinat/Resin/internal/model"
	"github.com/Resinat/Resin/internal/proxy"
	"github.com/Resinat/Resin/internal/service"
)

func (m *endpointRuntimeManager) applySamePortEndpointLocked(
	runtime *managedEndpointRuntime,
	endpoint model.Endpoint,
) error {
	current := runtime.current()
	if current.AllowSOCKS5 == endpoint.AllowSOCKS5 {
		storeEndpointConfig(runtime, endpoint)
		m.setRuntimeStatusLocked(runtime, endpoint.ID)
		return nil
	}
	if endpoint.AllowSOCKS5 {
		packetConn, handler, err := m.bindEndpointUDP(endpoint.Port)
		if err != nil {
			return err
		}
		runtime.packetHandler = handler
		runtime.attachPacketConn(packetConn)
		storeEndpointConfig(runtime, endpoint)
		if runtime.started {
			m.startUDPLocked(runtime)
		}
	} else {
		storeEndpointConfig(runtime, endpoint)
		if err := runtime.stopUDP(context.Background()); err != nil {
			return err
		}
	}
	m.setRuntimeStatusLocked(runtime, endpoint.ID)
	return nil
}

func (m *endpointRuntimeManager) createRuntimeLocked(
	endpoint model.Endpoint,
) (*managedEndpointRuntime, error) {
	address := formatListenAddress(m.listenAddress, endpoint.Port)
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return nil, err
	}
	var packetConn net.PacketConn
	var packetHandler endpointPacketHandler
	if endpoint.AllowSOCKS5 {
		packetConn, packetHandler, err = m.bindEndpointUDP(endpoint.Port)
		if err != nil {
			_ = listener.Close()
			return nil, err
		}
	}
	m.nextScopeID++
	runtime := &managedEndpointRuntime{
		listener:         proxy.NewCountingListener(listener, m.metricsSink),
		associationScope: fmt.Sprintf("%s#%d", endpoint.ID, m.nextScopeID),
		packetHandler:    packetHandler,
		packetConn:       packetConn,
	}
	storeEndpointConfig(runtime, endpoint)
	m.configureRuntimeServers(runtime)
	return runtime, nil
}

func (m *endpointRuntimeManager) configureRuntimeServers(runtime *managedEndpointRuntime) {
	currentConfig := func() model.Endpoint { return runtime.current() }
	httpHandler := newEndpointInboundMux(
		currentConfig,
		m.proxyToken,
		m.forward,
		m.reverse,
		m.apiHandler,
		m.tokenAPI,
	)
	runtime.server = newInboundDemuxServer(
		&http.Server{Handler: httpHandler},
		&endpointSocksGate{
			current:          currentConfig,
			next:             m.socks5,
			associationScope: runtime.associationScope,
		},
	)
}

func storeEndpointConfig(runtime *managedEndpointRuntime, endpoint model.Endpoint) {
	copy := endpoint
	runtime.config.Store(&copy)
}

func (m *endpointRuntimeManager) setRuntimeStatusLocked(
	runtime *managedEndpointRuntime,
	endpointID string,
) {
	state := "starting"
	if runtime.started {
		state = "active"
	}
	m.statuses[endpointID] = service.EndpointRuntimeStatus{State: state}
}
