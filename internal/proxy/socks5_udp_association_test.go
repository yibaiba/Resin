package proxy

import (
	"errors"
	"net/netip"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type associationTestCloser struct {
	closes atomic.Int32
}

func (c *associationTestCloser) Close() error {
	c.closes.Add(1)
	return nil
}

func (c *associationTestCloser) writePayload([]byte) error {
	return nil
}

func registerExactAssociation(
	t *testing.T,
	manager *socks5UDPAssociationManager,
	endpointID string,
	clientIP netip.Addr,
	port uint16,
	account string,
) *socks5UDPAssociation {
	t.Helper()
	association, err := manager.register(socks5UDPAssociationOptions{
		endpointID:      endpointID,
		clientIP:        clientIP,
		requestedSource: netip.AddrPortFrom(netip.IPv4Unspecified(), port),
		platformName:    "platform",
		account:         account,
	})
	if err != nil {
		t.Fatalf("register exact association: %v", err)
	}
	return association
}

func registerPendingAssociation(
	t *testing.T,
	manager *socks5UDPAssociationManager,
	endpointID string,
	clientIP netip.Addr,
	account string,
) (*socks5UDPAssociation, error) {
	t.Helper()
	return manager.register(socks5UDPAssociationOptions{
		endpointID:      endpointID,
		clientIP:        clientIP,
		requestedSource: netip.AddrPortFrom(netip.IPv4Unspecified(), 0),
		platformName:    "platform",
		account:         account,
	})
}

func TestSocks5UDPAssociationExactSourceIsolation(t *testing.T) {
	manager := newSocks5UDPAssociationManager(time.Minute)
	defer manager.close()
	clientIP := netip.MustParseAddr("198.51.100.1")
	association := registerExactAssociation(t, manager, "endpoint-a", clientIP, 41000, "account-a")

	matched, err := manager.match("endpoint-a", netip.MustParseAddrPort("198.51.100.1:41000"))
	if err != nil || matched != association {
		t.Fatalf("exact match: association=%p err=%v", matched, err)
	}
	if matched.platformName != "platform" || matched.account != "account-a" {
		t.Fatalf("identity not preserved: platform=%q account=%q", matched.platformName, matched.account)
	}

	unknownSources := []struct {
		endpoint string
		source   netip.AddrPort
	}{
		{endpoint: "endpoint-b", source: netip.MustParseAddrPort("198.51.100.1:41000")},
		{endpoint: "endpoint-a", source: netip.MustParseAddrPort("198.51.100.2:41000")},
		{endpoint: "endpoint-a", source: netip.MustParseAddrPort("198.51.100.1:41001")},
	}
	for _, unknown := range unknownSources {
		if _, err := manager.match(unknown.endpoint, unknown.source); !errors.Is(err, errSocks5UDPAssociationNotFound) {
			t.Fatalf("unknown source %s/%s: got %v", unknown.endpoint, unknown.source, err)
		}
	}
}

func TestSocks5UDPAssociationPendingFirstPacketBinding(t *testing.T) {
	manager := newSocks5UDPAssociationManager(time.Minute)
	defer manager.close()
	clientIP := netip.MustParseAddr("198.51.100.3")
	first, err := registerPendingAssociation(t, manager, "endpoint", clientIP, "account-a")
	if err != nil {
		t.Fatalf("register pending: %v", err)
	}
	if _, err := manager.match("endpoint", netip.MustParseAddrPort("198.51.100.4:42000")); !errors.Is(err, errSocks5UDPAssociationNotFound) {
		t.Fatalf("different IP should not bind pending association: %v", err)
	}

	firstSource := netip.MustParseAddrPort("198.51.100.3:42000")
	matched, err := manager.match("endpoint", firstSource)
	if err != nil || matched != first || first.sourceAddr() != firstSource {
		t.Fatalf("first-packet bind: matched=%p source=%s err=%v", matched, first.sourceAddr(), err)
	}

	second, err := registerPendingAssociation(t, manager, "endpoint", clientIP, "account-b")
	if err != nil {
		t.Fatalf("register pending after first bind: %v", err)
	}
	secondSource := netip.MustParseAddrPort("198.51.100.3:42001")
	matched, err = manager.match("endpoint", secondSource)
	if err != nil || matched != second {
		t.Fatalf("bind second NAT account: matched=%p err=%v", matched, err)
	}
	matched, err = manager.match("endpoint", firstSource)
	if err != nil || matched != first {
		t.Fatalf("first association lost after second bind: matched=%p err=%v", matched, err)
	}
}

func TestSocks5UDPAssociationRejectsConcurrentPendingRegistration(t *testing.T) {
	manager := newSocks5UDPAssociationManager(time.Minute)
	defer manager.close()
	clientIP := netip.MustParseAddr("198.51.100.5")

	start := make(chan struct{})
	results := make(chan error, 2)
	var wait sync.WaitGroup
	for index := 0; index < 2; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			_, err := manager.register(socks5UDPAssociationOptions{
				endpointID:      "endpoint",
				clientIP:        clientIP,
				requestedSource: netip.AddrPortFrom(netip.IPv4Unspecified(), 0),
			})
			results <- err
		}()
	}
	close(start)
	wait.Wait()
	close(results)

	successes := 0
	conflicts := 0
	for err := range results {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, errSocks5UDPPendingExists):
			conflicts++
		default:
			t.Fatalf("unexpected registration error: %v", err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("results: successes=%d conflicts=%d", successes, conflicts)
	}
}

