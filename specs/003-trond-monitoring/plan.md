# Implementation Plan: trond Monitoring Stack

**Branch**: `feat/monitoring` | **Date**: 2026-05-20 | **Spec**: [spec.md](spec.md)

## Summary

Add a `monitoring:` intent block and `--monitor` / `--no-monitor` CLI flags that deploy a Prometheus + Grafana stack alongside java-tron nodes. The monitoring stack is deployed as an independent docker-compose project. Phase 1 covers `trond apply` for both docker and jar runtimes; Phase 2 covers `trond network create`.

The feature reuses tron-docker's 5 Grafana dashboard JSONs (embedded via `//go:embed`) and follows its Prometheus/Grafana image versions. Monitoring configuration (prometheus.yml, compose YAML) is generated dynamically by Go template so ports, names, and targets resolve from the intent at apply time.

---

## Technical Context

**Language/Version**: Go 1.25+

**New dependencies**: **None.** All external interactions go through `os/exec`, consistent with the rest of trond:
- `docker compose` — existing pattern
- Prometheus and Grafana run as containers; trond does not embed their binaries

**Existing trond packages reused**:
- `internal/intent/` — schema, defaults, validation
- `internal/render/` — HOCON + compose rendering, template loading
- `internal/runtime/` — DockerRuntime, JarRuntime patterns
- `internal/state/` — persist monitoring state on ManagedNode
- `internal/target/` — local + SSH targets
- `internal/output/` — structured error envelope
- `internal/schema/` — schema versioning

**Assets embedded**:
- `//go:embed dashboards/*.json` — 5 Grafana dashboard JSONs copied from tron-docker

---

## Architecture

```
                         trond apply --intent dev.yaml [--monitor]
                              |
        ┌─────────────────────┼─────────────────────┐
        |                     |                     |
   cmd/apply.go         cmd/apply.go           internal/mcp
   (cobra flags)        (cobra flags)          (tool: apply)
        |                     |
        └──────── calls ──────┴─────── calls ───────┘
                              |
                    internal/apply/apply.go
                              |
               ┌──────────────┼──────────────┐
               |              |              |
               ▼              ▼              ▼
         Deploy node   Deploy monitoring   Persist state
               |              |              |
               ▼              ▼              ▼
   internal/runtime/   internal/runtime/   internal/state/
   docker.go/jar.go    monitoring.go       types.go
               |              |
               └──────────────┘
                    docker compose up
```

### Monitoring deployment flow

```
1. Determine monitoring enabled:
   --monitor          → true
   --no-monitor       → false
   neither            → intent.Monitoring.Enabled (default false)

2. Determine monitoring target:
   docker runtime     → same target as node
   jar runtime        → local (trond machine)

3. Determine scrape address:
   docker runtime     → <container_name>:<metrics_port> (docker internal network)
   jar runtime        → <host>:<metrics_port>

4. Render monitoring compose (Go template):
   - prometheus service (image, port mapping, volume mounts)
   - grafana service (image, port mapping, provisioning mounts)

5. Write assets to ~/.trond/monitoring/<name>/
   - docker-compose.yaml
   - conf/prometheus.yml (dynamic targets)
   - grafana/provisioning/datasources/prometheus.yml
   - grafana/provisioning/dashboards/dashboards.yml
   - grafana/dashboards/*.json (5 embedded dashboards)

6. docker compose up -d

7. Record monitoring state in ManagedNode.Monitoring
```

### Prometheus scrape target addresses

| Runtime | Target Type | Scrape Address |
|---------|-------------|----------------|
| docker | local | `<container_name>:<metrics_port>` via docker internal network |
| docker | ssh | `<container_name>:<metrics_port>` via docker internal network |
| jar | local | `127.0.0.1:<metrics_port>` |
| jar | ssh | `<ssh_host>:<metrics_port>` |

For docker, the monitoring compose references the node's docker network as `external: true` so prometheus can resolve the container name.

### Monitoring stack file layout

