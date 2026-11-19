# sing-box v1.12.21 Reality ML-KEM patch

Upstream sing-box strips `X25519MLKEM768` from every Reality ClientHello. Xray-core 26.9.8+ (and 3x-ui inbounds that require it) then treats the handshake as unauthorized.

These two files overlay `github.com/sagernet/sing-box@v1.12.21` via `scripts/prepare-sing-box.sh`:

- `option/tls.go` adds `support_x25519mlkem768` (default false).
- `common/tls/reality_client.go` skips the strip only when that field is true.

Nodes without the flag keep the upstream strip, so existing Reality outbounds stay unchanged.
