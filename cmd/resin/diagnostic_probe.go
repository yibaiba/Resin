package main

import (
	"context"
	"errors"
	"net"

	"github.com/Resinat/Resin/internal/node"
	"github.com/Resinat/Resin/internal/outbound"
	"github.com/Resinat/Resin/internal/topology"
	"github.com/sagernet/sing-box/adapter"
	M "github.com/sagernet/sing/common/metadata"
)

type runtimeDiagnosticTCPProber struct {
	pool        *topology.GlobalNodePool
	outboundMgr *outbound.OutboundManager
}

func (p runtimeDiagnosticTCPProber) ProbeDirect(ctx context.Context, target string) error {
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", target)
	if err != nil {
		return err
	}
	_ = conn.Close()
	return nil
}

func (p runtimeDiagnosticTCPProber) ProbeRouted(
	ctx context.Context,
	hash node.Hash,
	hops []node.Hash,
	target string,
) error {
	ob, err := p.routedDiagnosticOutbound(hash, hops)
	if err != nil {
		return err
	}
	conn, err := ob.DialContext(ctx, "tcp", M.ParseSocksaddr(target))
	if err != nil {
		return err
	}
	_ = conn.Close()
	return nil
}

func (p runtimeDiagnosticTCPProber) routedDiagnosticOutbound(hash node.Hash, hops []node.Hash) (adapter.Outbound, error) {
	if len(hops) > 0 {
		if p.outboundMgr == nil {
			return nil, errors.New("chained outbound manager is not ready")
		}
		return p.outboundMgr.EnsureChainedOutbound(hops, hash)
	}
	entry, ok := p.pool.GetEntry(hash)
	if !ok {
		return nil, errors.New("selected node is no longer available")
	}
	outboundPtr := entry.Outbound.Load()
	if outboundPtr == nil {
		return nil, errors.New("selected node outbound is not ready")
	}
	return *outboundPtr, nil
}
