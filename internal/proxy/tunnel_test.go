package proxy

import (
	"errors"
	"fmt"
	"io"
	"net"
	"syscall"
	"testing"
	"time"
)

func TestClassifyTunnelHealth_UsesErrorOwnershipAndFirstByte(t *testing.T) {
	networkErr := errors.New("network failure")
	tests := []struct {
		name    string
		ingress tunnelDirectionResult
		egress  tunnelDirectionResult
		want    passiveHealthOutcome
	}{
		{name: "upstream read before first byte", ingress: tunnelDirectionResult{operation: tunnelIORead, err: networkErr}, want: passiveHealthFailure},
		{name: "client write before first byte", ingress: tunnelDirectionResult{operation: tunnelIOWrite, err: networkErr}, want: passiveHealthNeutral},
		{name: "upstream write before first byte", egress: tunnelDirectionResult{operation: tunnelIOWrite, err: networkErr}, want: passiveHealthFailure},
		{name: "client read before first byte", egress: tunnelDirectionResult{operation: tunnelIORead, err: networkErr}, want: passiveHealthNeutral},
		{name: "first upstream byte wins", ingress: tunnelDirectionResult{bytes: 1, operation: tunnelIORead, err: networkErr}, want: passiveHealthSuccess},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := classifyTunnelHealth(test.ingress, test.egress); got != test.want {
				t.Fatalf("outcome = %d, want %d", got, test.want)
			}
		})
	}
}

func TestTunnelRelayResult_PreservesDirectionalTerminationDetails(t *testing.T) {
	ingressErr := fmt.Errorf("upstream read failed\nwith newline: %w", syscall.ECONNRESET)
	ingress := finalizeTunnelDirectionResult(tunnelDirectionResult{
		direction: tunnelDirectionIngress, bytes: 7, endOrder: 2, operation: tunnelIORead, err: ingressErr,
	})
	egress := finalizeTunnelDirectionResult(tunnelDirectionResult{
		direction: tunnelDirectionEgress, bytes: 11, endOrder: 1, operation: tunnelIORead, err: io.EOF,
	})
	result := buildTunnelRelayResult(ingress, egress, tunnelPumpOptions{})

	if result.firstEnded != tunnelDirectionEgress {
		t.Fatalf("first ended = %q, want %q", result.firstEnded, tunnelDirectionEgress)
	}
	if result.ingress.bytes != 7 || result.egress.bytes != 11 {
		t.Fatalf("direction bytes: ingress=%d egress=%d", result.ingress.bytes, result.egress.bytes)
	}
	if result.ingress.operation != tunnelIORead || result.ingress.errorKind != "connection_reset" ||
		result.ingress.errno != "ECONNRESET" {
		t.Fatalf("ingress detail: %+v", result.ingress)
	}
	if result.ingress.errorMessage != "upstream read failed with newline: connection reset by peer" {
		t.Fatalf("sanitized ingress message = %q", result.ingress.errorMessage)
	}
	if result.egress.errorKind != "eof" || result.egress.endOrder != 1 {
		t.Fatalf("egress detail: %+v", result.egress)
	}
}

func TestTunnelRelayResult_CloseWriteFailureIsRequestFailureOnly(t *testing.T) {
	ingress := finalizeTunnelDirectionResult(tunnelDirectionResult{
		direction: tunnelDirectionIngress, bytes: 7, endOrder: 1,
		operation: tunnelIORead, err: io.EOF, closeWriteErr: syscall.EPIPE,
	})
	egress := finalizeTunnelDirectionResult(tunnelDirectionResult{
		direction: tunnelDirectionEgress, bytes: 11, endOrder: 2,
		operation: tunnelIORead, err: io.EOF,
	})
	result := buildTunnelRelayResult(ingress, egress, tunnelPumpOptions{})

	if result.netOK || result.upstreamStage != "connect_client_close_write" {
		t.Fatalf("request result: net_ok=%v stage=%q", result.netOK, result.upstreamStage)
	}
	if !errors.Is(result.upstreamErr, syscall.EPIPE) {
		t.Fatalf("upstream error = %v, want EPIPE", result.upstreamErr)
	}
	if result.healthOutcome != passiveHealthSuccess {
		t.Fatalf("health outcome = %d, want success", result.healthOutcome)
	}
	if ingress.closeWriteErrorKind != "broken_pipe" || ingress.closeWriteErrno != "EPIPE" {
		t.Fatalf("close-write detail: %+v", ingress)
	}
}