```
~/.trond/monitoring/
└── <name>/
    ├── docker-compose.yaml
    ├── conf/
    │   └── prometheus.yml
    ├── grafana/
    │   ├── provisioning/
    │   │   ├── datasources/
    │   │   │   └── prometheus.yml
    │   │   └── dashboards/
    │   │       └── dashboards.yml
    │   └── dashboards/
    │       ├── java-tron-server.json
    │       ├── java-tron-api.json
    │       ├── java-tron-api-statistic.json
    │       ├── java-tron-mechanism.json
    │       └── node-exporter-full.json
    ├── prometheus_data/     # persisted TSDB
    └── grafana_data/        # persisted settings
```

### Docker network attachment

For `trond apply` (single node):
```yaml
# monitoring-compose.yaml
networks:
  <name>_default:
    external: true

services:
  prometheus:
    networks:
      - <name>_default
```

For `trond network create` (multi-node, Phase 2):
```yaml
networks:
  trond-<name>:
    external: true
```

### Monitoring deployment is non-fatal

If the monitoring stack fails to deploy, the node deployment still succeeds. The error is surfaced in `Result.MonitoringError` for CLI/MCP/recipe visibility.

---

## Phase Breakdown

### Phase 1 — Intent Schema + Validation (~1 day)

**Deliverable**: `trond config validate` accepts and validates `monitoring:` blocks.

- `internal/intent/schema.go`: Add `Monitoring`, `PromConfig`, `GrafConfig` structs
- `internal/intent/defaults.go`: Add `applyMonitoringDefaults()` — default enabled=false, port=9090/3000, retention=7d
- `internal/intent/loader.go`: Add `validateMonitoring()` — port range 1024-65535, retention format `^\d+[dwh]$`
- `internal/intent/fields_test.go`: Add `TestMonitoring_*` tests
- `schemas/intent.schema.json`: Add `monitoring` block + `Monitoring`/`PromConfig`/`GrafConfig` defs
- `internal/schema/embed.go`: Bump SchemaVersion to 1.4.0
- `internal/schema/version_baseline.json`: Regenerate via `TROND_SCHEMA_UPDATE_BASELINE=1 go test`
- `schemas/output/apply.schema.json`: Add `monitoring_error` and `monitoring` fields

### Phase 2 — HOCON Auto-Enable Metrics (~0.5 day)

**Deliverable**: `RenderHOCON` injects `node.metrics.prometheus.enable=true` when monitoring is enabled.

- `internal/render/hocon.go`: Modify `applyFeatureOverrides()` to inject metrics config
- `internal/render/hocon_test.go`: Add `TestEnsureMetricsEnabled_*` tests
- Logic: if monitoring enabled and HOCON lacks `node.metrics.prometheus` block, append it; if it exists, set `enable = true`

### Phase 3 — Monitoring Renderers + Asset Embedding (~2 days)

**Deliverable**: Monitoring compose + prometheus config + dashboard assets render correctly.

- Copy 5 dashboard JSONs from `tron-docker/metric_monitor/grafana_dashboard/` to `internal/render/dashboards/`
- `internal/render/embed.go`: Add `//go:embed dashboards/*.json`
- `internal/render/monitoring.go` (new):
  - `MonitoringTarget` struct
  - `RenderMonitoringCompose(name, intent, targets) string` — Go template, based on tron-docker compose structure
  - `RenderPrometheusConfig(targets, retention) string`
  - `RenderGrafanaProvisioning(prometheusURL)` — datasource + dashboard provider YAML
  - `LoadDashboard(name) ([]byte, error)` — read embedded dashboard
  - `DashboardNames() []string`
  - `normalizeDashboard(data []byte) []byte` — replace hardcoded UIDs with `"prometheus"`
- `internal/render/monitoring_test.go` (new):
  - Assert rendered compose contains prometheus + grafana services
  - Assert prometheus config contains correct targets
  - Assert dashboard JSON loads and is normalized

### Phase 4 — MonitoringRuntime (~1 day)

**Deliverable**: `MonitoringRuntime` deploys/removes/statuses monitoring stacks.

- `internal/runtime/monitoring.go` (new):
  - `MonitoringRuntime` struct (target + workDir)
  - `NewMonitoringRuntime(t target.Target, workDir string)`
  - `MonitoringDeployOpts` struct
  - `Deploy(ctx, opts) error` — create dir, write compose + configs + dashboards, `docker compose up -d`
  - `Remove(ctx, name) error` — `docker compose down -v`, best-effort rm
  - `Status(ctx, name) (*NodeStatus, error)` — `docker compose ps`, parse running/exited
