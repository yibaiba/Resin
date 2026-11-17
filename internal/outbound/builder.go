package outbound

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/adapter/endpoint"
	"github.com/sagernet/sing-box/adapter/inbound"
	sbOutbound "github.com/sagernet/sing-box/adapter/outbound"
	"github.com/sagernet/sing-box/dns"
	"github.com/sagernet/sing-box/include"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common"
	sJson "github.com/sagernet/sing/common/json"
	"github.com/sagernet/sing/service"
)

// OutboundBuilder creates outbound instances from raw node options.
type OutboundBuilder interface {
	Build(rawOptions json.RawMessage) (adapter.Outbound, error)
}

// SingboxBuilderConfig configures SingboxBuilder construction.
type SingboxBuilderConfig struct {
	// DNSUpstreams configures Resin's node DNS chain.
	// Values are DNS upstream URI strings and the slice must not be empty.
	DNSUpstreams []string
	// SingboxLogLevel accepts disabled, error, warn, info, debug, or trace.
	// An empty value uses warn for callers created before logging was exposed.
	SingboxLogLevel string
}

// ---------------------------------------------------------------------------
// SingboxBuilder — creates real sing-box adapter.Outbound instances.
// ---------------------------------------------------------------------------

// SingboxBuilder builds real sing-box outbound instances from raw JSON options.
// It holds a fully-wired context with DNS services so that domain-based
// outbound servers can be resolved.
type SingboxBuilder struct {
	registry            *sbOutbound.Registry
	ctx                 context.Context
	logFactory          log.Factory
	dnsTransportManager *dns.TransportManager
	dnsRouter           *dns.Router
	dnsLogger           log.ContextLogger
	dnsMu               sync.Mutex
	dnsURIToTag         map[string]string
	dnsNextIndex        int
	dnsFailover         *secureDNSFailoverTransport
	closeOnce           sync.Once
	closeErr            error
}

// NewSingboxBuilderWithConfig creates a SingboxBuilder with a complete
// sing-box service graph (registries + DNS). The caller must call Close()
// when done.
func NewSingboxBuilderWithConfig(cfg SingboxBuilderConfig) (*SingboxBuilder, error) {
	return newSingboxBuilderWithLogFactory(cfg, log.New)
}

func newSingboxBuilderWithLogFactory(cfg SingboxBuilderConfig, newFactory logFactoryConstructor) (*SingboxBuilder, error) {
	ctx := context.Background()
	ctx = include.Context(ctx) // inject protocol registries

	logFactory, err := newResinSingboxLogFactory(ctx, cfg.SingboxLogLevel, newFactory)
	if err != nil {
		return nil, fmt.Errorf("singbox builder: create log factory: %w", err)
	}
	if err := logFactory.Start(); err != nil {
		return nil, errors.Join(
			fmt.Errorf("singbox builder: start log factory: %w", err),
			logFactory.Close(),
		)
	}

	builder := &SingboxBuilder{ctx: ctx, logFactory: logFactory}
	if err := builder.initialize(cfg.DNSUpstreams); err != nil {
		return nil, errors.Join(err, builder.Close())
	}
	return builder, nil
}

