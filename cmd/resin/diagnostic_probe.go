package main

import (
	"context"
	"errors"
	"net"

	"github.com/Resinat/Resin/internal/node"
	"github.com/Resinat/Resin/internal/topology"
	M "github.com/sagernet/sing/common/metadata"
)

type runtimeDiagnosticTCPProber struct {
	pool *topology.GlobalNodePool
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
	target string,
) error {
	entry, ok := p.pool.GetEntry(hash)
	if !ok {
		return errors.New("selected node is no longer available")
	}
	outbound := entry.Outbound.Load()
	if outbound == nil {
		return errors.New("selected node outbound is not ready")
	}
	conn, err := (*outbound).DialContext(ctx, "tcp", M.ParseSocksaddr(target))
	if err != nil {
		return err
	}
	_ = conn.Close()
	return nil
}
