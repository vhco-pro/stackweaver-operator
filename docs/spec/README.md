# Stackweaver Runner Operator — Specifications

These specifications define the detailed behavior of the operator. They expand on the high-level design in the [plan](../PLAN.md) with implementation-level detail.

| Spec | Description |
|------|-------------|
| [01-controller.md](./01-controller.md) | RunnerPool and Runner controller reconcile logic, phase transitions, error handling, owned resources, spec update behavior |
| [02-scaler.md](./02-scaler.md) | KEDA external scaler gRPC interface, caching, fallback scaler for clusters without KEDA |
| [03-api-client.md](./03-api-client.md) | Stackweaver API client: endpoints consumed, retry strategy, circuit breaker, Secret watching |
| [04-graceful-shutdown.md](./04-graceful-shutdown.md) | Drain protocol, preStop hooks, SIGTERM handling, PodDisruptionBudget, ephemeral mode |
| [05-testing.md](./05-testing.md) | Unit, integration (envtest), and E2E (kind) test strategy with full scenario tables |
| [06-helm-chart.md](./06-helm-chart.md) | Helm chart values schema, RBAC, CRD management, installation guide |
| [07-ci-release.md](./07-ci-release.md) | GitHub Actions CI pipeline, versioning, image publishing, Helm chart release, security |

## CRD Types

The Go type definitions are the source of truth for the CRD schema:

- [`api/v1alpha1/runnerpool_types.go`](../../api/v1alpha1/runnerpool_types.go) — RunnerPool spec, status, phases, cloud identity
- [`api/v1alpha1/runner_types.go`](../../api/v1alpha1/runner_types.go) — Runner spec, status, phases

Generated CRD YAML manifests are in [`config/crd/bases/`](../../config/crd/bases/).

## Consolidated Prometheus Metrics

All operator metrics in one place (defined across individual specs):

### Controller Metrics

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `stackweaver_controller_reconcile_total` | Counter | `controller`, `result` | Reconcile attempts (success/error) |
| `stackweaver_controller_reconcile_duration_seconds` | Histogram | `controller` | Reconcile loop duration |
| `stackweaver_runnerpool_count` | Gauge | `phase` | Number of RunnerPool CRs by phase |
| `stackweaver_runner_count` | Gauge | `pool`, `phase` | Number of Runner CRs by pool and phase |

### Scaler Metrics (from [02-scaler.md](./02-scaler.md))

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `stackweaver_scaler_queue_depth` | Gauge | `agent_pool_id` | Last observed pending job count |
| `stackweaver_scaler_api_requests_total` | Counter | `agent_pool_id`, `status` | API calls made by the scaler |
| `stackweaver_scaler_api_latency_seconds` | Histogram | `agent_pool_id` | API call latency |
| `stackweaver_scaler_cache_hits_total` | Counter | `agent_pool_id` | Cache hit count |
| `stackweaver_scaler_errors_total` | Counter | `agent_pool_id`, `error_type` | Error count by type |

### Shutdown Metrics (from [04-graceful-shutdown.md](./04-graceful-shutdown.md))

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `stackweaver_runner_shutdown_duration_seconds` | Histogram | `pool`, `reason` | Time from SIGTERM to pod exit |
| `stackweaver_runner_forced_terminations_total` | Counter | `pool` | Pods killed by SIGKILL after grace period |
| `stackweaver_runner_graceful_shutdowns_total` | Counter | `pool` | Successful graceful shutdowns |
| `stackweaver_runner_drain_active` | Gauge | `pool` | Number of runners currently draining |
