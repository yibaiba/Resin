package proxy

import (
	"bufio"
	"context"
	"io"
	"net"
	"sync"
	"time"
)

const (
	socks5MethodNoAuth          = 0x00
	socks5MethodUserPass        = 0x02
	socks5MethodNoAcceptable    = 0xFF
	socks5UserPassVersion       = 0x01
	socks5UserPassStatusSuccess = 0x00
	socks5UserPassStatusFailure = 0x01
)

type socks5HandshakeResult struct {
	platformName string
	account      string
	request      socks5Request
	ok           bool
}

func (s *Socks5Inbound) performHandshake(
	conn net.Conn,
	reader *bufio.Reader,
	requireAuthInfo bool,
) socks5HandshakeResult {
	method, ok := s.negotiateMethod(conn, reader, requireAuthInfo)
	if !ok {
		return socks5HandshakeResult{}
	}

	result := socks5HandshakeResult{ok: true}
	if method == socks5MethodUserPass {
		var authOK bool
		result.platformName, result.account, authOK = s.authenticateUserPass(conn, reader, requireAuthInfo)
		if !authOK {
			return socks5HandshakeResult{}
		}
	}

	request, replyCode, err := readSocks5Request(reader)
	if err != nil {
		if replyCode != 0 {
			_ = writeSocks5Reply(conn, replyCode, nil)
		}
		return socks5HandshakeResult{}
	}
	if request.command != socks5CommandConnect && request.command != socks5CommandUDPAssociate {
		_ = writeSocks5Reply(conn, socks5ReplyCommandUnsupported, nil)
		return socks5HandshakeResult{}
	}
	result.request = request
	return result
}

type socks5HandshakePhase struct {
	conn     net.Conn
	stopCh   chan struct{}
	done     chan struct{}
	stopOnce sync.Once
}

func startSocks5HandshakePhase(baseCtx context.Context, conn net.Conn) *socks5HandshakePhase {
	if baseCtx == nil {
		baseCtx = context.Background()
	}
	phase := &socks5HandshakePhase{
		conn:   conn,
		stopCh: make(chan struct{}),
		done:   make(chan struct{}),
	}
	go phase.run(baseCtx)
	return phase
}

func (p *socks5HandshakePhase) run(baseCtx context.Context) {
	defer close(p.done)
	if p == nil || p.conn == nil {
		return
	}

	select {
	case <-p.stopCh:
		return
	case <-baseCtx.Done():
		_ = p.conn.SetReadDeadline(time.Now())
		_ = p.conn.SetWriteDeadline(time.Now())
	}
}

func (p *socks5HandshakePhase) Stop() {
	if p == nil {
		return
	}
	p.stopOnce.Do(func() {
		if p.stopCh != nil {
			close(p.stopCh)
		}
		if p.done != nil {
			<-p.done
		}
		if p.conn != nil {
			_ = p.conn.SetReadDeadline(time.Time{})
			_ = p.conn.SetWriteDeadline(time.Time{})
		}
	})
}

func (s *Socks5Inbound) negotiateMethod(
	conn net.Conn,
	reader *bufio.Reader,
	requireAuthInfo bool,
) (byte, bool) {
	header := make([]byte, 2)
	if _, err := io.ReadFull(reader, header); err != nil || header[0] != socks5Version {
		return 0, false
	}
	methods := make([]byte, int(header[1]))
	if _, err := io.ReadFull(reader, methods); err != nil {
		return 0, false
	}
	selected := s.selectAuthMethod(methods, requireAuthInfo)
	if _, err := conn.Write([]byte{socks5Version, selected}); err != nil {
		return 0, false
	}
	return selected, selected != socks5MethodNoAcceptable
}

func (s *Socks5Inbound) selectAuthMethod(methods []byte, requireAuthInfo bool) byte {
	if s.token != "" || requireAuthInfo {
		if containsSocks5Method(methods, socks5MethodUserPass) {
			return socks5MethodUserPass
		}
		return socks5MethodNoAcceptable
	}
	if containsSocks5Method(methods, socks5MethodUserPass) {
		return socks5MethodUserPass
	}
	if containsSocks5Method(methods, socks5MethodNoAuth) {
		return socks5MethodNoAuth
	}
	return socks5MethodNoAcceptable
}

func (s *Socks5Inbound) authenticateUserPass(
	conn net.Conn,
	reader *bufio.Reader,
	requireAuthInfo bool,
) (string, string, bool) {
	header := make([]byte, 2)
	if _, err := io.ReadFull(reader, header); err != nil {
		return "", "", false
	}
	if header[0] != socks5UserPassVersion {
		return "", "", writeSocks5AuthFailure(conn)
	}
	username := make([]byte, int(header[1]))
	if _, err := io.ReadFull(reader, username); err != nil {
		return "", "", false
	}
	password, ok := readSocks5Password(reader)
	if !ok {
		return "", "", false
	}
	if s.token != "" && string(password) != s.token {
		return "", "", writeSocks5AuthFailure(conn)
	}
	if requireAuthInfo && (len(username) == 0 || len(password) == 0) {
		return "", "", writeSocks5AuthFailure(conn)
	}
	if _, err := conn.Write([]byte{socks5UserPassVersion, socks5UserPassStatusSuccess}); err != nil {
		return "", "", false
	}
	platformName, account := parseV1PlatformAccountIdentity(string(username))
	return platformName, account, true
}

func readSocks5Password(reader io.Reader) ([]byte, bool) {
	length := []byte{0}
	if _, err := io.ReadFull(reader, length); err != nil {
		return nil, false
	}
	password := make([]byte, int(length[0]))
	if _, err := io.ReadFull(reader, password); err != nil {
		return nil, false
	}
	return password, true
}

func writeSocks5AuthFailure(conn net.Conn) bool {
	_, _ = conn.Write([]byte{socks5UserPassVersion, socks5UserPassStatusFailure})
	return false
}

func containsSocks5Method(methods []byte, candidate byte) bool {
	for _, method := range methods {
		if method == candidate {
			return true
		}
	}
	return false
}
