//go:build integration

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/netip"
	"os"
	"testing"
	"time"

	"github.com/Resinat/Resin/internal/model"
	"github.com/Resinat/Resin/internal/node"
	"github.com/Resinat/Resin/internal/outbound"
	"github.com/Resinat/Resin/internal/platform"
	"github.com/Resinat/Resin/internal/proxy"
	"github.com/Resinat/Resin/internal/routing"
	"github.com/Resinat/Resin/internal/subscription"
	"github.com/Resinat/Resin/internal/topology"
	"github.com/sagernet/sing-box"
	"github.com/sagernet/sing-box/include"
	"github.com/sagernet/sing-box/option"
	sJson "github.com/sagernet/sing/common/json"
	N "github.com/sagernet/sing/common/network"
)

const protocolMatrixUDPTimeout = 10 * time.Second

var protocolMatrixCases = []string{
	"socks", "shadowsocks", "vmess", "vless",
	"trojan", "hysteria", "hysteria2", "tuic",
}

type protocolMatrixNode struct {
	pool     *topology.GlobalNodePool
	platform *platform.Platform
	hash     node.Hash
}

func TestRealSingboxUDPProtocolMatrix(t *testing.T) {
	certificates := newProtocolMatrixCertificates(t)
	for _, protocol := range protocolMatrixCases {
		t.Run(protocol, func(t *testing.T) {
			runRealSingboxUDPProtocol(t, protocol, certificates)
		})
	}
}

func runRealSingboxUDPProtocol(
	t *testing.T,
	protocol string,
	certificates protocolMatrixCertificates,
) {
	t.Helper()
	serverPort := reserveDualTransportPort(t)
	config := newProtocolMatrixConfig(t, protocol, serverPort, certificates)
	startProtocolMatrixServer(t, config.inbound)

	builder := newProtocolMatrixBuilder(t)
	rawOutbound := marshalProtocolMatrixJSON(t, config.outbound)
	matrixNode, manager := newProtocolMatrixNode(t, rawOutbound, builder)
	t.Cleanup(func() { closeProtocolMatrixManager(t, manager) })
	endpointPort := startProtocolMatrixEndpoint(t, matrixNode)

	echo := startRecordingUDPEcho(t, "udp4", net.IPv4(127, 0, 0, 1))
	client := listenEndpointUDPClient(t)
	control := openAuthenticatedUDPAssociation(t, endpointPort, client.LocalAddr().(*net.UDPAddr).Port)
	t.Cleanup(func() { closeProtocolMatrixResource(t, "SOCKS5 control connection", control.Close) })
	assertProtocolMatrixUDPExchange(t, protocol, endpointPort, client, echo.conn.LocalAddr().String())
}

func startProtocolMatrixServer(t *testing.T, inbound map[string]any) {
	t.Helper()
	serverConfig := map[string]any{
		"log":      map[string]any{"disabled": true},
		"inbounds": []any{inbound},
		"outbounds": []any{map[string]any{
			"type": "direct", "tag": "direct",
		}},
		"route": map[string]any{"final": "direct"},
	}
	ctx := include.Context(context.Background())
	var options option.Options
	if err := sJson.UnmarshalContext(ctx, marshalProtocolMatrixJSON(t, serverConfig), &options); err != nil {
		t.Fatalf("parse sing-box server configuration: %v", err)
	}
	server, err := box.New(box.Options{Context: ctx, Options: options})
	if err != nil {
		t.Fatalf("create sing-box server: %v", err)
	}
	if err := server.Start(); err != nil {
		_ = server.Close()
		t.Fatalf("start sing-box server: %v", err)
	}
	t.Cleanup(func() {
		err := server.Close()
		if err != nil && !errors.Is(err, os.ErrClosed) {
			t.Errorf("close sing-box server: %v", err)
		}
	})
}

