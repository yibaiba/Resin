package outbound

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Resinat/Resin/internal/node"
	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common"
	sJson "github.com/sagernet/sing/common/json"
)

const chainDirectPlaceholderTag = "resin-direct"

func (b *SingboxBuilder) ensureChainDirectPlaceholder() error {
	if b == nil || b.outboundMgr == nil {
		return fmt.Errorf("singbox builder: outbound manager is not initialized")
	}
	if _, loaded := b.outboundMgr.Outbound(chainDirectPlaceholderTag); loaded {
		return nil
	}
	return b.createStartedOutbound(json.RawMessage(`{"type":"direct"}`), chainDirectPlaceholderTag, "")
}

// ChainedHop is one front hop used to build a detour chain.
type ChainedHop struct {
	Hash node.Hash
	Raw  json.RawMessage
}

// ChainedOutboundBuilder builds a landing outbound whose dialer is a hop chain.
type ChainedOutboundBuilder interface {
	BuildChained(hops []ChainedHop, landing json.RawMessage, landingHash node.Hash) (adapter.Outbound, []string, error)
	ReleaseChain(tags []string)
}

func chainHopTag(hops []node.Hash, landing node.Hash, hopIndex int) string {
	var b strings.Builder
	b.WriteString("resin-chain")
	for i := 0; i <= hopIndex; i++ {
		b.WriteByte('-')
		b.WriteString(hops[i].Hex())
	}
	b.WriteString("-via-")
	b.WriteString(landing.Hex())
	return b.String()
}

func chainLandingTag(hops []node.Hash, landing node.Hash) string {
	var b strings.Builder
	b.WriteString("resin-chain")
	for _, hop := range hops {
		b.WriteByte('-')
		b.WriteString(hop.Hex())
	}
	b.WriteByte('-')
	b.WriteString(landing.Hex())
	return b.String()
}

func stripOutboundDetour(raw json.RawMessage) (json.RawMessage, error) {
	var object map[string]any
	if err := json.Unmarshal(raw, &object); err != nil {
		return nil, fmt.Errorf("parse outbound json: %w", err)
	}
	if object == nil {
		return raw, nil
	}
	if _, ok := object["detour"]; !ok {
		return raw, nil
	}
	delete(object, "detour")
	out, err := json.Marshal(object)
	if err != nil {
		return nil, fmt.Errorf("encode outbound json: %w", err)
	}
	return out, nil
}

func withDialerOverride(raw json.RawMessage, tag, detour string) (json.RawMessage, error) {
	var object map[string]any
	if err := json.Unmarshal(raw, &object); err != nil {
		return nil, fmt.Errorf("parse outbound json: %w", err)
	}
	if object == nil {
		object = make(map[string]any)
	}
	object["tag"] = tag
	if detour == "" {
		delete(object, "detour")
	} else {
		object["detour"] = detour
	}
	out, err := json.Marshal(object)
	if err != nil {
		return nil, fmt.Errorf("encode outbound json: %w", err)
	}
	return out, nil
}

func (b *SingboxBuilder) BuildChained(hops []ChainedHop, landing json.RawMessage, landingHash node.Hash) (adapter.Outbound, []string, error) {
	if b == nil || b.outboundMgr == nil || b.registry == nil {
		return nil, nil, fmt.Errorf("singbox builder: DNS/outbound runtime is not initialized")
	}
	if len(hops) == 0 {
		return nil, nil, fmt.Errorf("singbox builder: chained outbound requires at least one hop")
	}
	hopHashes := make([]node.Hash, len(hops))
	for i, hop := range hops {
		if hop.Hash.IsZero() || len(hop.Raw) == 0 {
			return nil, nil, fmt.Errorf("singbox builder: chain hop %d is empty", i+1)
		}
		hopHashes[i] = hop.Hash
	}
	if landingHash.IsZero() || len(landing) == 0 {
		return nil, nil, fmt.Errorf("singbox builder: chain landing is empty")
	}

	tags := make([]string, 0, len(hops)+1)
	var prevTag string
	for i, hop := range hops {
		tag := chainHopTag(hopHashes, landingHash, i)
		if err := b.createStartedOutbound(hop.Raw, tag, prevTag); err != nil {
			b.ReleaseChain(tags)
			return nil, nil, err
		}
		tags = append(tags, tag)
		prevTag = tag
	}
	landingTag := chainLandingTag(hopHashes, landingHash)
	if err := b.createStartedOutbound(landing, landingTag, prevTag); err != nil {
		b.ReleaseChain(tags)
		return nil, nil, err
	}
	tags = append(tags, landingTag)
	created, loaded := b.outboundMgr.Outbound(landingTag)
	if !loaded {
		b.ReleaseChain(tags)
		return nil, nil, fmt.Errorf("singbox builder: chained landing %s missing after create", landingTag)
	}
	return created, tags, nil
}

func (b *SingboxBuilder) createStartedOutbound(raw json.RawMessage, tag, detour string) error {
	if tag == chainDirectPlaceholderTag {
		if _, loaded := b.outboundMgr.Outbound(tag); loaded {
			return nil
		}
	} else if _, loaded := b.outboundMgr.Outbound(tag); loaded {
		return fmt.Errorf("create chained outbound %s: tag already registered", tag)
	}
	mutated, err := withDialerOverride(raw, tag, detour)
	if err != nil {
		return err
	}
	var outboundConfig option.Outbound
	if err := sJson.UnmarshalContext(b.ctx, mutated, &outboundConfig); err != nil {
		return fmt.Errorf("parse chained outbound %s: %w", tag, err)
	}
	logger := b.logFactory.NewLogger(outboundLogTag(outboundConfig.Type, tag))
	if err := b.outboundMgr.Create(
		b.ctx,
		nil,
		logger,
		tag,
		outboundConfig.Type,
		outboundConfig.Options,
	); err != nil {
		return fmt.Errorf("create chained outbound %s: %w", tag, err)
	}
	created, loaded := b.outboundMgr.Outbound(tag)
	if !loaded {
		return fmt.Errorf("create chained outbound %s: missing after create", tag)
	}
	for _, stage := range adapter.ListStartStages {
		if err := adapter.LegacyStart(created, stage); err != nil {
			_ = common.Close(created)
			_ = b.outboundMgr.Remove(tag)
			return fmt.Errorf("start chained outbound %s %s: %w", tag, stage, err)
		}
	}
	return nil
}

func (b *SingboxBuilder) ReleaseChain(tags []string) {
	if b == nil || b.outboundMgr == nil {
		return
	}
	for i := len(tags) - 1; i >= 0; i-- {
		tag := tags[i]
		if tag == "" {
			continue
		}
		if existing, loaded := b.outboundMgr.Outbound(tag); loaded {
			_ = common.Close(existing)
		}
		_ = b.outboundMgr.Remove(tag)
	}
}
