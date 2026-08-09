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
		associationScope := m.nextAssociationScopeLocked(endpoint.ID)
		if err := handler.OpenUDPAssociationScope(associationScope); err != nil {
			_ = packetConn.Close()
			return err
		}
		runtime.attachUDP(packetConn, handler)
		storeEndpointState(runtime, endpoint, associationScope)
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
	associationScope := ""
	if endpoint.AllowSOCKS5 {
		packetConn, packetHandler, err = m.bindEndpointUDP(endpoint.Port)
		if err != nil {
			_ = listener.Close()
			return nil, err
		}
		associationScope = m.nextAssociationScopeLocked(endpoint.ID)
		if err := packetHandler.OpenUDPAssociationScope(associationScope); err != nil {
			_ = packetConn.Close()
			_ = listener.Close()
			return nil, err
		}
	}
	runtime := &managedEndpointRuntime{
		listener:      proxy.NewCountingListener(listener, m.metricsSink),
		packetHandler: packetHandler,
		packetConn:    packetConn,
	}
	storeEndpointState(runtime, endpoint, associationScope)
	m.configureRuntimeServers(runtime)
	return runtime, nil
}

func (m *endpointRuntimeManager) configureRuntimeServers(runtime *managedEndpointRuntime) {
	currentConfig := func() model.Endpoint { return runtime.current() }
	scopeHandler, _ := m.socks5.(endpointPacketHandler)
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
			current:      runtime.currentSnapshot,
			next:         m.socks5,
			scopeHandler: scopeHandler,
		},
	)
}

func (m *endpointRuntimeManager) nextAssociationScopeLocked(endpointID string) string {
	m.nextScopeID++
	return fmt.Sprintf("%s#%d", endpointID, m.nextScopeID)
}

func storeEndpointConfig(runtime *managedEndpointRuntime, endpoint model.Endpoint) {
	storeEndpointState(runtime, endpoint, runtime.currentSnapshot().associationScope)
}

func storeEndpointState(
	runtime *managedEndpointRuntime,
	endpoint model.Endpoint,
	associationScope string,
) {
	runtime.state.Store(&endpointRuntimeSnapshot{
		endpoint:         endpoint,
		associationScope: associationScope,
	})
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
