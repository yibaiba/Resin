package proxy

import (
	"bufio"
	"context"
	"net"
	"time"

	"github.com/Resinat/Resin/internal/outbound"
	"github.com/Resinat/Resin/internal/routing"
)

var socks5HandshakeTimeout = 15 * time.Second

const defaultSocks5UDPIdleTimeout = 5 * time.Minute

// Socks5InboundConfig holds dependencies for the SOCKS5 inbound handler.
type Socks5InboundConfig struct {
	ProxyToken       string
	Router           *routing.Router
	Pool             outbound.PoolAccessor
	Health           HealthRecorder
	Events           EventEmitter
	MetricsSink      MetricsEventSink
	ProxyBypassRules []string
	Decisioner       RouteDecisioner
	UDPIdleTimeout   time.Duration
	UDPDirectDialer  UDPDirectPacketDialer
}

// Socks5Inbound implements SOCKS5 CONNECT and authenticated UDP ASSOCIATE.
type Socks5Inbound struct {
	token           string
	tunnel          tunnelDeps
	events          EventEmitter
	udpAssociations *socks5UDPAssociationManager
	udpDirectDialer UDPDirectPacketDialer
}

type socks5Session struct {
	ctx       context.Context
	conn      net.Conn
	reader    *bufio.Reader
	handshake socks5HandshakeResult
}

// NewSocks5Inbound creates a new SOCKS5 inbound handler.
func NewSocks5Inbound(cfg Socks5InboundConfig) *Socks5Inbound {
	ev := cfg.Events
	if ev == nil {
		ev = NoOpEventEmitter{}
	}
	udpIdleTimeout := cfg.UDPIdleTimeout
	if udpIdleTimeout <= 0 {
		udpIdleTimeout = defaultSocks5UDPIdleTimeout
	}
	decisioner := cfg.Decisioner
	if decisioner == nil {
		decisioner = newCompatibilityDecisioner(cfg.ProxyBypassRules)
	}
	return &Socks5Inbound{
		token: cfg.ProxyToken,
		tunnel: tunnelDeps{
			router:      cfg.Router,
			pool:        cfg.Pool,
			decisioner:  decisioner,
			health:      cfg.Health,
			metricsSink: cfg.MetricsSink,
		},
		events:          ev,
		udpAssociations: newSocks5UDPAssociationManager(udpIdleTimeout),
		udpDirectDialer: resolveUDPDirectPacketDialer(cfg.UDPDirectDialer),
	}
}

func resolveUDPDirectPacketDialer(configured UDPDirectPacketDialer) UDPDirectPacketDialer {
	if configured != nil {
		return configured
	}
	return netUDPDirectPacketDialer{}
}

// ServeConn handles a SOCKS5 session on an already-accepted TCP connection.
func (s *Socks5Inbound) ServeConn(conn net.Conn) {
	s.ServeConnContext(context.Background(), conn)
}

// ServeConnContext handles a SOCKS5 session with a caller-provided base context.
func (s *Socks5Inbound) ServeConnContext(baseCtx context.Context, conn net.Conn) {
	if conn == nil {
		return
	}
	defer conn.Close()
	if baseCtx == nil {
		baseCtx = context.Background()
	}
	reader := bufio.NewReader(conn)

	handshakeCtx, cancelHandshake := context.WithTimeout(baseCtx, socks5HandshakeTimeout)
	defer cancelHandshake()
	handshakePhase := startSocks5HandshakePhase(handshakeCtx, conn)
	defer handshakePhase.Stop()

	requireAuthInfo := InboundPolicyFromContext(baseCtx).RequireProxyAuthInfo && s.token == ""
	handshake := s.performHandshake(conn, reader, requireAuthInfo)
	if !handshake.ok {
		return
	}
	handshakePhase.Stop()
	session := socks5Session{
		ctx: baseCtx, conn: conn, reader: reader, handshake: handshake,
	}
	switch handshake.request.command {
	case socks5CommandConnect:
		s.serveConnect(session)
	case socks5CommandUDPAssociate:
		s.serveUDPAssociate(session)
	}
}
