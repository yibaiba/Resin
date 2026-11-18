package outbound

import (
	"context"
	"encoding/json"
	"net"
	"sync"
	"testing"

	"github.com/Resinat/Resin/internal/node"
	"github.com/sagernet/sing-box/adapter"
	M "github.com/sagernet/sing/common/metadata"
)

func TestStripOutboundDetourRemovesDetourKeepsTag(t *testing.T) {
	raw := json.RawMessage(`{"type":"socks","tag":"node","server":"127.0.0.1","server_port":1,"detour":"clash-hop"}`)
	stripped, err := stripOutboundDetour(raw)
	if err != nil {
		t.Fatalf("strip: %v", err)
	}
	var object map[string]any
	if err := json.Unmarshal(stripped, &object); err != nil {
		t.Fatal(err)
	}
	if object["tag"] != "node" {
		t.Fatalf("tag = %v", object["tag"])
	}
	if _, ok := object["detour"]; ok {
		t.Fatalf("detour survived strip: %v", object["detour"])
	}

	unchanged := json.RawMessage(`{"type":"direct","tag":"plain"}`)
	got, err := stripOutboundDetour(unchanged)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(unchanged) {
		t.Fatalf("strip without detour rewrote json: %s", got)
	}
}

func TestBuildStripsInheritedDetour(t *testing.T) {
	b := newTestSingboxBuilder(t)
	defer b.Close()

	ob, err := b.Build(json.RawMessage(`{"type":"socks","tag":"imported","server":"127.0.0.1","server_port":1,"version":"5","detour":"clash-hop"}`))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if deps := ob.Dependencies(); len(deps) != 0 {
		t.Fatalf("direct build kept detour dependencies %v", deps)
	}
}

func TestWithDialerOverrideSetsTagAndDetour(t *testing.T) {
	raw := json.RawMessage(`{"type":"direct","tag":"old","detour":"clash-hop"}`)

	first, err := withDialerOverride(raw, "resin-chain-h0-via-l", "")
	if err != nil {
		t.Fatalf("first hop override: %v", err)
	}
	var firstObj map[string]any
	if err := json.Unmarshal(first, &firstObj); err != nil {
		t.Fatal(err)
	}
	if firstObj["tag"] != "resin-chain-h0-via-l" {
		t.Fatalf("first hop tag = %v", firstObj["tag"])
	}
	if _, ok := firstObj["detour"]; ok {
		t.Fatalf("first hop kept inherited detour: %v", firstObj["detour"])
	}

	landing, err := withDialerOverride(raw, "resin-chain-h0-l", "resin-chain-h0-via-l")
	if err != nil {
		t.Fatalf("landing override: %v", err)
	}
	var landingObj map[string]any
	if err := json.Unmarshal(landing, &landingObj); err != nil {
		t.Fatal(err)
	}
	if landingObj["tag"] != "resin-chain-h0-l" || landingObj["detour"] != "resin-chain-h0-via-l" {
		t.Fatalf("landing override = %v", landingObj)
	}
}

func TestBuildChainedRegistersDetourTags(t *testing.T) {
	b := newTestSingboxBuilder(t)
	defer b.Close()

	if got := b.outboundMgr.Default(); got == nil || got.Tag() != chainDirectPlaceholderTag {
		t.Fatalf("default outbound = %v, want %s", got, chainDirectPlaceholderTag)
	}

	hopJSON := json.RawMessage(`{"type":"socks","server":"127.0.0.1","server_port":1,"version":"5"}`)
	landJSON := json.RawMessage(`{"type":"socks","server":"127.0.0.1","server_port":2,"version":"5"}`)
	hopHash := node.HashFromRawOptions(hopJSON)
	landHash := node.HashFromRawOptions(landJSON)
	ob, tags, err := b.BuildChained(
		[]ChainedHop{{Hash: hopHash, Raw: hopJSON}},
		landJSON,
		landHash,
	)
	if err != nil {
		t.Fatalf("BuildChained: %v", err)
	}
	if ob == nil || len(tags) != 2 {
		t.Fatalf("chained outbound tags = %v", tags)
	}
	if tags[0] != chainHopTag([]node.Hash{hopHash}, landHash, 0) {
		t.Fatalf("hop tag = %s", tags[0])
	}
	if tags[1] != chainLandingTag([]node.Hash{hopHash}, landHash) {
		t.Fatalf("landing tag = %s", tags[1])
	}
	if _, ok := b.outboundMgr.Outbound(tags[0]); !ok {
		t.Fatal("hop tag missing from sing-box outbound manager")
	}
	if created, ok := b.outboundMgr.Outbound(tags[1]); !ok || created != ob {
		t.Fatal("landing tag missing from sing-box outbound manager")
	}
	if got := b.outboundMgr.Default(); got == nil || got.Tag() != chainDirectPlaceholderTag {
		t.Fatalf("chain create replaced default outbound: %v", got)
	}

	b.ReleaseChain(tags)
	if _, ok := b.outboundMgr.Outbound(tags[0]); ok {
		t.Fatal("hop tag survived ReleaseChain")
	}
	if _, ok := b.outboundMgr.Outbound(tags[1]); ok {
		t.Fatal("landing tag survived ReleaseChain")
	}
}