func (b *SingboxBuilder) initialize(dnsUpstreams []string) error {
	logger := b.logFactory.NewLogger("resin-outbound")

	dnsRegistry, ok := service.FromContext[adapter.DNSTransportRegistry](b.ctx).(*dns.TransportRegistry)
	if !ok {
		return fmt.Errorf("singbox builder: unexpected DNS transport registry type %T", service.FromContext[adapter.DNSTransportRegistry](b.ctx))
	}
	registerSecureDNSTransport(dnsRegistry)

	// --- Service graph (same order as Demos/simple-proxy/main.go) -----------

	// Endpoint Manager
	endpointMgr := endpoint.NewManager(logger, service.FromContext[adapter.EndpointRegistry](b.ctx))
	service.MustRegister[adapter.EndpointManager](b.ctx, endpointMgr)

	// Inbound Manager (required dependency even though unused)
	inboundMgr := inbound.NewManager(logger, service.FromContext[adapter.InboundRegistry](b.ctx), endpointMgr)
	service.MustRegister[adapter.InboundManager](b.ctx, inboundMgr)

	// Outbound Manager (sing-box's own manager, for detour resolution)
	outboundMgr := sbOutbound.NewManager(logger, service.FromContext[adapter.OutboundRegistry](b.ctx), endpointMgr, "")
	service.MustRegister[adapter.OutboundManager](b.ctx, outboundMgr)

	// DNS Transport Manager
	dnsTransportMgr := dns.NewTransportManager(logger, service.FromContext[adapter.DNSTransportRegistry](b.ctx), outboundMgr, secureDNSFailoverTransportTag)
	service.MustRegister[adapter.DNSTransportManager](b.ctx, dnsTransportMgr)

	// DNS Router
	dnsRouter := dns.NewRouter(b.ctx, b.logFactory, option.DNSOptions{})
	service.MustRegister[adapter.DNSRouter](b.ctx, dnsRouter)
	b.dnsLogger = logger
	b.dnsTransportManager = dnsTransportMgr
	b.dnsRouter = dnsRouter

	normalizedUpstreams, err := NormalizeDNSUpstreams(dnsUpstreams)
	if err != nil {
		return fmt.Errorf("singbox builder: configure DNS transports: %w", err)
	}
	dnsTransportSpecs, err := secureDNSTransportSpecsForUpstreams(normalizedUpstreams)
	if err != nil {
		return fmt.Errorf("singbox builder: configure DNS transports: %w", err)
	}
	for _, spec := range dnsTransportSpecs {
		if err := dnsTransportMgr.Create(b.ctx, logger, spec.tag, spec.transportType, spec.options); err != nil {
			return fmt.Errorf("singbox builder: create DNS transport %s[%s]: %w", spec.transportType, spec.tag, err)
		}
	}

	// Start DNS Transport Manager lifecycle
	if err := dnsTransportMgr.Start(adapter.StartStateInitialize); err != nil {
		return fmt.Errorf("singbox builder: initialize DNS transport manager: %w", err)
	}
	if err := dnsTransportMgr.Start(adapter.StartStateStart); err != nil {
		return fmt.Errorf("singbox builder: start DNS transport manager: %w", err)
	}

	// Start DNS Router lifecycle
	if err := dnsRouter.Initialize(nil); err != nil {
		return fmt.Errorf("singbox builder: initialize DNS router: %w", err)
	}
	if err := dnsRouter.Start(adapter.StartStateStart); err != nil {
		return fmt.Errorf("singbox builder: start DNS router: %w", err)
	}

	if err := b.captureDNSRuntime(normalizedUpstreams); err != nil {
		return err
	}

	b.registry = service.FromContext[adapter.OutboundRegistry](b.ctx).(*sbOutbound.Registry)
	return nil
}

func (b *SingboxBuilder) captureDNSRuntime(upstreams []string) error {
	b.dnsMu.Lock()
	defer b.dnsMu.Unlock()

	b.dnsURIToTag = make(map[string]string, len(upstreams)+1)
	b.dnsNextIndex = len(upstreams)
	for i, raw := range upstreams {
		id := dnsUpstreamIdentity(raw)
		if id == localDNSTransportTag {
			b.dnsURIToTag[id] = localDNSTransportTag
			continue
		}
		b.dnsURIToTag[id] = customDNSUpstreamTransportTag(i)
	}
	if _, ok := b.dnsTransportManager.Transport(localDNSTransportTag); ok {
		b.dnsURIToTag[localDNSTransportTag] = localDNSTransportTag
	}

	transport, ok := b.dnsTransportManager.Transport(secureDNSFailoverTransportTag)
	if !ok || transport == nil {
		return fmt.Errorf("singbox builder: missing secure DNS failover transport")
	}
	failover, ok := transport.(*secureDNSFailoverTransport)
	if !ok {
		return fmt.Errorf("singbox builder: unexpected failover transport type %T", transport)
	}
	b.dnsFailover = failover
	return nil
}

