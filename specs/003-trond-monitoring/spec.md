# Feature Specification: trond Monitoring Stack

**Feature Branch**: `feat/monitoring`
**Created**: 2026-05-20
**Status**: Draft
**Input**: Integrate Prometheus + Grafana monitoring stack into trond, leveraging tron-docker's existing dashboard assets. Deploy and manage the stack declaratively via intent.yaml.

## Background

trond today has excellent CLI diagnostics (`diagnose`, `health`, `logs`, `events`) but no graphical monitoring. Operators who want to observe node health over time, inspect metrics trends, or set up alerts must manually configure Prometheus + Grafana outside of trond.

tron-docker (the separate repo) already has a production-tested monitoring stack: 5 pre-built Grafana dashboards, Prometheus configs, and compose files. This feature embeds those assets into trond and makes monitoring a first-class, intent-driven capability.

## Non-Goals

- trond will NOT implement its own metrics collection or storage. Prometheus handles scraping and TSDB; trond is the orchestrator.
- trond will NOT replace tron-docker's advanced monitoring setups (Thanos remote-write, Victoria Metrics, Loki log aggregation). Those stay in tron-docker for operators who need them.
- trond will NOT build a custom Grafana UI or dashboard editor. We ship pre-built dashboards; customization happens in Grafana's native UI.
- Monitoring for `trond network create` (multi-node) is Phase 2 — Phase 1 focuses on single-node `trond apply`.

## Design Decisions

### Monitoring stack deployment model: Independent compose

The monitoring stack (Prometheus + Grafana) is deployed as an **independent docker-compose project**, not mixed into the node's compose. This keeps lifecycles clean: monitoring can be upgraded or removed without touching the node.

### Runtime coverage: Docker + Jar for `trond apply`

Phase 1 covers both runtimes for single-node `trond apply`:
- **Docker runtime**: monitoring co-locates on the same target machine as the node
- **Jar runtime**: monitoring deploys to the **trond machine** (centralized), scraping the remote node via network

`trond network create` (multi-node) monitoring is Phase 2.

## Intent Schema

### New top-level `monitoring:` block

```yaml
name: my-node
network: mainnet
target:
  type: local
  runtime: docker

monitoring:
  enabled: true              # explicit opt-in; default false
  prometheus:
    port: 9090               # Prometheus UI port; default 9090
    retention: 7d            # TSDB retention; default 7d
  grafana:
    port: 3000               # Grafana UI port; default 3000

nodes:
  - type: fullnode
```

### Validation rules

| Field | Rule |
|-------|------|
| `monitoring.enabled` | `*bool`; default `false` |
| `monitoring.prometheus.port` | `0` or `1024-65535`; default `9090` |
| `monitoring.grafana.port` | `0` or `1024-65535`; default `3000` |
| `monitoring.prometheus.retention` | Pattern `^\d+[dwh]$` (e.g. "7d", "2w"); default "7d" |

### Auto-enable metrics in HOCON

When `monitoring.enabled=true` (or `--monitor` flag), `RenderHOCON` automatically injects `node.metrics.prometheus.enable = true` into the rendered config, regardless of `features.metrics` setting. The user does not need to manually remember to flip the feature flag.

### CLI flags for ad-hoc override

`--monitor` and `--no-monitor` are first-class CLI flags on `trond apply`. They override the intent's `monitoring.enabled` for a single invocation without editing the file:

```bash
# Intent says enabled=true, but disable for this run
trond apply --intent node.yaml --no-monitor

# Intent says enabled=false, but enable for this run
trond apply --intent node.yaml --monitor
```

| Source | Effect |
|--------|--------|
| No CLI flag | Follow `monitoring.enabled` from intent (default `false`) |
| `--monitor` | Force enable, regardless of intent |
| `--no-monitor` | Force disable, regardless of intent |

## Architecture

