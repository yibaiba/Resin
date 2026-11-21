package probe

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"log"
	"net"
	"net/netip"
	"time"

	"github.com/Resinat/Resin/internal/config"
	"github.com/Resinat/Resin/internal/node"
	"github.com/miekg/dns"
	"github.com/sagernet/sing-box/adapter"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

const udpProbeQuestion = "example.com."

func isDualStack(entry *node.NodeEntry) bool {
	return entry != nil &&
		entry.SupportsNetwork(N.NetworkTCP) &&
		entry.SupportsNetwork(N.NetworkUDP)
}

func (m *ProbeManager) udpProbeReady() bool {
	return m != nil && m.pool != nil && m.udpProbeTarget != nil && m.timeout != nil
}

func (m *ProbeManager) probeUDP(hash node.Hash, entry *node.NodeEntry) {
	if !m.udpProbeReady() || !isDualStack(entry) {
		return
	}
	if m.onProbeEvent != nil {
		m.onProbeEvent("udp")
	}

	err := m.performUDPProbe(hash, m.udpProbeTarget())
	if errors.Is(err, context.Canceled) || isSkippedProbe(err) {
		return
	}
	if err != nil {
		m.noteChainProbeFailure(hash, N.NetworkUDP)
	}
	m.pool.RecordUDPProbeResult(hash, err == nil)
	if err != nil {
		m.logUDPProbeFailure(hash, err)
	}
}

func (m *ProbeManager) logUDPProbeFailure(hash node.Hash, err error) {
	select {
	case <-m.stopCh:
		return
	default:
	}
	log.Printf("[probe] UDP probe failed for %s: %v", hash.Hex(), err)
}

func (m *ProbeManager) performUDPProbe(hash node.Hash, rawTarget string) error {
	target, err := config.ParseUDPProbeTarget(rawTarget)
	if err != nil {
		return fmt.Errorf("parse target: %w", err)
	}
	timeout := m.timeout()
	if timeout <= 0 {
		return fmt.Errorf("invalid timeout %v", timeout)
	}
	var ob adapter.Outbound
	if m.outbound != nil {
		resolved, err := m.outbound(hash)
		if err != nil {
			return err
		}
		ob = resolved
	} else {
		entry, ok := m.pool.GetEntry(hash)
		if !ok {
			return fmt.Errorf("node not found")
		}
		outbound := entry.Outbound.Load()
		if outbound == nil {
			return fmt.Errorf("node outbound not ready")
		}
		ob = *outbound
	}

	ctx, cancel := context.WithTimeout(m.ctx, timeout)
	defer cancel()
	packetConn, err := ob.ListenPacket(ctx, M.SocksaddrFromNetIP(target))
	if err != nil {
		return fmt.Errorf("listen packet: %w", err)
	}
	defer packetConn.Close()
	stopDeadline := armUDPProbeConn(ctx, packetConn)
	defer stopDeadline()

	queryID, query, err := buildUDPProbeQuery()
	if err != nil {
		return err
	}
	if _, err := packetConn.WriteTo(query, net.UDPAddrFromAddrPort(target)); err != nil {
		return fmt.Errorf("write query: %w", err)
	}

	buffer := make([]byte, dns.MaxMsgSize)
	read, source, err := packetConn.ReadFrom(buffer)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		return fmt.Errorf("read response: %w", err)
	}
	if !sameAddrPort(M.AddrPortFromNet(source), target) {
		return fmt.Errorf("unexpected response source %s", source)
	}
	return validateUDPProbeResponse(buffer[:read], queryID)
}

func buildUDPProbeQuery() (uint16, []byte, error) {
	var randomID [2]byte
	if _, err := rand.Read(randomID[:]); err != nil {
		return 0, nil, fmt.Errorf("generate query ID: %w", err)
	}
	id := binary.BigEndian.Uint16(randomID[:])
	query := new(dns.Msg)
	query.SetQuestion(udpProbeQuestion, dns.TypeA)
	query.Id = id
	wire, err := query.Pack()
	if err != nil {
		return 0, nil, fmt.Errorf("pack query: %w", err)
	}
	return id, wire, nil
}

func validateUDPProbeResponse(payload []byte, queryID uint16) error {
	response := new(dns.Msg)
	if err := response.Unpack(payload); err != nil {
		return fmt.Errorf("unpack response: %w", err)
	}
	if response.Id != queryID {
		return fmt.Errorf("transaction ID mismatch")
	}
	if !response.Response {
		return fmt.Errorf("response bit not set")
	}
	if response.Truncated {
		return fmt.Errorf("truncated response")
	}
	if response.Opcode != dns.OpcodeQuery || len(response.Question) != 1 {
		return fmt.Errorf("unexpected response question structure")
	}
	question := response.Question[0]
	if question.Name != udpProbeQuestion || question.Qtype != dns.TypeA || question.Qclass != dns.ClassINET {
		return fmt.Errorf("response question mismatch")
	}
	return nil
}

// armUDPProbeConn bounds a probe with SetReadDeadline. Hysteria2's PacketConn
// rejects SetDeadline (os.ErrInvalid); SetReadDeadline is implemented.
// If neither deadline method works, timeout closes the conn to unblock ReadFrom.
func armUDPProbeConn(ctx context.Context, packetConn net.PacketConn) func() bool {
	stop := context.AfterFunc(ctx, func() {
		interruptUDPProbeConn(packetConn)
	})
	if deadline, ok := ctx.Deadline(); ok {
		_ = setUDPProbeReadDeadline(packetConn, deadline)
	}
	return stop
}

func setUDPProbeReadDeadline(packetConn net.PacketConn, deadline time.Time) error {
	if err := packetConn.SetReadDeadline(deadline); err == nil {
		return nil
	}
	return packetConn.SetDeadline(deadline)
}

func interruptUDPProbeConn(packetConn net.PacketConn) {
	if setUDPProbeReadDeadline(packetConn, time.Now()) == nil {
		return
	}
	_ = packetConn.Close()
}

func sameAddrPort(left, right netip.AddrPort) bool {
	if !left.IsValid() || !right.IsValid() {
		return false
	}
	left = netip.AddrPortFrom(left.Addr().Unmap(), left.Port())
	right = netip.AddrPortFrom(right.Addr().Unmap(), right.Port())
	return left == right
}
