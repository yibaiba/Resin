# Routing Control and Diagnostics

Resin applies routing controls in this order: target rules, Platform fallback, global egress-IP policy, sticky-lease rotation, and diagnostics. HTTP forward proxy, HTTP CONNECT, reverse proxy, SOCKS5 TCP, and SOCKS5 UDP all use the same target decision engine.

## WebUI

- `/ui/routing-rules`: create, enable, reorder, and preview target rules.
- `/ui/platforms`: configure proxy chain, ordered fallbacks, and rotation on a Platform, or rotate one account lease.
- `/ui/egress-ips`: manage egress lifecycle, capacity, resume time, draining, and rotations.
- `/ui/routing-diagnostics`: run a reproducible dry-run or one real TCP connection test.

## Target Rules

`RESIN_PROXY_BYPASS` is evaluated first and produces `DIRECT`. Enabled persisted rules then run in ascending priority order, stopping on the first match. An explicitly requested Platform is preserved unless a rule sets `override_explicit_platform=true`. With no match, Resin uses the requested Platform or `Default`.

Host types are `ANY`, `EXACT`, `DOMAIN_SUFFIX`, `WILDCARD` (`*.example.com`), and `CIDR`. Empty port ranges match every port. Networks are `ANY`, `TCP`, or `UDP`; actions are `PLATFORM`, `DIRECT`, or `REJECT`.

```bash
curl -X POST http://127.0.0.1:2260/api/v1/routing-rules \
  -H "Authorization: Bearer $RESIN_ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"name":"Block private API","enabled":true,"host_type":"DOMAIN_SUFFIX","host_value":"internal.example.com","ports":[{"start":443,"end":443}],"network":"TCP","action":"REJECT","override_explicit_platform":true}'
```

`PUT /api/v1/routing-rules/order` accepts every rule ID in the desired order: `{"rule_ids":["<first-id>","<second-id>"]}`.

## Platform Fallback

`fallback_platform_ids` is ordered. Resin performs depth-first traversal from the owner Platform and uses the first Platform with eligible candidates. Fallback happens only before dialing; Resin never replays a request across Platforms after an upstream connection or request has started.

The graph must be acyclic and cannot contain self references, duplicates, or missing Platforms. A referenced Platform cannot be deleted. In a lease, `platform_id` is the owner while `selected_platform_id` is the Platform that supplied the node.

```bash
curl -X PATCH http://127.0.0.1:2260/api/v1/platforms/<owner-id> \
  -H "Authorization: Bearer $RESIN_ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"fallback_platform_ids":["<fallback-a-id>","<fallback-b-id>"]}'
```

## Platform Chain

`chain_platform_ids` is an ordered list of other Platforms used as front hops for this landing Platform. Empty means the current direct exit. One ID is A→C. `[A, B]` on C is A→B→C. Resin does not expand a hop Platform's own chain.

Traffic is a true sing-box `detour` chain: Resin dials the landing server through the hop outbound, then the landing protocol talks to the destination. Hop Platforms keep their own sticky leases, filters, circuit breakers, and fallback. A missing hop node fails the request with `NO_AVAILABLE_NODES`. A referenced hop Platform cannot be deleted.

Hop nodes are probed directly. A landing node that matches any non-empty chain is probed through that chain. If hops cannot be picked, the probe is skipped (it is never dialed directly from Resin). Hop selection excludes the landing node itself; with no other hop, probes skip and user traffic returns `NO_AVAILABLE_NODES`. Landing UDP probes use UDP-capable hops only. A chained user UDP listen failure or TCP/HTTP dial failure does not trip the landing circuit. Subscription `dialer-proxy` is stripped from direct node outbounds. Put locally unreachable landings (home broadband) on a chained Platform only; do not attach them to an unchained allowlist.

```bash
curl -X PATCH http://127.0.0.1:2260/api/v1/platforms/<landing-id> \
  -H "Authorization: Bearer $RESIN_ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"chain_platform_ids":["<hop-a-id>","<hop-b-id>"]}'
```

## Global Egress-IP Pool