```
┌─────────────────────────────────────────────────────────────────┐
│                        trond apply                               │
├─────────────────────────────────────────────────────────────────┤
│  1. Load intent                                                  │
│  2. Validate                                                     │
│  3. Apply defaults                                               │
│  4. Resolve build (if build: block present)                      │
│  5. Render HOCON  ──► auto-inject metrics config                │
│  6. Render compose / systemd                                     │
│  7. Deploy node via runtime                                      │
│  8. [IF monitoring.enabled]                                      │
│       a. Determine scrape target address                         │
│       b. Render monitoring compose + prometheus.yml              │
│       c. Deploy monitoring stack via MonitoringRuntime           │
│       d. Record monitoring state in ManagedNode                  │
│  9. Wait (if --wait)                                             │
│ 10. Return Result (with optional monitoring_error)               │
└─────────────────────────────────────────────────────────────────┘
```

### Monitoring stack file layout

```
~/.trond/monitoring/<name>/
├── docker-compose.yaml           # prometheus + grafana services
├── conf/
│   └── prometheus.yml            # scrape targets (dynamic)
└── grafana/
    ├── provisioning/
    │   ├── datasources/
    │   │   └── prometheus.yml    # datasource auto-config
    │   └── dashboards/
    │       └── dashboards.yml    # dashboard provider
    └── dashboards/
        ├── java-tron-server.json
        ├── java-tron-api.json
        ├── java-tron-api-statistic.json
        ├── java-tron-mechanism.json
        └── node-exporter-full.json
```

### Scrape target addresses by runtime

| Runtime | Target Type | Scrape Address |
|---------|-------------|----------------|
| docker | local | `host.docker.internal:<metrics_port>` (Desktop) / `172.17.0.1:<metrics_port>` (Linux) |
| docker | ssh | `<ssh_host>:<metrics_port>` |
| jar | local | `127.0.0.1:<metrics_port>` |
| jar | ssh | `<ssh_host>:<metrics_port>` |

For jar+ssh, the monitoring stack runs on the **trond machine** (local target) and scrapes the remote node.

### Monitoring deployment is non-fatal

If the monitoring stack fails to deploy, the node deployment still succeeds. The error is surfaced in `Result.MonitoringError` so callers (CLI, MCP, recipe) can decide whether to surface a warning.

## Reused Assets (from tron-docker)

| Asset | Source | Destination in trond |
|-------|--------|---------------------|
| 5 Grafana dashboards | `tron-docker/metric_monitor/grafana_dashboard/*.json` | `internal/render/dashboards/*.json` (embedded via `//go:embed`) |
| Prometheus image | `prom/prometheus:v3.10.0` | Same |
| Grafana image | `grafana/grafana-oss:12.4.1` | Same |

Dashboard JSONs are normalized at load time: hardcoded datasource UIDs are replaced with `"prometheus"` to match the provisioned datasource name.

## State Persistence

`ManagedNode` gains an optional `MonitoringState`:

```go
type MonitoringState struct {
    Enabled        bool   `json:"enabled"`
    PrometheusPort int    `json:"prometheus_port"`
    GrafanaPort    int    `json:"grafana_port"`
    TargetType     string `json:"target_type"` // "local" or "ssh"
}
```

This enables `trond remove` to clean up the monitoring stack alongside the node.

## Schema Version Impact

- **Bump**: `1.3.0` → `1.4.0` (MINOR: new monitoring schema added)
- **Modified schemas**: `intent.schema.json` (adds `monitoring:` block), `apply.schema.json` (adds `monitoring_error` and `monitoring` output fields)
- **No breaking changes** to existing schemas

## Known Limitations

### Single Prometheus instance in multi-node networks

The monitoring stack deploys exactly one Prometheus container for the entire network. Under normal operation this works well — all nodes are scraped via the shared docker network.

Under `trond partition`, the Prometheus container lands in one partition group and loses visibility into nodes in other groups. Their metrics vanish from dashboards until `trond heal` restores connectivity. This is an inherent trade-off of the single-Prometheus deployment model: simplicity and resource efficiency over partition tolerance. Operators who need per-partition observability should run their own monitoring setup.

## Open Questions

1. Should monitoring ports participate in `auto_ports` allocation? If prometheus 9090 is taken, should trond auto-pick 9091?
2. For jar+ssh, should we attempt to open the remote firewall (e.g. `ufw allow 9527`) or document that the user must ensure network reachability?
3. Should `trond status` include monitoring health (prometheus/grafana container status)?
