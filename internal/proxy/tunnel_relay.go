package proxy

import (
	"bufio"
	"bytes"
	"io"
	"net"
	"sync"
	"sync/atomic"
)

type tunnelDirection string

const (
	tunnelDirectionIngress tunnelDirection = "upstream_to_client"
	tunnelDirectionEgress  tunnelDirection = "client_to_upstream"
	tunnelCopyBufferSize                   = 32 * 1024
)

type tunnelIOOperation string

const (
	tunnelIORead  tunnelIOOperation = "read"
	tunnelIOWrite tunnelIOOperation = "write"
)

type tunnelDirectionResult struct {
	direction    tunnelDirection
	bytes        int64
	endOrder     uint32
	operation    tunnelIOOperation
	err          error
	errorKind    string
	errno        string
	errorMessage string
}

type tunnelRelayResult struct {
	ingressBytes  int64
	egressBytes   int64
	firstEnded    tunnelDirection
	ingress       tunnelDirectionResult
	egress        tunnelDirectionResult
	netOK         bool
	healthOutcome passiveHealthOutcome
	proxyErr      *ProxyError
	upstreamStage string
	upstreamErr   error
}

type tunnelPumpOptions struct {
	requireBidirectionalTraffic bool
	onFirstIngressByte          func()
}

type tunnelDirectionPump struct {
	dst         net.Conn
	src         io.Reader
	direction   tunnelDirection
	onFirstByte func()
	endOrder    *atomic.Uint32
	closeBoth   func()
	results     chan<- tunnelDirectionResult
}

func pumpPreparedTunnel(
	clientConn net.Conn,
	clientReader *bufio.Reader,
	session *preparedTunnel,
	opts tunnelPumpOptions,
) tunnelRelayResult {
	clientToUpstream, err := makeTunnelClientReader(clientConn, clientReader)
	if err != nil {
		closeTunnel(clientConn, session)
		if session != nil {
			session.health.record(passiveHealthNeutral)
		}
		return tunnelRelayResult{
			proxyErr:      ErrUpstreamRequestFailed,
			upstreamStage: "connect_client_prefetch_drain",
			upstreamErr:   err,
		}
	}
	return pumpPreparedTunnelReader(clientConn, clientToUpstream, session, opts)
}

func pumpPreparedTunnelReader(
	clientConn net.Conn,
	clientToUpstream io.Reader,
	session *preparedTunnel,
	opts tunnelPumpOptions,
) tunnelRelayResult {
	if clientConn == nil || clientToUpstream == nil || session == nil || session.upstreamConn == nil {
		return tunnelRelayResult{}
	}

	var closeOnce sync.Once
	closeBoth := func() {
		closeOnce.Do(func() { closeTunnel(clientConn, session) })
	}
	results := make(chan tunnelDirectionResult, 2)
	var endOrder atomic.Uint32
	go (tunnelDirectionPump{
		dst: session.upstreamConn, src: clientToUpstream, direction: tunnelDirectionEgress,
		endOrder: &endOrder, closeBoth: closeBoth, results: results,
	}).run()
	go (tunnelDirectionPump{
		dst: clientConn, src: session.upstreamConn, direction: tunnelDirectionIngress,
		onFirstByte: func() {
			session.health.record(passiveHealthSuccess)
			if opts.onFirstIngressByte != nil {
				opts.onFirstIngressByte()
			}
		},
		endOrder: &endOrder, closeBoth: closeBoth, results: results,
	}).run()

	first := <-results
	second := <-results
	closeBoth()
	result := buildTunnelRelayResult(first, second, opts)
	session.health.record(result.healthOutcome)
	return result
}

func (p tunnelDirectionPump) run() {
	n, operation, err := copyTunnelStream(p.dst, p.src, p.onFirstByte)
	order := p.endOrder.Add(1)
	if !isBenignTunnelCopyError(err) || closeWriteErr(p.dst) != nil {
		p.closeBoth()
	}
	p.results <- finalizeTunnelDirectionResult(tunnelDirectionResult{
		direction: p.direction,
		bytes:     n,
		endOrder:  order,
		operation: operation,
		err:       err,
	})
}

func finalizeTunnelDirectionResult(result tunnelDirectionResult) tunnelDirectionResult {
	detail := summarizeUpstreamError(result.err)
	result.errorKind = detail.Kind
	result.errno = detail.Errno
	result.errorMessage = detail.Message
	return result
}

