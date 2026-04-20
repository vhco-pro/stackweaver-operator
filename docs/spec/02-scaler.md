# KEDA External Scaler Specification

This document specifies the KEDA external scaler component that bridges Stackweaver job queue depth to KEDA scaling decisions.

## Overview

The operator runs a gRPC server implementing the [KEDA external scaler interface](https://keda.sh/docs/latest/concepts/external-scalers/). KEDA calls this server to determine how many runner replicas are needed. The scaler's sole job is to answer "how many pending jobs exist for a given agent pool?" — all scaling decisions (cooldown, stabilisation, replica count calculation) are handled by KEDA.

## gRPC Interface

The scaler implements the `externalscaler.ExternalScaler` service:

```protobuf
service ExternalScaler {
  rpc IsActive(ScaledObjectRef) returns (IsActiveResponse);
  rpc GetMetricSpec(ScaledObjectRef) returns (GetMetricSpecResponse);
  rpc GetMetrics(GetMetricsRequest) returns (GetMetricsResponse);
  rpc StreamIsActive(ScaledObjectRef) returns (stream IsActiveResponse);
}
```

### Metadata

KEDA passes metadata from the ScaledObject trigger to every gRPC call. The scaler expects:

| Key | Required | Description |
|-----|----------|-------------|
| `agentPoolId` | yes | Stackweaver agent pool ID to query |
| `serverUrl` | yes | Stackweaver API base URL |
| `tokenSecretRef` | yes | Name of the Secret containing the API key |
| `tokenSecretKey` | no | Key within the Secret (default: `token`) |
| `tokenSecretNamespace` | yes | Namespace of the Secret |
| `runnerType` | no | Filter pending jobs by type (`terraform`, `ansible`). If unset, returns total pending. |
| `targetSize` | no | Jobs per runner (default: `1`). Maps from RunnerPool `spec.jobsPerRunner`. |

### Secret Resolution

The gRPC scaler receives only string metadata from KEDA — not actual Secret values. To resolve the API key:

1. The scaler runs in the same pod as the controller manager (sidecar mode) and uses the operator's ServiceAccount
2. On each request, the scaler reads the Secret from the Kubernetes API using the `tokenSecretRef`, `tokenSecretKey`, and `tokenSecretNamespace` metadata values
3. Resolved keys are cached for 60 seconds to avoid excessive Secret reads
4. If the Secret cannot be read (missing, RBAC error), the scaler returns an error to KEDA

The operator's ClusterRole already includes `get`, `list`, `watch` on Secrets (see [06-helm-chart.md](./06-helm-chart.md)). If the scaler runs as a separate Deployment, it needs its own ServiceAccount with the same Secret RBAC.

### IsActive

Called by KEDA to determine whether to scale from zero.

```
Input:  ScaledObjectRef (contains metadata)
Output: IsActiveResponse { result: bool }
```

**Behavior:**
1. Call `GET {serverUrl}/api/v2/agent-pools/{agentPoolId}/queue-depth`
2. Parse response for `total-pending` (or type-specific count if `runnerType` is set)
3. Return `result: true` if pending > 0

**Failure behavior:**
- If the API call fails, return `result: false` (don't scale up if we can't verify demand)
- Log the error for observability

### GetMetricSpec

Called by KEDA to learn which metrics to watch and their target values.

```
Input:  ScaledObjectRef (contains metadata)
Output: GetMetricSpecResponse { metricSpecs: [] }
```

**Behavior:**
Return a single metric spec:
- `metricName`: `stackweaver_pending_jobs_{agentPoolId}`
- `targetSize`: value of `targetSize` from metadata (default: 1)

KEDA calculates desired replicas as `ceil(metricValue / targetSize)`. The RunnerPool controller sets `targetSize` in the ScaledObject trigger metadata to match `spec.jobsPerRunner`, so a pool with `jobsPerRunner: 3` and 9 pending jobs scales to 3 runners.

### GetMetrics

Called by KEDA on each polling interval to get the current metric value.

```
Input:  GetMetricsRequest { scaledObjectRef, metricName }
Output: GetMetricsResponse { metricValues: [] }
```

**Behavior:**
1. Call `GET {serverUrl}/api/v2/agent-pools/{agentPoolId}/queue-depth`
2. Extract pending job count (filtered by `runnerType` if set)
3. Return `metricValues: [{ metricName, metricValue: pendingCount }]`

**Failure behavior:**
- If the API call fails, return the last known good value (stale metric) with a warning log
- If no cached value exists (first call), return `metricValue: 0` (safe — don't scale up on unknown)

### StreamIsActive

Optional streaming variant. Returns immediately and sends updates when active state changes. The scaler implements this as a polling loop:

1. Poll queue depth every 10 seconds
2. On state change (active ↔ inactive), send `IsActiveResponse`
3. On client disconnect, stop polling

If not implemented, KEDA falls back to polling `IsActive` directly.

## API Client

The scaler uses a shared API client (see [03-api-client.md](./03-api-client.md)) to call the Stackweaver API.

### Queue Depth Endpoint

```
GET /api/v2/agent-pools/{agentPoolId}/queue-depth
Authorization: Bearer {api-key}

Response 200:
{
  "data": {
    "type": "queue-depths",
    "id": "{agentPoolId}",
    "attributes": {
      "pending-terraform-jobs": 3,
      "pending-ansible-jobs": 1,
      "total-pending": 4,
      "busy-runners": 2,
      "total-runners": 5,
      "idle-runners": 3
    }
  }
}
```

Note: This endpoint does not exist in the Stackweaver API yet. It must be implemented as part of Phase 2 (see plan). The scaler spec is written against the planned response format.

## Caching

To avoid redundant API calls when KEDA calls multiple methods in quick succession:

- Cache queue-depth responses for 5 seconds (configurable)
- Cache key: `{serverUrl}:{agentPoolId}`
- Use sync.Map or similar thread-safe cache (the gRPC server handles concurrent requests)
- On cache hit, return cached value without API call
- On cache miss or stale entry, make API call and update cache

## gRPC Server Configuration

| Setting | Default | Description |
|---------|---------|-------------|
| Listen address | `:9090` | gRPC server bind address |
| TLS | Disabled | Optional TLS for gRPC (cert/key from flags or Secret) |
| Max concurrent streams | 100 | Per-connection limit |
| Keepalive interval | 30s | gRPC keepalive ping interval |

The scaler runs as a sidecar container in the operator Deployment (same pod as the controller manager), or as a separate Deployment. Running in the same pod simplifies Secret access and reduces resource overhead.

## ScaledObject Generation

The RunnerPool controller generates a KEDA ScaledObject for each RunnerPool. The scaler spec defines what the ScaledObject looks like:

```yaml
apiVersion: keda.sh/v1alpha1
kind: ScaledObject
metadata:
  name: {pool.name}-scaler
  namespace: {pool.namespace}
  ownerReferences:
    - apiVersion: runner.stackweaver.io/v1alpha1
      kind: RunnerPool
      name: {pool.name}
spec:
  scaleTargetRef:
    name: {pool.name}          # Deployment name
  minReplicaCount: {pool.spec.minReplicas}
  maxReplicaCount: {pool.spec.maxReplicas}
  cooldownPeriod: {pool.spec.scaleDownDelaySeconds}
  pollingInterval: 15          # seconds — how often KEDA calls GetMetrics
  triggers:
    - type: external
      metadata:
        scalerAddress: "stackweaver-operator-scaler.{operator-namespace}.svc.cluster.local:9090"
        agentPoolId: {pool.spec.agentPoolId}
        serverUrl: {pool.spec.serverUrl}
        tokenSecretRef: {pool.spec.tokenSecretRef.name}
        tokenSecretKey: {pool.spec.tokenSecretRef.key}
        tokenSecretNamespace: {pool.namespace}
        runnerType: {pool.spec.runnerType}
        targetSize: "{pool.spec.jobsPerRunner}"
```

## Fallback Scaler (No KEDA)

For clusters without KEDA installed, the operator provides a built-in fallback scaler. This is intentionally minimal — it is not a reimplementation of KEDA.

### Behavior

1. RunnerPool controller detects KEDA is not installed (ScaledObject CRD not registered)
2. Controller logs a warning: "KEDA not detected, using built-in fallback scaler"
3. Sets condition `ScalingActive=True` with message "using built-in scaler (KEDA not installed)"
4. Controller runs its own poll loop per RunnerPool:
   - Poll `GET /api/v2/agent-pools/{id}/queue-depth` every 15 seconds
   - Calculate desired replicas: `min(maxReplicas, max(minReplicas, ceil(pendingJobs / jobsPerRunner)))`
   - Update Deployment replica count directly
   - Respect cooldown: don't scale down if last scale event was within `scaleDownDelaySeconds`

### Limitations vs KEDA

- No stabilisation window (KEDA uses a sliding window to avoid flapping)
- No metrics-server integration
- No HPA integration
- Simple cooldown only (time-based, no metric-based stabilisation)
- No scale velocity controls

The fallback scaler is documented as "basic" and users are encouraged to install KEDA for production use.

## Observability

The scaler exposes Prometheus metrics:

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `stackweaver_scaler_queue_depth` | Gauge | `agent_pool_id` | Last observed pending job count |
| `stackweaver_scaler_api_requests_total` | Counter | `agent_pool_id`, `status` | API calls made by the scaler |
| `stackweaver_scaler_api_latency_seconds` | Histogram | `agent_pool_id` | API call latency |
| `stackweaver_scaler_cache_hits_total` | Counter | `agent_pool_id` | Cache hit count |
| `stackweaver_scaler_errors_total` | Counter | `agent_pool_id`, `error_type` | Error count by type |
