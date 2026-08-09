package proxy

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"
)

type blockingUDPResolver struct {
	started chan struct{}
}

type staticUDPResolver struct {
	addresses []net.IPAddr
}

func (r staticUDPResolver) LookupIPAddr(
	context.Context,
	string,
) ([]net.IPAddr, error) {
	return r.addresses, nil
}

func (r *blockingUDPResolver) LookupIPAddr(
	ctx context.Context,
	_ string,
) ([]net.IPAddr, error) {
	close(r.started)
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestUDPDirectPacketDialerCancelsTargetResolution(t *testing.T) {
	resolver := &blockingUDPResolver{started: make(chan struct{})}
	dialer := netUDPDirectPacketDialer{resolver: resolver}
	ctx, cancel := context.WithCancel(context.Background())
	type listenResult struct {
		packetConn  net.PacketConn
		destination net.Addr
		err         error
	}
	resultCh := make(chan listenResult, 1)
	go func() {
		packetConn, destination, err := dialer.ListenPacket(ctx, "blocked.invalid:53")
		resultCh <- listenResult{
			packetConn: packetConn, destination: destination, err: err,
		}
	}()

	select {
	case <-resolver.started:
	case <-time.After(time.Second):
		t.Fatal("UDP target resolution did not start")
	}
	cancel()
	select {
	case result := <-resultCh:
		if result.packetConn != nil || result.destination != nil {
			t.Fatalf("canceled resolution returned resources: %+v", result)
		}
		if !errors.Is(result.err, context.Canceled) {
			t.Fatalf("canceled resolution error: got %v", result.err)
		}
	case <-time.After(time.Second):
		t.Fatal("UDP target resolution ignored context cancellation")
	}
}

func TestUDPDirectPacketDialerPrefersIPv4AndSupportsIPv6OnlyDomains(t *testing.T) {
	tests := []struct {
		name      string
		addresses []net.IPAddr
		wantIP    string
	}{
		{
			name: "dual stack",
			addresses: []net.IPAddr{
				{IP: net.ParseIP("::1")},
				{IP: net.IPv4(127, 0, 0, 1)},
			},
			wantIP: "127.0.0.1",
		},
		{
			name:      "IPv6 only",
			addresses: []net.IPAddr{{IP: net.ParseIP("2001:db8::1")}},
			wantIP:    "2001:db8::1",
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			dialer := netUDPDirectPacketDialer{
				resolver: staticUDPResolver{addresses: testCase.addresses},
			}
			destination, err := dialer.resolveTarget(context.Background(), "example.test:5353")
			if err != nil {
				t.Fatalf("resolve UDP target: %v", err)
			}
			if got := destination.IP.String(); got != testCase.wantIP {
				t.Fatalf("resolved IP: got %q, want %q", got, testCase.wantIP)
			}
			if destination.Port != 5353 {
				t.Fatalf("resolved port: got %d, want 5353", destination.Port)
			}
		})
	}
}