func newProtocolMatrixBuilder(t *testing.T) *outbound.SingboxBuilder {
	t.Helper()
	builder, err := outbound.NewSingboxBuilderWithConfig(outbound.SingboxBuilderConfig{
		DNSUpstreams:    []string{"local"},
		SingboxLogLevel: "disabled",
	})
	if err != nil {
		t.Fatalf("create Resin sing-box builder: %v", err)
	}
	t.Cleanup(func() { closeProtocolMatrixResource(t, "sing-box builder", builder.Close) })
	return builder
}

func newProtocolMatrixNode(
	t *testing.T,
	rawOutbound json.RawMessage,
	builder outbound.OutboundBuilder,
) (protocolMatrixNode, *outbound.OutboundManager) {
	t.Helper()
	subscriptions := topology.NewSubscriptionManager()
	pool := topology.NewGlobalNodePool(topology.PoolConfig{
		SubLookup:              subscriptions.Lookup,
		GeoLookup:              func(netip.Addr) string { return "us" },
		MaxLatencyTableEntries: 16,
		MaxConsecutiveFailures: func() int { return 3 },
		LatencyDecayWindow:     func() time.Duration { return 10 * time.Minute },
	})
	platformEntry := platform.NewPlatform("matrix-platform-id", "platform", nil, nil)
	platformEntry.StickyTTLNs = int64(time.Hour)
	platformEntry.ReverseProxyMissAction = "TREAT_AS_EMPTY"
	pool.RegisterPlatform(platformEntry)

	sub := subscription.NewSubscription("matrix-sub", "matrix-sub", "http://127.0.0.1/unused", true, false)
	subscriptions.Register(sub)
	hash := node.HashFromRawOptions(rawOutbound)
	sub.ManagedNodes().StoreNode(hash, subscription.ManagedNode{Tags: []string{"matrix"}})
	pool.AddNodeFromSub(hash, rawOutbound, sub.ID)

	manager := outbound.NewOutboundManager(pool, builder)
	manager.EnsureNodeOutbound(hash)
	entry := requireProtocolMatrixEntry(t, pool, hash)
	prepareProtocolMatrixHealth(t, pool, entry, hash)
	return protocolMatrixNode{pool: pool, platform: platformEntry, hash: hash}, manager
}

func requireProtocolMatrixEntry(
	t *testing.T,
	pool *topology.GlobalNodePool,
	hash node.Hash,
) *node.NodeEntry {
	t.Helper()
	entry, ok := pool.GetEntry(hash)
	if !ok {
		t.Fatal("protocol matrix node was not added to the pool")
	}
	if !entry.HasOutbound() {
		t.Fatalf("build protocol matrix outbound: %s", entry.GetLastError())
	}
	return entry
}

func prepareProtocolMatrixHealth(
	t *testing.T,
	pool *topology.GlobalNodePool,
	entry *node.NodeEntry,
	hash node.Hash,
) {
	t.Helper()
	entry.SetEgressIP(netip.MustParseAddr("203.0.113.10"))
	entry.LatencyTable.Update("127.0.0.1", 20*time.Millisecond, 10*time.Minute)
	pool.RecordResult(hash, true)
	pool.RecordUDPProbeResult(hash, true)
	pool.NotifyNodeDirty(hash)
}