func TestPumpPreparedTunnelReader_FallsBackToFullCloseWhenHalfCloseUnavailable(t *testing.T) {
	clientBase, clientPeer := net.Pipe()
	upstreamBase, upstreamPeer := net.Pipe()
	defer clientPeer.Close()
	defer upstreamPeer.Close()

	clientConn := &connCloseNotifier{
		Conn: clientBase,
		sink: newCountingConnTestSink(),
	}
	upstreamConn := newTLSLatencyConn(newCountingConn(upstreamBase, newCountingConnTestSink()), nil)

	clientPayloadDone := make(chan []byte, 1)
	go func() {
		data, _ := io.ReadAll(clientPeer)
		clientPayloadDone <- data
	}()

	done := make(chan struct{})
	go func() {
		_ = pumpPreparedTunnelReader(
			clientConn,
			clientConn,
			&preparedTunnel{
				upstreamConn: upstreamConn,
			},
			tunnelPumpOptions{},
		)
		close(done)
	}()

	go func() {
		_, _ = upstreamPeer.Write([]byte("server-push"))
		_ = upstreamPeer.Close()
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("pumpPreparedTunnelReader should fall back to full close when CloseWrite is unavailable")
	}

	select {
	case payload := <-clientPayloadDone:
		if string(payload) != "server-push" {
			t.Fatalf("client payload: got %q, want %q", string(payload), "server-push")
		}
	case <-time.After(time.Second):
		t.Fatal("expected client peer to receive upstream payload and EOF")
	}
}

func TestPumpPreparedTunnelReader_ClientReadResetAfterIngressDoesNotFail(t *testing.T) {
	clientLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen client side: %v", err)
	}
	defer clientLn.Close()

	clientAccepted := make(chan net.Conn, 1)
	go func() {
		conn, acceptErr := clientLn.Accept()
		if acceptErr != nil {
			clientAccepted <- nil
			return
		}
		clientAccepted <- conn
	}()

	clientConn, err := net.Dial("tcp", clientLn.Addr().String())
	if err != nil {
		t.Fatalf("dial client side: %v", err)
	}
	clientTCP, ok := clientConn.(*net.TCPConn)
	if !ok {
		t.Fatalf("client conn type: got %T, want *net.TCPConn", clientConn)
	}
	defer clientTCP.Close()

	proxyClientConn := <-clientAccepted
	if proxyClientConn == nil {
		t.Fatal("accept client side failed")
	}
	defer proxyClientConn.Close()

	upstreamLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen upstream side: %v", err)
	}
	defer upstreamLn.Close()

	upstreamAccepted := make(chan net.Conn, 1)
	go func() {
		conn, acceptErr := upstreamLn.Accept()
		if acceptErr != nil {
			upstreamAccepted <- nil
			return
		}
		upstreamAccepted <- conn
	}()

	upstreamPeer, err := net.Dial("tcp", upstreamLn.Addr().String())
	if err != nil {
		t.Fatalf("dial upstream side: %v", err)
	}
	defer upstreamPeer.Close()

	proxyUpstreamConn := <-upstreamAccepted
	if proxyUpstreamConn == nil {
		t.Fatal("accept upstream side failed")
	}
	defer proxyUpstreamConn.Close()

	resultCh := make(chan tunnelRelayResult, 1)
	go func() {
		resultCh <- pumpPreparedTunnelReader(
			proxyClientConn,
			proxyClientConn,
			&preparedTunnel{
				upstreamConn: proxyUpstreamConn,
			},
			tunnelPumpOptions{},
		)
	}()

	const request = "client-hello"
	const response = "server-reply"
	upstreamDone := make(chan error, 1)
	go func() {
		defer close(upstreamDone)
		buf := make([]byte, len(request))
		if _, err := io.ReadFull(upstreamPeer, buf); err != nil {
			upstreamDone <- err
			return
		}
		if string(buf) != request {
			upstreamDone <- io.ErrUnexpectedEOF
			return
		}
		if _, err := upstreamPeer.Write([]byte(response)); err != nil {
			upstreamDone <- err
			return
		}
		_, _ = io.Copy(io.Discard, upstreamPeer)
		upstreamDone <- nil
	}()

	if _, err := clientTCP.Write([]byte(request)); err != nil {
		t.Fatalf("write client request: %v", err)
	}

	respBuf := make([]byte, len(response))
	if _, err := io.ReadFull(clientTCP, respBuf); err != nil {
		t.Fatalf("read client response: %v", err)
	}
	if string(respBuf) != response {
		t.Fatalf("response: got %q, want %q", string(respBuf), response)
	}

	if err := clientTCP.SetLinger(0); err != nil {
		t.Fatalf("set linger: %v", err)
	}
	if err := clientTCP.Close(); err != nil {
		t.Fatalf("close client with reset: %v", err)
	}

	select {
	case result := <-resultCh:
		if !result.netOK {
			t.Fatalf("netOK: got false, want true (stage=%q err=%v)", result.upstreamStage, result.upstreamErr)
		}
		if result.proxyErr != nil {
			t.Fatalf("proxyErr: got %+v, want nil", result.proxyErr)
		}
		if result.upstreamStage != "" {
			t.Fatalf("upstreamStage: got %q, want empty", result.upstreamStage)
		}
		if result.ingressBytes != int64(len(response)) {
			t.Fatalf("ingressBytes: got %d, want %d", result.ingressBytes, len(response))
		}
		if result.egressBytes != int64(len(request)) {
			t.Fatalf("egressBytes: got %d, want %d", result.egressBytes, len(request))
		}
	case <-time.After(2 * time.Second):
		t.Fatal("expected tunnel relay result")
	}

	select {
	case err := <-upstreamDone:
		if err != nil {
			t.Fatalf("upstream side failed: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("expected upstream side to finish")
	}
}
