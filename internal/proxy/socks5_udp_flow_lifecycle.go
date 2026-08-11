package proxy

import (
	"errors"
	"io"
	"log"
	"net"
	"time"

	"github.com/sagernet/sing/common/bufio"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

var errSocks5UDPFlowClosed = errors.New("SOCKS5 UDP flow is closed")

func (f *socks5UDPRelayFlow) start() {
	if !f.touchIdleTimeout() {
		return
	}
	go f.readResponses()
}

func (f *socks5UDPRelayFlow) writePayload(payload []byte) error {
	if !f.touchIdleTimeout() {
		return errSocks5UDPFlowClosed
	}
	if !f.association.touch() {
		return errSocks5UDPAssociationNotFound
	}
	f.writeMu.Lock()
	n, err := writeUDPRelayPacket(f.packetConn, payload, f.upstream)
	if err == nil && n != len(payload) {
		err = io.ErrShortWrite
	}
	if err == nil {
		f.recoveryGeneration = f.association.beginRecoveryGeneration()
	}
	f.writeMu.Unlock()
	if n > 0 {
		f.association.recordTraffic(0, int64(n))
		if f.metricsSink != nil {
			f.metricsSink.OnTrafficDelta(0, int64(n))
		}
	}
	if err != nil {
		if !f.closed.Load() {
			f.fail("udp_upstream_write", err, true)
		}
		return err
	}
	return nil
}

func writeUDPRelayPacket(conn net.PacketConn, payload []byte, destination net.Addr) (int, error) {
	if writer, ok := conn.(N.NetPacketWriter); ok {
		return bufio.WritePacket(writer, payload, destination)
	}
	return conn.WriteTo(payload, destination)
}

func (f *socks5UDPRelayFlow) readResponses() {
	buffer := make([]byte, socks5UDPMaxDatagramSize)
	for {
		n, source, err := f.packetConn.ReadFrom(buffer)
		if err != nil {
			if !f.closed.Load() {
				f.fail("udp_upstream_read", err, true)
			}
			return
		}
		if err := f.relayResponse(buffer[:n], source); err != nil {
			if f.isRetiringOrClosed() {
				return
			}
			f.fail("udp_client_write", err, false)
			return
		}
	}
}

func (f *socks5UDPRelayFlow) relayResponse(
	payload []byte,
	source net.Addr,
) error {
	if !f.touchIdleTimeout() {
		return errSocks5UDPFlowClosed
	}
	if !f.association.touch() {
		return errSocks5UDPAssociationNotFound
	}
	target := f.target
	if sourceAddress := M.SocksaddrFromNet(source); sourceAddress.IsValid() {
		target = sourceAddress.String()
	}
	packet, err := encodeSocks5UDPDatagram(target, payload)
	if err != nil {
		return err
	}
	clientSource := f.association.sourceAddr()
	if !clientSource.IsValid() {
		return errSocks5UDPInvalidAssociationSource
	}
	if _, err := f.client.writeTo(packet, net.UDPAddrFromAddrPort(clientSource)); err != nil {
		return err
	}
	f.recordResponse(len(payload))
	return nil
}

func (f *socks5UDPRelayFlow) recordResponse(payloadLength int) {
	f.association.recordTraffic(int64(payloadLength), 0)
	f.writeMu.Lock()
	recoveryGeneration := f.recoveryGeneration
	f.writeMu.Unlock()
	if recoveryGeneration != 0 {
		f.association.recordFlowSuccess(recoveryGeneration)
	}
	if f.metricsSink != nil {
		f.metricsSink.OnTrafficDelta(int64(payloadLength), 0)
	}
	f.healthOKOnce.Do(func() {
		if f.health != nil && f.route.PlatformID != "" {
			recordPassiveUDPResultAsync(f.health, f.route, true)
		}
	})
}

func (f *socks5UDPRelayFlow) touchIdleTimeout() bool {
	f.idleMu.Lock()
	defer f.idleMu.Unlock()
	if f.closed.Load() || f.retiring {
		return false
	}
	f.lastActivity = time.Now()
	if f.idleTimer == nil {
		f.idleTimer = time.AfterFunc(f.idleTimeout, f.expireIfIdle)
	} else {
		f.idleTimer.Reset(f.idleTimeout)
	}
	return true
}

func (f *socks5UDPRelayFlow) expireIfIdle() {
	f.idleMu.Lock()
	if f.closed.Load() || f.retiring {
		f.idleMu.Unlock()
		return
	}
	remaining := f.idleTimeout - time.Since(f.lastActivity)
	if remaining > 0 {
		f.idleTimer.Reset(remaining)
		f.idleMu.Unlock()
		return
	}
	f.retiring = true
	f.idleMu.Unlock()
	_ = f.association.removeFlow(f.target, f)
}

func (f *socks5UDPRelayFlow) isRetiringOrClosed() bool {
	f.idleMu.Lock()
	defer f.idleMu.Unlock()
	return f.retiring || f.closed.Load()
}

func (f *socks5UDPRelayFlow) fail(stage string, err error, nodeFailure bool) {
	f.failureOnce.Do(func() {
		log.Printf("SOCKS5 UDP flow failed stage=%s target=%q error=%v", stage, f.target, err)
		proxyErr := classifyUpstreamError(err)
		if proxyErr != nil {
			f.association.recordFailure(stage, err, proxyErr)
		}
		if nodeFailure && proxyErr != nil && f.health != nil && f.route.PlatformID != "" {
			recordPassiveUDPResultAsync(f.health, f.route, false)
		}
		_ = f.association.removeFlow(f.target, f)
	})
}

func (f *socks5UDPRelayFlow) Close() error {
	var closeErr error
	f.closeOnce.Do(func() {
		f.closed.Store(true)
		f.idleMu.Lock()
		if f.idleTimer != nil {
			f.idleTimer.Stop()
		}
		f.idleMu.Unlock()
		closeErr = f.packetConn.Close()
		if f.metricsSink != nil {
			f.metricsSink.OnConnectionLifecycle(ConnectionOutbound, ConnectionClose)
		}
	})
	return closeErr
}
