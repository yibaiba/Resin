# 路由控制与诊断

Resin 的路由控制按以下顺序执行：目标路由规则、Platform fallback、出口 IP 策略、粘性租约轮换、诊断。所有 HTTP 正向代理、HTTP CONNECT、反向代理、SOCKS5 TCP 和 SOCKS5 UDP 入口共用同一套目标决策。

## WebUI 入口

- `/ui/routing-rules`：创建、启停、排序和预览目标路由规则。
- `/ui/platforms`：在 Platform 详情的配置页维护 fallback 顺序、轮换间隔和轮换策略；在租约页手动轮换单个账号。
- `/ui/egress-ips`：维护出口 IP 生命周期、容量、恢复时间，执行批量排空和按出口 IP 轮换。
- `/ui/routing-diagnostics`：执行可复现的 dry-run 或一次真实 TCP 建连测试。

## 1. 目标路由规则

决策顺序如下：

1. `RESIN_PROXY_BYPASS` 命中时直接 `DIRECT`。
2. 启用的持久化规则按 `priority` 从小到大匹配，首条命中即停止。
3. 请求已经显式指定 Platform 时，默认跳过规则；只有 `override_explicit_platform=true` 的规则可以覆盖它。
4. 没有规则命中时使用请求指定的 Platform，未指定则使用 `Default`。

主机匹配支持 `ANY`、`EXACT`、`DOMAIN_SUFFIX`、`WILDCARD`（必须为 `*.example.com` 形式）和 `CIDR`。端口范围是闭区间；空数组表示任意端口。网络可选 `ANY`、`TCP`、`UDP`，动作可选 `PLATFORM`、`DIRECT`、`REJECT`。

```bash
curl -X POST http://127.0.0.1:2260/api/v1/routing-rules \
  -H "Authorization: Bearer $RESIN_ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "name":"Block private API",
    "enabled":true,
    "host_type":"DOMAIN_SUFFIX",
    "host_value":"internal.example.com",
    "ports":[{"start":443,"end":443}],
    "network":"TCP",
    "action":"REJECT",
    "override_explicit_platform":true
  }'
```

规则排序接口要求一次提交全部规则 ID，顺序即新优先级：

```http
PUT /api/v1/routing-rules/order
{"rule_ids":["<first-rule-id>","<second-rule-id>"]}
```

## 2. Platform fallback

`fallback_platform_ids` 是有序 Platform ID 列表。Resin 从根 Platform 开始深度优先遍历，选择第一个存在合格候选的 Platform。fallback 只发生在代理拨号前；一旦上游连接或请求已经开始，失败不会触发跨 Platform 重放。

Platform 配置必须是无环图，不能引用自身、重复或不存在的 Platform。被其他 Platform 引用的 Platform 不能直接删除。租约的 `platform_id` 始终表示请求归属，`selected_platform_id` 表示实际提供节点的 Platform。

```bash
curl -X PATCH http://127.0.0.1:2260/api/v1/platforms/<owner-platform-id> \
  -H "Authorization: Bearer $RESIN_ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"fallback_platform_ids":["<fallback-a-id>","<fallback-b-id>"]}'
```

## 3. Platform 链式代理

`chain_platform_ids` 是落地 Platform 的有序前置 Platform 列表。空数组表示直连。填一个 ID 就是 A→C。在 C 上填 `[A, B]` 就是 A→B→C。前置 Platform 自己的 chain 不会再展开。

真链式是 sing-box `detour`：用前置 outbound 去拨落地服务器，再在隧道上做落地协议。前置 Platform 沿用自己的粘性、过滤、熔断和 fallback。前置没有节点时整次请求失败，错误码仍是 `NO_AVAILABLE_NODES`。被引用的前置 Platform 不能删除。

前置节点直连探测。落地节点只在属于恰好一条非空 chain 时，探针才走这条链。落地订阅应单独放在链式平台里。

```bash
curl -X PATCH http://127.0.0.1:2260/api/v1/platforms/<landing-id> \
  -H "Authorization: Bearer $RESIN_ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"chain_platform_ids":["<hop-a-id>","<hop-b-id>"]}'
```

## 4. 出口 IP 池

每个探测到的出口 IP 都可以设置全局策略，跨 Platform 共同生效：

| 状态 | 行为 |
| --- | --- |
| `ACTIVE` | 可创建和继续使用租约。 |
| `DRAINING` | 不创建新租约；既有租约触发轮换，无法轮换时可继续保留仍有效的租约。 |
| `BLOCKED` | 不创建或保留租约，既有租约在下次路由时淘汰。 |