func startProtocolMatrixEndpoint(t *testing.T, matrixNode protocolMatrixNode) int {
	t.Helper()
	if !matrixNode.platform.NetworkView(N.NetworkUDP).Contains(matrixNode.hash) {
		t.Fatal("real sing-box outbound was not admitted to the UDP routing view")
	}
	router := routing.NewRouter(routing.RouterConfig{
		Pool: matrixNode.pool, Authorities: func() []string { return []string{"127.0.0.1"} },
		P2CWindow: func() time.Duration { return 10 * time.Minute },
	})
	socksInbound := proxy.NewSocks5Inbound(proxy.Socks5InboundConfig{
		ProxyToken: "token", Router: router, Pool: matrixNode.pool, Health: matrixNode.pool,
	})
	manager := newEndpointRuntimeManager(
		"127.0.0.1", "token", nil, nil, nil, nil, socksInbound, nil,
	)
	port := reserveDualTransportPort(t)
	endpoint := model.Endpoint{
		ID: "protocol-matrix", Port: port, Enabled: true,
		AllowProxy: true, AllowSOCKS5: true, RequireProxyAuthInfo: true,
	}
	if err := manager.ApplyEndpoint(endpoint); err != nil {
		t.Fatalf("apply protocol matrix endpoint: %v", err)
	}
	manager.Start()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		closeProtocolMatrixResource(t, "protocol matrix endpoint", func() error {
			return manager.Shutdown(ctx)
		})
	})
	return port
}

func assertProtocolMatrixUDPExchange(
	t *testing.T,
	protocol string,
	endpointPort int,
	client *net.UDPConn,
	target string,
) {
	t.Helper()
	payload := []byte("real sing-box UDP matrix: " + protocol)
	packet := encodeSOCKS5UDPTestPacket(t, target, payload)
	proxyAddress := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: endpointPort}
	if _, err := client.WriteToUDP(packet, proxyAddress); err != nil {
		t.Fatalf("write %s SOCKS5 UDP packet: %v", protocol, err)
	}
	if err := client.SetReadDeadline(time.Now().Add(protocolMatrixUDPTimeout)); err != nil {
		t.Fatalf("set %s UDP response deadline: %v", protocol, err)
	}
	response := make([]byte, 65535)
	n, _, err := client.ReadFromUDP(response)
	if err != nil {
		t.Fatalf("read %s SOCKS5 UDP response: %v", protocol, err)
	}
	if got := decodeSOCKS5UDPTestPayload(t, response[:n]); !bytes.Equal(got, payload) {
		t.Fatalf("%s UDP response payload: got %q, want %q", protocol, got, payload)
	}
}

func TestRealSingboxTCPOnlyOutboundExcludedFromUDPView(t *testing.T) {
	builder := newProtocolMatrixBuilder(t)
	rawOutbound := json.RawMessage(`{
		"type":"http","tag":"matrix-tcp-only","server":"127.0.0.1","server_port":1
	}`)
	matrixNode, manager := newProtocolMatrixNode(t, rawOutbound, builder)
	t.Cleanup(func() { closeProtocolMatrixManager(t, manager) })
	entry := requireProtocolMatrixEntry(t, matrixNode.pool, matrixNode.hash)
	if entry.UDPHealth() != node.UDPHealthUnsupported {
		t.Fatalf("TCP-only outbound UDP health: got %q, want %q", entry.UDPHealth(), node.UDPHealthUnsupported)
	}
	if matrixNode.platform.NetworkView(N.NetworkUDP).Contains(matrixNode.hash) {
		t.Fatal("TCP-only outbound was admitted to the UDP routing view")
	}
	if !matrixNode.platform.NetworkView(N.NetworkTCP).Contains(matrixNode.hash) {
		t.Fatal("TCP-only outbound was not admitted to the TCP routing view")
	}
}

func TestRealSingboxWireGuardRestrictedMatrix(t *testing.T) {
	t.Skip("WireGuard requires TUN/network-administration permissions and is excluded from the unprivileged matrix")
}

func marshalProtocolMatrixJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal protocol matrix JSON: %v", err)
	}
	return data
}

func closeProtocolMatrixManager(t *testing.T, manager *outbound.OutboundManager) {
	t.Helper()
	closeProtocolMatrixResource(t, "outbound manager", manager.Close)
}

func closeProtocolMatrixResource(t *testing.T, name string, closeFn func() error) {
	t.Helper()
	if err := closeFn(); err != nil && !errors.Is(err, os.ErrClosed) {
		t.Errorf("close %s: %v", name, err)
	}
}