Policies apply across every Platform. `ACTIVE` permits new and existing leases. `DRAINING` excludes new leases and requests rotation for existing ones, while a still-valid lease can remain deferred when no replacement exists. `BLOCKED` permits neither new nor retained leases.

`max_active_leases=0` means unlimited; positive values cap sticky leases globally for the IP. `resume_at` is RFC3339 and makes a DRAINING/BLOCKED policy effectively ACTIVE after that time. Setting status to ACTIVE clears the resume time.

```bash
curl -X PATCH http://127.0.0.1:2260/api/v1/egress-ips/203.0.113.7 \
  -H "Authorization: Bearer $RESIN_ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"status":"DRAINING","max_active_leases":20,"resume_at":"2026-08-11T02:00:00Z"}'
```

`POST /api/v1/egress-ips/actions/batch-drain` returns an independent result for every IP. URL-encode IPv6 addresses used as path parameters.

## Sticky Rotation

`rotation_interval` is a Go duration. `0s` disables scheduled rotation; a positive interval must be shorter than `sticky_ttl`. Strategies are:

- `ANY`: select any other eligible node.
- `SAME_IP`: change the node while retaining the egress IP.
- `PREFER_NEW_IP`: try a new egress IP first, then any eligible node.
- `REQUIRE_NEW_IP`: require a different egress IP.

Rotation is triggered by the interval, a manual request, DRAINING, or BLOCKED. If a strategy cannot be satisfied but the old lease remains usable, the result is `deferred`; a BLOCKED lease cannot be deferred. Rotation preserves the original lease expiry.

```http
POST /api/v1/platforms/{platform_id}/leases/{account}/actions/rotate
{"strategy":"REQUIRE_NEW_IP"}

POST /api/v1/egress-ips/{ip}/actions/rotate-leases
{"strategy":"PREFER_NEW_IP"}
```

## Routing Diagnostics

`DRY_RUN` evaluates real rules, fallback, egress policy, capacity, an existing lease, and P2C scoring without creating or updating leases or changing capacity, health, or latency. A fixed `seed` makes selection reproducible while state remains unchanged.

`LIVE_TCP` additionally opens and closes one real TCP connection without application payload or health feedback. `REJECT` does not dial, `DIRECT` dials the target directly, and `ROUTED` uses the selected outbound. It requires `required_network=tcp` and a target port, either explicit or inferred from a URL scheme.

```bash
curl -X POST http://127.0.0.1:2260/api/v1/routing-diagnostics \
  -H "Authorization: Bearer $RESIN_ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"mode":"DRY_RUN","platform_name":"Default","account":"user_tom","target":"api.example.com:443","required_network":"tcp","seed":42,"timeout":"5s"}'
```

Use `decision_trace.rules` for rule mismatch reasons, `route.fallback` for traversal order, `route.candidates[].exclusions` for health/lifecycle/capacity filtering, `route.p2c` for scores, and `route.selection` for the final Platform, node, egress IP, and rotation result.

## API Index

| Method and path | Purpose |
| --- | --- |
| `GET/POST /api/v1/routing-rules` | List or create rules |
| `GET/PATCH/DELETE /api/v1/routing-rules/{id}` | Read, update, or delete one rule |
| `PUT /api/v1/routing-rules/order` | Atomically reorder all rules |
| `POST /api/v1/routing-rules/preview-match` | Preview only the target decision |
| `PATCH /api/v1/platforms/{id}` | Configure fallback and rotation |
| `GET /api/v1/egress-ips` | List the pool |
| `PATCH /api/v1/egress-ips/{ip}` | Update one policy |
| `POST /api/v1/egress-ips/actions/batch-drain` | Drain multiple IPs |
| `POST /api/v1/platforms/{id}/leases/{account}/actions/rotate` | Rotate one lease |
| `POST /api/v1/egress-ips/{ip}/actions/rotate-leases` | Rotate leases on an egress IP |
| `POST /api/v1/routing-diagnostics` | Run DRY_RUN or LIVE_TCP diagnostics |

When `RESIN_ADMIN_TOKEN` is configured, every control-plane endpoint above requires `Authorization: Bearer <token>`.