func TestSocks5UDPAssociationValidatesRequestedSource(t *testing.T) {
	manager := newSocks5UDPAssociationManager(time.Minute)
	defer manager.close()
	clientIP := netip.MustParseAddr("198.51.100.6")
	tests := []netip.AddrPort{
		netip.MustParseAddrPort("198.51.100.7:43000"),
		netip.MustParseAddrPort("198.51.100.6:0"),
	}
	for _, requested := range tests {
		_, err := manager.register(socks5UDPAssociationOptions{
			endpointID:      "endpoint",
			clientIP:        clientIP,
			requestedSource: requested,
		})
		if !errors.Is(err, errSocks5UDPInvalidAssociationSource) {
			t.Fatalf("requested source %s: got %v", requested, err)
		}
	}
}

func TestSocks5UDPAssociationCloseCleansFlowsAndRegistration(t *testing.T) {
	manager := newSocks5UDPAssociationManager(time.Minute)
	defer manager.close()
	clientIP := netip.MustParseAddr("198.51.100.8")
	association := registerExactAssociation(t, manager, "endpoint", clientIP, 44000, "account")
	firstCloser := &associationTestCloser{}
	secondCloser := &associationTestCloser{}
	if _, installed, err := association.installFlow("one:53", firstCloser); err != nil || !installed {
		t.Fatalf("install first flow: installed=%v err=%v", installed, err)
	}
	if _, installed, err := association.installFlow("two:53", secondCloser); err != nil || !installed {
		t.Fatalf("install second flow: installed=%v err=%v", installed, err)
	}

	if err := association.Close(); err != nil {
		t.Fatalf("close association: %v", err)
	}
	if err := association.Close(); err != nil {
		t.Fatalf("close association twice: %v", err)
	}
	if firstCloser.closes.Load() != 1 || secondCloser.closes.Load() != 1 {
		t.Fatalf("flow close counts: first=%d second=%d", firstCloser.closes.Load(), secondCloser.closes.Load())
	}
	if _, err := manager.match("endpoint", netip.MustParseAddrPort("198.51.100.8:44000")); !errors.Is(err, errSocks5UDPAssociationNotFound) {
		t.Fatalf("closed association still matched: %v", err)
	}
}

func TestSocks5UDPAssociationActivityRefreshesIdleTimeout(t *testing.T) {
	const idleTimeout = 200 * time.Millisecond
	manager := newSocks5UDPAssociationManager(idleTimeout)
	defer manager.close()
	association := registerExactAssociation(
		t,
		manager,
		"endpoint",
		netip.MustParseAddr("198.51.100.9"),
		45000,
		"account",
	)

	time.Sleep(50 * time.Millisecond)
	if _, err := manager.match("endpoint", netip.MustParseAddrPort("198.51.100.9:45000")); err != nil {
		t.Fatalf("refresh activity: %v", err)
	}
	time.Sleep(80 * time.Millisecond)
	if association.closed.Load() {
		t.Fatal("association expired before refreshed idle timeout")
	}

	deadline := time.Now().Add(time.Second)
	for !association.closed.Load() && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if !association.closed.Load() {
		t.Fatal("association did not expire after idle timeout")
	}
}

func TestSocks5UDPAssociationEndpointAndManagerCleanup(t *testing.T) {
	manager := newSocks5UDPAssociationManager(time.Minute)
	clientIP := netip.MustParseAddr("198.51.100.10")
	exact := registerExactAssociation(t, manager, "endpoint-a", clientIP, 46000, "exact")
	pending, err := registerPendingAssociation(t, manager, "endpoint-a", clientIP, "pending")
	if err != nil {
		t.Fatalf("register pending: %v", err)
	}
	other := registerExactAssociation(t, manager, "endpoint-b", clientIP, 46001, "other")

	if err := manager.closeEndpoint("endpoint-a"); err != nil {
		t.Fatalf("close endpoint: %v", err)
	}
	if !exact.closed.Load() || !pending.closed.Load() || other.closed.Load() {
		t.Fatalf("endpoint cleanup states: exact=%v pending=%v other=%v", exact.closed.Load(), pending.closed.Load(), other.closed.Load())
	}
	if err := manager.close(); err != nil {
		t.Fatalf("close manager: %v", err)
	}
	if !other.closed.Load() {
		t.Fatal("manager close did not close remaining association")
	}
	_, err = manager.register(socks5UDPAssociationOptions{
		endpointID:      "endpoint-c",
		clientIP:        clientIP,
		requestedSource: netip.MustParseAddrPort("0.0.0.0:46002"),
	})
	if !errors.Is(err, errSocks5UDPAssociationManagerClosed) {
		t.Fatalf("register after close: got %v", err)
	}
}

