package proxy

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"

	M "github.com/sagernet/sing/common/metadata"
)

type socks5UDPInboundPacket struct {
	ctx              context.Context
	associationScope string
	client           *socks5UDPClientWriter
	source           net.Addr
	packet           []byte
}

type socks5UDPRelayDatagram struct {
	ctx      context.Context
	client   *socks5UDPClientWriter
	source   net.Addr
	datagram socks5UDPDatagram
}

// ServePacketConnContext relays SOCKS5 UDP datagrams for one endpoint runtime.
// associationScope must also be used when registering the TCP control session.
func (s *Socks5Inbound) ServePacketConnContext(
	ctx context.Context,
	associationScope string,
	packetConn net.PacketConn,
) error {
	if s == nil || s.udpAssociations == nil {
		return errors.New("SOCKS5 UDP inbound is not initialized")
	}
	if associationScope == "" {
		return errors.New("SOCKS5 UDP association scope is empty")
	}
	if packetConn == nil {
		return errors.New("SOCKS5 UDP packet connection is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	defer s.udpAssociations.closeEndpoint(associationScope)
	client := &socks5UDPClientWriter{packetConn: packetConn}
	buffer := make([]byte, socks5UDPMaxDatagramSize)
	for {
		n, source, err := packetConn.ReadFrom(buffer)
		if err != nil {
			return fmt.Errorf("read SOCKS5 UDP datagram: %w", err)
		}
		s.handleUDPDatagram(socks5UDPInboundPacket{
			ctx:              ctx,
			associationScope: associationScope,
			client:           client,
			source:           source,
			packet:           buffer[:n],
		})
	}
}

func (s *Socks5Inbound) handleUDPDatagram(packet socks5UDPInboundPacket) {
	datagram, err := decodeSocks5UDPDatagram(packet.packet)
	if err != nil {
		log.Printf("SOCKS5 UDP datagram rejected source=%q reason=%v", packet.source, err)
		return
	}
	sourceAddr := normalizeAddrPort(M.AddrPortFromNet(packet.source))
	association, err := s.udpAssociations.match(packet.associationScope, sourceAddr)
	if err != nil {
		log.Printf("SOCKS5 UDP datagram rejected source=%q reason=%v", packet.source, err)
		return
	}
	if current, ok := association.flow(datagram.target); ok {
		if err := current.flow.writePayload(datagram.payload); err != nil {
			log.Printf("SOCKS5 UDP datagram relay failed source=%q target=%q error=%v", packet.source, datagram.target, err)
		}
		return
	}
	s.createAndWriteUDPFlow(association, socks5UDPRelayDatagram{
		ctx: packet.ctx, client: packet.client, source: packet.source, datagram: datagram,
	})
}

func (s *Socks5Inbound) createAndWriteUDPFlow(
	association *socks5UDPAssociation,
	relay socks5UDPRelayDatagram,
) {
	created := s.newUDPRelayFlow(socks5UDPFlowRequest{
		ctx: relay.ctx, association: association, client: relay.client, target: relay.datagram.target,
	})
	if created.flow == nil {
		log.Printf(
			"SOCKS5 UDP flow creation failed source=%q target=%q stage=%s error=%v",
			relay.source,
			relay.datagram.target,
			created.stage,
			created.err,
		)
		association.recordRoute(relay.datagram.target, created.route)
		if created.proxyErr != nil {
			association.recordFailure(created.stage, created.err, created.proxyErr)
		}
		return
	}
	association.recordRoute(relay.datagram.target, created.route)
	ref, installed, err := association.installFlow(relay.datagram.target, created.flow)
	if err != nil {
		_ = created.flow.Close()
		log.Printf("SOCKS5 UDP flow install failed source=%q target=%q error=%v", relay.source, relay.datagram.target, err)
		return
	}
	if !installed {
		_ = created.flow.Close()
	} else {
		created.flow.start()
	}
	if err := ref.flow.writePayload(relay.datagram.payload); err != nil {
		log.Printf("SOCKS5 UDP datagram relay failed source=%q target=%q error=%v", relay.source, relay.datagram.target, err)
	}
}

// CloseUDPAssociations closes all authenticated UDP sessions and their flows.
func (s *Socks5Inbound) CloseUDPAssociations() error {
	if s == nil || s.udpAssociations == nil {
		return nil
	}
	return s.udpAssociations.close()
}

// CloseUDPAssociationScope closes sessions owned by one endpoint runtime.
func (s *Socks5Inbound) CloseUDPAssociationScope(associationScope string) error {
	if s == nil || s.udpAssociations == nil {
		return nil
	}
	return s.udpAssociations.closeEndpoint(associationScope)
}

// OpenUDPAssociationScope allows a new endpoint generation to register sessions.
func (s *Socks5Inbound) OpenUDPAssociationScope(associationScope string) error {
	if s == nil || s.udpAssociations == nil {
		return errors.New("SOCKS5 UDP inbound is not initialized")
	}
	return s.udpAssociations.openEndpoint(associationScope)
}

// RetainUDPAssociationScope protects an endpoint generation while a TCP handler may register UDP.
func (s *Socks5Inbound) RetainUDPAssociationScope(
	associationScope string,
) (func(), error) {
	if s == nil || s.udpAssociations == nil {
		return nil, errors.New("SOCKS5 UDP inbound is not initialized")
	}
	return s.udpAssociations.retainEndpoint(associationScope)
}