`max_active_leases=0` 表示不限容量；正数限制该出口 IP 在所有 Platform 中的粘性租约总数。`resume_at` 使用 RFC3339，时间到达后 DRAINING/BLOCKED 的有效状态自动恢复为 ACTIVE；将配置状态改为 ACTIVE 会清空恢复时间。

```bash
curl -X PATCH http://127.0.0.1:2260/api/v1/egress-ips/203.0.113.7 \
  -H "Authorization: Bearer $RESIN_ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"status":"DRAINING","max_active_leases":20,"resume_at":"2026-08-11T02:00:00Z"}'
```

批量排空使用 `POST /api/v1/egress-ips/actions/batch-drain`，返回每个 IP 的独立成功或失败结果。IPv6 地址作为路径参数时应进行 URL 编码。

## 5. 粘性轮换

Platform 的 `rotation_interval` 使用 Go duration；`0s` 表示禁用定时轮换，正值必须小于 `sticky_ttl`。`rotation_strategy` 支持：

| 策略 | 行为 |
| --- | --- |
| `ANY` | 选择任意其他合格节点。 |
| `SAME_IP` | 更换节点但保持出口 IP。 |
| `PREFER_NEW_IP` | 优先新出口 IP，无新 IP 时允许其他合格节点。 |
| `REQUIRE_NEW_IP` | 必须更换出口 IP。 |

轮换在间隔到期、手动请求、DRAINING 或 BLOCKED 时触发。无法满足策略但旧租约仍允许继续使用时，返回 `deferred` 并保留原因；BLOCKED 租约不会被延期保留。轮换不延长原租约 TTL。

```http
POST /api/v1/platforms/{platform_id}/leases/{account}/actions/rotate
{"strategy":"REQUIRE_NEW_IP"}

POST /api/v1/egress-ips/{ip}/actions/rotate-leases
{"strategy":"PREFER_NEW_IP"}
```

## 6. 路由诊断

`DRY_RUN` 使用真实规则、fallback、出口策略、容量、现有租约和 P2C 评分，但不创建或更新租约，也不修改容量、健康或延迟状态。指定相同 `seed` 和相同状态时可复现选择结果。

`LIVE_TCP` 在相同诊断结果上额外执行一次真实 TCP 建连并立即关闭，不发送应用数据，也不提交节点健康反馈。`REJECT` 不拨号；`DIRECT` 直接连接目标；`ROUTED` 使用诊断选择的出站节点。该模式只支持 `required_network=tcp`，目标必须包含端口或可由 URL scheme 推导端口。

```bash
curl -X POST http://127.0.0.1:2260/api/v1/routing-diagnostics \
  -H "Authorization: Bearer $RESIN_ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "mode":"DRY_RUN",
    "platform_name":"Default",
    "account":"user_tom",
    "target":"api.example.com:443",
    "required_network":"tcp",
    "seed":42,
    "timeout":"5s"
  }'
```

响应中的 `decision_trace.rules` 解释规则未命中原因，`route.fallback` 展示遍历顺序，`route.candidates[].exclusions` 展示健康、生命周期和容量排除，`route.p2c` 展示评分，`route.selection` 展示最终 Platform、节点、出口 IP 和轮换结果。

## API 索引

| 方法与路径 | 用途 |
| --- | --- |
| `GET/POST /api/v1/routing-rules` | 列表、新建规则 |
| `GET/PATCH/DELETE /api/v1/routing-rules/{id}` | 查询、修改、删除规则 |
| `PUT /api/v1/routing-rules/order` | 原子重排全部规则 |
| `POST /api/v1/routing-rules/preview-match` | 仅预览目标决策 |
| `PATCH /api/v1/platforms/{id}` | 设置 fallback 与轮换配置 |
| `GET /api/v1/egress-ips` | 查询出口池 |
| `PATCH /api/v1/egress-ips/{ip}` | 修改单个策略 |
| `POST /api/v1/egress-ips/actions/batch-drain` | 批量排空 |
| `POST /api/v1/platforms/{id}/leases/{account}/actions/rotate` | 轮换单个租约 |
| `POST /api/v1/egress-ips/{ip}/actions/rotate-leases` | 轮换出口 IP 上的租约 |
| `POST /api/v1/routing-diagnostics` | dry-run 或 LIVE_TCP 诊断 |

以上控制面接口在配置了 `RESIN_ADMIN_TOKEN` 时都需要 `Authorization: Bearer <token>`。