- `internal/runtime/monitoring_test.go` (new):
  - Unit test with fake target (same pattern as existing runtime tests)

### Phase 5 — Apply Integration (~2 days)

**Deliverable**: `trond apply --monitor` deploys node + monitoring end-to-end.

- `cmd/apply.go`: Add `--monitor` and `--no-monitor` flags
- `internal/apply/apply.go`:
  - Determine monitoring enabled from CLI flags → intent → default
  - After node deploy, if monitoring enabled: call `deployMonitoring()`
  - `deployMonitoring()` function:
    - Build `[]MonitoringTarget` from intent (runtime-specific address resolution)
    - Determine monitoring target (same target for docker, local for jar)
    - Create `MonitoringRuntime`, call `Deploy()`
    - On failure: record in `Result.MonitoringError` (non-fatal)
    - On success: record in `ManagedNode.Monitoring`
  - Add `MonitoringError string` to `Result` struct
  - Add `Monitoring *MonitoringState` to `ManagedNode` (via `internal/state/types.go`)
- `internal/apply/apply_test.go`:
  - Test monitoring deploy with stub target
  - Test monitoring failure is non-fatal
- Preflight integration: check Docker available when `--monitor` is set

### Phase 6 — Remove / Destroy Cleanup (~0.5 day)

**Deliverable**: `trond remove` cleans up monitoring stack.

- `cmd/remove.go`: Before node removal, check `ManagedNode.Monitoring`; if present, call `MonitoringRuntime.Remove()`
- `cmd/network/destroy.go` (Phase 2): Best-effort monitoring removal before node teardown

### Phase 7 — E2E Tests + Examples (~1 day)

**Deliverable**: E2E test passes; example intent available.

- `cmd/apply_monitoring_e2e_test.go`:
  - `TestApply_WithMonitoring` — apply with monitoring enabled, verify prometheus/grafana respond 200
  - Cleanup: remove node, verify monitoring containers gone
- `examples/monitoring.yaml` — example intent with `monitoring.enabled: true`
- `examples/README.md` — quickstart: "Enable monitoring for your node"

### Phase 8 — Phase 2: Network Create (~1 day, deferred)

**Deliverable**: `trond network create` deploys global monitoring for multi-node networks.

- `cmd/network/create.go`: After node loop, deploy monitoring stack with all nodes as targets
- Monitoring target addresses use container names within shared `trond-<name>` network

---

## Total Estimate

~8 working days from schema to E2E (Phase 1-7). Phase 8 (network create) is an additional ~1 day.

| Phase | Scope | Est |
|-------|-------|-----|
| 1 | Intent schema + validation | 1d |
| 2 | HOCON auto-enable metrics | 0.5d |
| 3 | Monitoring renderers + assets | 2d |
| 4 | MonitoringRuntime | 1d |
| 5 | Apply integration | 2d |
| 6 | Remove/destroy cleanup | 0.5d |
| 7 | E2E tests + examples | 1d |
| **Total** | | **~8d** |

---

## Risks and Mitigations

| Risk | Mitigation |
|------|------------|
| **Dashboard JSON has hardcoded datasource UIDs** | `normalizeDashboard()` replaces UIDs with `"prometheus"` at load time |
| **Docker not available on target machine (jar mode)** | Preflight check; monitoring skipped with warning if Docker absent |
| **Port conflict (9090/3000 taken)** | Preflight warns; user can override via intent ports |
| **Monitoring compose project name collision** | Use `<name>-monitoring` as project name, same as node name prefix |
| **Prometheus fails to scrape before node is ready** | Prometheus retries naturally; `--wait` on apply ensures node is up first |
| **Jar+SSH network reachability** | Document requirement that target must expose metrics port; no automatic firewall magic |

---

## Schema Impact

- **SchemaVersion**: `1.3.0` → `1.4.0` (MINOR: new monitoring schema)
- **New fields in intent.schema.json**: `monitoring`, `monitoring.enabled`, `monitoring.prometheus`, `monitoring.grafana`
- **New fields in apply.schema.json**: `monitoring_error`, `monitoring.prometheus_url`, `monitoring.grafana_url`
- **New state field**: `ManagedNode.Monitoring` (optional)
- **No breaking changes** to existing schemas