func copyTunnelStream(dst io.Writer, src io.Reader, onFirstByte func()) (int64, tunnelIOOperation, error) {
	buffer := make([]byte, tunnelCopyBufferSize)
	var total int64
	var firstByteOnce sync.Once
	for {
		readBytes, readErr := src.Read(buffer)
		if readBytes > 0 {
			firstByteOnce.Do(func() {
				if onFirstByte != nil {
					onFirstByte()
				}
			})
			written, writeErr := dst.Write(buffer[:readBytes])
			total += int64(written)
			if writeErr != nil {
				return total, tunnelIOWrite, writeErr
			}
			if written != readBytes {
				return total, tunnelIOWrite, io.ErrShortWrite
			}
		}
		if readErr != nil {
			return total, tunnelIORead, readErr
		}
	}
}

func buildTunnelRelayResult(
	first tunnelDirectionResult,
	second tunnelDirectionResult,
	opts tunnelPumpOptions,
) tunnelRelayResult {
	ingress, egress := orderTunnelResults(first, second)
	result := tunnelRelayResult{
		ingressBytes:  ingress.bytes,
		egressBytes:   egress.bytes,
		firstEnded:    firstTunnelEnd(first, second),
		ingress:       ingress,
		egress:        egress,
		netOK:         true,
		healthOutcome: classifyTunnelHealth(ingress, egress),
	}
	setTunnelRequestError(&result, opts)
	return result
}

func firstTunnelEnd(first, second tunnelDirectionResult) tunnelDirection {
	if first.endOrder < second.endOrder {
		return first.direction
	}
	return second.direction
}

func orderTunnelResults(
	first tunnelDirectionResult,
	second tunnelDirectionResult,
) (tunnelDirectionResult, tunnelDirectionResult) {
	if first.direction == tunnelDirectionIngress {
		return first, second
	}
	return second, first
}

func setTunnelRequestError(result *tunnelRelayResult, opts tunnelPumpOptions) {
	ingressBenign := isBenignTunnelCopyError(result.ingress.err)
	egressBenign := isBenignTunnelCopyError(result.egress.err)
	if !egressBenign && result.ingress.bytes > 0 && isClientReadResetError(result.egress.err) {
		egressBenign = true
	}
	switch {
	case !ingressBenign:
		setTunnelCopyError(result, "connect_upstream_to_client_copy", result.ingress.err)
	case !egressBenign:
		setTunnelCopyError(result, "connect_client_to_upstream_copy", result.egress.err)
	case opts.requireBidirectionalTraffic && (result.ingress.bytes == 0 || result.egress.bytes == 0):
		setTunnelZeroTrafficError(result)
	}
}

func classifyTunnelHealth(
	ingress tunnelDirectionResult,
	egress tunnelDirectionResult,
) passiveHealthOutcome {
	if ingress.bytes > 0 {
		return passiveHealthSuccess
	}
	if !isBenignTunnelCopyError(ingress.err) && ingress.operation == tunnelIORead {
		return passiveHealthFailure
	}
	if !isBenignTunnelCopyError(egress.err) && egress.operation == tunnelIOWrite {
		return passiveHealthFailure
	}
	return passiveHealthNeutral
}

func setTunnelCopyError(result *tunnelRelayResult, stage string, err error) {
	result.netOK = false
	result.proxyErr = ErrUpstreamRequestFailed
	result.upstreamStage = stage
	result.upstreamErr = err
}

func setTunnelZeroTrafficError(result *tunnelRelayResult) {
	result.netOK = false
	result.proxyErr = ErrUpstreamRequestFailed
	switch {
	case result.ingress.bytes == 0 && result.egress.bytes == 0:
		result.upstreamStage = "connect_zero_traffic"
	case result.ingress.bytes == 0:
		result.upstreamStage = "connect_no_ingress_traffic"
	default:
		result.upstreamStage = "connect_no_egress_traffic"
	}
}

func closeTunnel(clientConn net.Conn, session *preparedTunnel) {
	if clientConn != nil {
		_ = clientConn.Close()
	}
	if session != nil && session.upstreamConn != nil {
		_ = session.upstreamConn.Close()
	}
}

// makeTunnelClientReader preserves bytes pre-read by an HTTP or SOCKS reader.
func makeTunnelClientReader(clientConn net.Conn, buffered *bufio.Reader) (io.Reader, error) {
	if buffered == nil {
		return clientConn, nil
	}
	n := buffered.Buffered()
	if n == 0 {
		return clientConn, nil
	}
	prefetched := make([]byte, n)
	if _, err := io.ReadFull(buffered, prefetched); err != nil {
		return nil, err
	}
	return io.MultiReader(bytes.NewReader(prefetched), clientConn), nil
}
