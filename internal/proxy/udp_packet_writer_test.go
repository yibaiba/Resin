package proxy

import (
	"bytes"
	"net"
	"testing"

	"github.com/sagernet/sing/common/buf"
	M "github.com/sagernet/sing/common/metadata"
)

type extendedRelayPacketWriter struct {
	net.PacketConn
	payload []byte
	target  M.Socksaddr
}

func (w *extendedRelayPacketWriter) WriteTo(payload []byte, _ net.Addr) (int, error) {
	return len(payload) + 16, nil
}

func (w *extendedRelayPacketWriter) WritePacket(buffer *buf.Buffer, target M.Socksaddr) error {
	defer buffer.Release()
	w.payload = append([]byte(nil), buffer.Bytes()...)
	w.target = target
	return nil
}

func TestWriteUDPRelayPacketNormalizesExtendedWriterLength(t *testing.T) {
	writer := &extendedRelayPacketWriter{}
	payload := []byte("payload")
	target := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 5353}

	written, err := writeUDPRelayPacket(writer, payload, target)
	if err != nil {
		t.Fatalf("writeUDPRelayPacket: %v", err)
	}
	if written != len(payload) {
		t.Fatalf("written bytes: got %d, want %d", written, len(payload))
	}
	if !bytes.Equal(writer.payload, payload) {
		t.Fatalf("extended payload: got %q, want %q", writer.payload, payload)
	}
	if normalizeAddrPort(writer.target.AddrPort()) != normalizeAddrPort(target.AddrPort()) {
		t.Fatalf("extended target: got %q, want %q", writer.target, target)
	}
}