func TestSocks5UDPAssociationConcurrentCloseFinishesLifecycleOnce(t *testing.T) {
	manager := newSocks5UDPAssociationManager(time.Minute)
	emitter := newMockEventEmitter()
	lifecycle := newRequestLifecycleFromMetadata(
		emitter,
		"198.51.100.12:47001",
		"UDP_ASSOCIATE",
		ProxyTypeSocks5Forward,
		true,
	)
	association, err := manager.register(socks5UDPAssociationOptions{
		endpointID:      "concurrent-close",
		clientIP:        netip.MustParseAddr("198.51.100.12"),
		requestedSource: netip.MustParseAddrPort("0.0.0.0:47001"),
		lifecycle:       lifecycle,
	})
	if err != nil {
		t.Fatalf("register association: %v", err)
	}

	start := make(chan struct{})
	var wait sync.WaitGroup
	for _, closeAssociation := range []func() error{
		association.Close,
		func() error { return manager.closeEndpoint("concurrent-close") },
		manager.close,
	} {
		wait.Add(1)
		go func(closeAssociation func() error) {
			defer wait.Done()
			<-start
			if err := closeAssociation(); err != nil {
				t.Errorf("close association: %v", err)
			}
		}(closeAssociation)
	}
	close(start)
	wait.Wait()

	assertSingleUDPAssociationLifecycle(t, emitter)
}

func TestSocks5UDPAssociationIdleCloseFinishesLifecycleOnce(t *testing.T) {
	const idleTimeout = time.Millisecond
	manager := newSocks5UDPAssociationManager(idleTimeout)
	emitter := newMockEventEmitter()
	lifecycle := newRequestLifecycleFromMetadata(
		emitter,
		"198.51.100.13:47002",
		"UDP_ASSOCIATE",
		ProxyTypeSocks5Forward,
		true,
	)
	association, err := manager.register(socks5UDPAssociationOptions{
		endpointID:      "idle-close",
		clientIP:        netip.MustParseAddr("198.51.100.13"),
		requestedSource: netip.MustParseAddrPort("0.0.0.0:47002"),
		lifecycle:       lifecycle,
	})
	if err != nil {
		t.Fatalf("register association: %v", err)
	}

	select {
	case entry := <-emitter.logCh:
		if entry.HTTPMethod != "UDP_ASSOCIATE" {
			t.Fatalf("request log method: got %q", entry.HTTPMethod)
		}
	case <-time.After(time.Second):
		t.Fatal("idle close did not finish lifecycle")
	}
	if err := association.Close(); err != nil {
		t.Fatalf("close expired association: %v", err)
	}
	assertNoUDPAssociationLifecycle(t, emitter)
}

func assertSingleUDPAssociationLifecycle(t *testing.T, emitter *mockEventEmitter) {
	t.Helper()
	select {
	case entry := <-emitter.logCh:
		if entry.HTTPMethod != "UDP_ASSOCIATE" {
			t.Fatalf("request log method: got %q", entry.HTTPMethod)
		}
	case <-time.After(time.Second):
		t.Fatal("association close did not finish lifecycle")
	}
	assertNoUDPAssociationLifecycle(t, emitter)
}

func assertNoUDPAssociationLifecycle(t *testing.T, emitter *mockEventEmitter) {
	t.Helper()
	select {
	case entry := <-emitter.logCh:
		t.Fatalf("association lifecycle finished more than once: %+v", entry)
	case <-time.After(20 * time.Millisecond):
	}
}

func TestSocks5UDPAssociationNormalizesIPv4MappedSource(t *testing.T) {
	manager := newSocks5UDPAssociationManager(time.Minute)
	defer manager.close()
	mapped := netip.MustParseAddr("::ffff:198.51.100.11")
	association := registerExactAssociation(t, manager, "endpoint", mapped, 47000, "account")
	matched, err := manager.match("endpoint", netip.MustParseAddrPort("198.51.100.11:47000"))
	if err != nil || matched != association {
		t.Fatalf("mapped IPv4 match: association=%p err=%v", matched, err)
	}
}