func TestBuildChainedDoesNotReplaceLiveTags(t *testing.T) {
	b := newTestSingboxBuilder(t)
	defer b.Close()

	hopJSON := json.RawMessage(`{"type":"socks","server":"127.0.0.1","server_port":11,"version":"5"}`)
	landJSON := json.RawMessage(`{"type":"socks","server":"127.0.0.1","server_port":12,"version":"5"}`)
	hopHash := node.HashFromRawOptions(hopJSON)
	landHash := node.HashFromRawOptions(landJSON)
	first, tags, err := b.BuildChained(
		[]ChainedHop{{Hash: hopHash, Raw: hopJSON}},
		landJSON,
		landHash,
	)
	if err != nil {
		t.Fatalf("first BuildChained: %v", err)
	}
	_, _, err = b.BuildChained(
		[]ChainedHop{{Hash: hopHash, Raw: hopJSON}},
		landJSON,
		landHash,
	)
	if err == nil {
		t.Fatal("second BuildChained replaced live tags")
	}
	if got, ok := b.outboundMgr.Outbound(tags[1]); !ok || got != first {
		t.Fatal("live landing outbound was replaced")
	}
	b.ReleaseChain(tags)
}

type recordingDialOutbound struct {
	tag    string
	server string
	detour adapter.Outbound
	seq    *[]string
	mu     *sync.Mutex
}

func (o *recordingDialOutbound) Type() string      { return "recording" }
func (o *recordingDialOutbound) Tag() string       { return o.tag }
func (o *recordingDialOutbound) Network() []string { return []string{"tcp"} }
func (o *recordingDialOutbound) Dependencies() []string {
	if o.detour == nil {
		return nil
	}
	return []string{o.detour.Tag()}
}
func (o *recordingDialOutbound) Close() error { return nil }

func (o *recordingDialOutbound) DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
	o.mu.Lock()
	*o.seq = append(*o.seq, o.tag+":"+destination.String())
	o.mu.Unlock()
	if o.detour != nil {
		return o.detour.DialContext(ctx, network, M.ParseSocksaddrHostPort(o.server, 443))
	}
	left, right := net.Pipe()
	_ = right.Close()
	return left, nil
}

func (o *recordingDialOutbound) ListenPacket(context.Context, M.Socksaddr) (net.PacketConn, error) {
	return nil, net.ErrClosed
}

func TestChainedDialOrderHopThenLandingServer(t *testing.T) {
	var (
		seq []string
		mu  sync.Mutex
	)
	hop := &recordingDialOutbound{tag: "hop", seq: &seq, mu: &mu}
	landing := &recordingDialOutbound{
		tag:    "landing",
		server: "landing.example.com",
		detour: hop,
		seq:    &seq,
		mu:     &mu,
	}

	conn, err := landing.DialContext(context.Background(), "tcp", M.ParseSocksaddrHostPort("dest.example.com", 443))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	_ = conn.Close()

	mu.Lock()
	got := append([]string(nil), seq...)
	mu.Unlock()
	want := []string{"landing:dest.example.com:443", "hop:landing.example.com:443"}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("dial order = %v, want %v", got, want)
	}
}