// SetDNSUpstreams hot-swaps the selected node DNS upstreams without rebuilding
// the sing-box service graph. Previously registered transports are kept so
// toggling a catalog upstream back on does not recreate it.
func (b *SingboxBuilder) SetDNSUpstreams(upstreams []string) error {
	normalized, err := NormalizeDNSUpstreams(upstreams)
	if err != nil {
		return err
	}

	b.dnsMu.Lock()
	defer b.dnsMu.Unlock()
	if b.dnsFailover == nil || b.dnsTransportManager == nil {
		return fmt.Errorf("singbox builder: DNS runtime is not initialized")
	}

	tags := make([]string, 0, len(normalized))
	for _, uri := range normalized {
		tag, err := b.ensureDNSTransportLocked(uri)
		if err != nil {
			return err
		}
		tags = append(tags, tag)
	}
	b.dnsFailover.SetUpstreamTags(tags)
	if b.dnsRouter != nil {
		b.dnsRouter.ClearCache()
	}
	return nil
}

func (b *SingboxBuilder) ensureDNSTransportLocked(uri string) (string, error) {
	id := dnsUpstreamIdentity(uri)
	if tag, ok := b.dnsURIToTag[id]; ok {
		return tag, nil
	}
	if b.dnsLogger == nil {
		return "", fmt.Errorf("singbox builder: missing DNS logger")
	}
	if id == localDNSTransportTag {
		spec := localDNSTransportSpec()
		if err := b.dnsTransportManager.Create(b.ctx, b.dnsLogger, spec.tag, spec.transportType, spec.options); err != nil {
			return "", fmt.Errorf("create DNS transport %s[%s]: %w", spec.transportType, spec.tag, err)
		}
		b.dnsURIToTag[id] = localDNSTransportTag
		return localDNSTransportTag, nil
	}

	tag := customDNSUpstreamTransportTag(b.dnsNextIndex)
	spec, needsLocal, err := parseCustomDNSUpstream(uri, tag)
	if err != nil {
		return "", err
	}
	if needsLocal {
		if _, err := b.ensureDNSTransportLocked(localDNSTransportTag); err != nil {
			return "", err
		}
	}
	if err := b.dnsTransportManager.Create(b.ctx, b.dnsLogger, spec.tag, spec.transportType, spec.options); err != nil {
		return "", fmt.Errorf("create DNS transport %s[%s]: %w", spec.transportType, spec.tag, err)
	}
	b.dnsNextIndex++
	b.dnsURIToTag[id] = tag
	return tag, nil
}

// Build parses rawOptions (a complete sing-box outbound JSON object with
// type/tag fields) into a real adapter.Outbound and runs it through the
// lifecycle stages.
func (b *SingboxBuilder) Build(rawOptions json.RawMessage) (adapter.Outbound, error) {
	// 1. Parse via official option.Outbound path (strips type/tag, creates
	//    typed options via OutboundOptionsRegistry + badjson.UnmarshallExcluded).
	var outboundConfig option.Outbound
	if err := sJson.UnmarshalContext(b.ctx, rawOptions, &outboundConfig); err != nil {
		return nil, fmt.Errorf("parse outbound options: %w", err)
	}

	// 2. Create the outbound instance via the registry.
	logger := b.logFactory.NewLogger(outboundLogTag(outboundConfig.Type, outboundConfig.Tag))
	ob, err := b.registry.CreateOutbound(
		b.ctx,
		nil, // router — not needed for simple dialing
		logger,
		outboundConfig.Tag,
		outboundConfig.Type,
		outboundConfig.Options,
	)
	if err != nil {
		return nil, fmt.Errorf("create outbound [%s]: %w", outboundConfig.Type, err)
	}

	// 3. Run lifecycle start stages. On failure, close and return error.
	for _, stage := range adapter.ListStartStages {
		if err := adapter.LegacyStart(ob, stage); err != nil {
			_ = common.Close(ob)
			return nil, fmt.Errorf("outbound start %s [%s]: %w", stage, outboundConfig.Type, err)
		}
	}

	return ob, nil
}

// Close shuts down the builder's internal DNS services.
func (b *SingboxBuilder) Close() error {
	b.closeOnce.Do(func() {
		var errs []error
		if b.dnsRouter != nil {
			errs = append(errs, b.dnsRouter.Close())
		}
		if b.dnsTransportManager != nil {
			errs = append(errs, b.dnsTransportManager.Close())
		}
		if b.logFactory != nil {
			errs = append(errs, b.logFactory.Close())
		}
		b.closeErr = errors.Join(errs...)
	})
	return b.closeErr
}
