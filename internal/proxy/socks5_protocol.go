package proxy

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
)

const (
	socks5Version                 = 0x05
	socks5CommandConnect          = 0x01
	socks5CommandBind             = 0x02
	socks5CommandUDPAssociate     = 0x03
	socks5AddressTypeIPv4         = 0x01
	socks5AddressTypeDomain       = 0x03
	socks5AddressTypeIPv6         = 0x04
	socks5ReplySucceeded          = 0x00
	socks5ReplyGeneralFailure     = 0x01
	socks5ReplyConnectionDenied   = 0x02
	socks5ReplyCommandUnsupported = 0x07
	socks5ReplyAddressUnsupported = 0x08
)

var (
	errSocks5TruncatedUDPDatagram = errors.New("truncated SOCKS5 UDP datagram")
	errSocks5InvalidReserved      = errors.New("invalid SOCKS5 UDP reserved field")
	errSocks5UDPFragmented        = errors.New("SOCKS5 UDP fragmentation is unsupported")
	errSocks5AddressType          = errors.New("unsupported SOCKS5 address type")
	errSocks5InvalidAddress       = errors.New("invalid SOCKS5 address")
)

type socks5Request struct {
	command byte
	target  string
}

type socks5UDPDatagram struct {
	target  string
	payload []byte
}

func readSocks5Request(reader io.Reader) (socks5Request, byte, error) {
	header := make([]byte, 4)
	if _, err := io.ReadFull(reader, header); err != nil {
		return socks5Request{}, 0, err
	}
	if header[0] != socks5Version || header[2] != 0 {
		return socks5Request{}, socks5ReplyGeneralFailure, errSocks5InvalidAddress
	}
	target, err := readSocks5AddrPort(reader, header[3])
	if err != nil {
		if errors.Is(err, errSocks5AddressType) {
			return socks5Request{}, socks5ReplyAddressUnsupported, err
		}
		return socks5Request{}, socks5ReplyGeneralFailure, err
	}
	return socks5Request{command: header[1], target: target}, 0, nil
}

func readSocks5ConnectRequest(reader io.Reader) (string, byte, bool) {
	request, replyCode, err := readSocks5Request(reader)
	if err != nil {
		return "", replyCode, false
	}
	if request.command != socks5CommandConnect {
		return "", socks5ReplyCommandUnsupported, false
	}
	return request.target, 0, true
}

func decodeSocks5UDPDatagram(packet []byte) (socks5UDPDatagram, error) {
	if len(packet) < 4 {
		return socks5UDPDatagram{}, errSocks5TruncatedUDPDatagram
	}
	if packet[0] != 0 || packet[1] != 0 {
		return socks5UDPDatagram{}, errSocks5InvalidReserved
	}
	if packet[2] != 0 {
		return socks5UDPDatagram{}, fmt.Errorf("%w: FRAG=%d", errSocks5UDPFragmented, packet[2])
	}
	reader := bytes.NewReader(packet[4:])
	target, err := readSocks5AddrPort(reader, packet[3])
	if err != nil {
		if errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) {
			return socks5UDPDatagram{}, fmt.Errorf("%w: %v", errSocks5TruncatedUDPDatagram, err)
		}
		return socks5UDPDatagram{}, err
	}
	payload := make([]byte, reader.Len())
	_, _ = io.ReadFull(reader, payload)
	return socks5UDPDatagram{target: target, payload: payload}, nil
}

func encodeSocks5UDPDatagram(target string, payload []byte) ([]byte, error) {
	packet := []byte{0, 0, 0}
	packet, err := appendSocks5AddrPort(packet, target)
	if err != nil {
		return nil, err
	}
	return append(packet, payload...), nil
}

func readSocks5AddrPort(reader io.Reader, addressType byte) (string, error) {
	host, err := readSocks5Host(reader, addressType)
	if err != nil {
		return "", err
	}
	portBytes := make([]byte, 2)
	if _, err := io.ReadFull(reader, portBytes); err != nil {
		return "", err
	}
	port := strconv.Itoa(int(binary.BigEndian.Uint16(portBytes)))
	return net.JoinHostPort(host, port), nil
}

func readSocks5Host(reader io.Reader, addressType byte) (string, error) {
	switch addressType {
	case socks5AddressTypeIPv4:
		return readSocks5IP(reader, net.IPv4len)
	case socks5AddressTypeIPv6:
		return readSocks5IP(reader, net.IPv6len)
	case socks5AddressTypeDomain:
		return readSocks5Domain(reader)
	default:
		return "", fmt.Errorf("%w: %d", errSocks5AddressType, addressType)
	}
}

func readSocks5IP(reader io.Reader, size int) (string, error) {
	ip := make([]byte, size)
	if _, err := io.ReadFull(reader, ip); err != nil {
		return "", err
	}
	return net.IP(ip).String(), nil
}

func readSocks5Domain(reader io.Reader) (string, error) {
	size := []byte{0}
	if _, err := io.ReadFull(reader, size); err != nil {
		return "", err
	}
	if size[0] == 0 {
		return "", fmt.Errorf("%w: empty domain", errSocks5InvalidAddress)
	}
	domain := make([]byte, int(size[0]))
	if _, err := io.ReadFull(reader, domain); err != nil {
		return "", err
	}
	return string(domain), nil
}

func appendSocks5AddrPort(dst []byte, target string) ([]byte, error) {
	host, portText, err := net.SplitHostPort(target)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errSocks5InvalidAddress, err)
	}
	port, err := strconv.ParseUint(portText, 10, 16)
	if err != nil {
		return nil, fmt.Errorf("%w: port %q", errSocks5InvalidAddress, portText)
	}
	dst, err = appendSocks5Host(dst, host)
	if err != nil {
		return nil, err
	}
	return binary.BigEndian.AppendUint16(dst, uint16(port)), nil
}

func appendSocks5Host(dst []byte, host string) ([]byte, error) {
	if ip := net.ParseIP(host); ip != nil {
		if ip4 := ip.To4(); ip4 != nil {
			return append(append(dst, socks5AddressTypeIPv4), ip4...), nil
		}
		return append(append(dst, socks5AddressTypeIPv6), ip.To16()...), nil
	}
	if len(host) == 0 || len(host) > 255 {
		return nil, fmt.Errorf("%w: domain length %d", errSocks5InvalidAddress, len(host))
	}
	dst = append(dst, socks5AddressTypeDomain, byte(len(host)))
	return append(dst, host...), nil
}

func writeSocks5Reply(writer io.Writer, replyCode byte, addr net.Addr) error {
	target := "0.0.0.0:0"
	if addr != nil {
		target = addr.String()
	}
	response, err := appendSocks5AddrPort([]byte{socks5Version, replyCode, 0}, target)
	if err != nil {
		return err
	}
	_, err = writer.Write(response)
	return err
}
