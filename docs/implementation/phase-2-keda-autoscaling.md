# Phase 2: KEDA Auto-Scaling — Implementation Plan

## Goal

Replace static replica counts with KEDA-driven auto-scaling based on job queue depth. Includes the new queue-depth API endpoint in the main Stackweaver platform, the KEDA external scaler gRPC server, and the fallback scaler for non-KEDA clusters.

## Prerequisites

- Phase 1 complete (controller creates Deployments with static replicas)
- API client working (`internal/stackweaver/client.go`)

## Implementation Order

### Step 1: Queue Depth API Endpoint (Stackweaver Platform)

**This is in the main Stackweaver repo, not the operator.**

**Create in stackweaver monorepo:**

| File | Purpose |
|------|---------|
| `backend/internal/api/v2/handlers/agent_pool_queue.go` | Handler for `GET /api/v2/agent-pools/:id/queue-depth` |
| `backend/internal/api/v2/routes/routes.go` | Register the new route |

**Handler logic:**
1. Authenticate via API key (same auth as runner endpoints)
2. Query: count runs with `status=pending` in the agent pool (Terraform)
3. Query: count jobs with `status=pending` in the agent pool (Ansible)
4. Query: count runners by status (online, busy, offline)
5. Return JSON:API response:

```json
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

**Must be lightweight** — called every 10-15 seconds per agent pool. Use count queries, not full row fetches.

### Step 2: KEDA External Scaler (gRPC Server)

**Create in operator:**

| File | Purpose |
|------|---------|
| `internal/scaler/server.go` | gRPC server setup, TLS config |
| `internal/scaler/handler.go` | ExternalScaler interface implementation |
| `internal/scaler/cache.go` | Thread-safe response cache (5s TTL) |
| `internal/scaler/metrics.go` | Prometheus metrics for scaler |
| `internal/scaler/handler_test.go` | Unit tests for all gRPC methods |

**gRPC interface** (from `docs/spec/02-scaler.md`):

```go
func (s *Scaler) IsActive(ctx, ref) → bool
  // Get queue depth → return totalPending > 0
  // On failure: return false (don't scale up on unknown)

func (s *Scaler) GetMetricSpec(ctx, ref) → []MetricSpec
  // Return metric name + targetSize from metadata

func (s *Scaler) GetMetrics(ctx, req) → []MetricValue
  // Get queue depth → return pending count
  // On failure: return cached value, or 0 if no cache

func (s *Scaler) StreamIsActive(ref, stream) → error
  // Poll every 10s, send on state change
```

**Secret resolution**: Scaler receives Secret name as metadata string. Reads Secret from K8s API using operator's ServiceAccount. Caches resolved keys for 60s.

**Dependencies**: Add `google.golang.org/grpc` and KEDA's external scaler proto to `go.mod`.

### Step 3: ScaledObject Builder

**Create**: `internal/controller/builder/scaledobject.go`

Builds a KEDA ScaledObject from a RunnerPool spec.

```go
func BuildScaledObject(pool *v1alpha1.RunnerPool, scalerAddress string) *kedav1alpha1.ScaledObject
```

**Fields mapped:**
- `scaleTargetRef.name` ← pool.name (Deployment)
- `minReplicaCount` ← pool.spec.minReplicas
- `maxReplicaCount` ← pool.spec.maxReplicas
- `cooldownPeriod` ← pool.spec.scaleDownDelaySeconds
- `pollingInterval` ← 15 (hardcoded)
- `triggers[0].type` ← "external"
- `triggers[0].metadata` ← agentPoolId, serverUrl, tokenSecretRef, tokenSecretKey, tokenSecretNamespace, runnerType, targetSize

**KEDA CRD dependency**: Import KEDA's API types. Use runtime discovery to check if ScaledObject CRD is registered (for fallback scaler logic).

### Step 4: RunnerPool Controller — KEDA Integration

**Modify**: `internal/controller/runnerpool_controller.go`

Update reconcile step 5 (currently sets static replicas) to:

```
5. Reconcile scaling
   - Check if ScaledObject CRD is registered (discovery)
   - If KEDA installed:
     - Build ScaledObject from spec
     - Create/update ScaledObject
     - Remove static replica count from Deployment (let KEDA manage)
     - Set condition ScalingActive=True
   - If KEDA not installed:
     - Start fallback scaler goroutine
     - Set condition ScalingActive=True with message "using built-in scaler"
```

**RBAC addition:**
```go
//+kubebuilder:rbac:groups=keda.sh,resources=scaledobjects,verbs=get;list;watch;create;update;patch;delete
```

### Step 5: Fallback Scaler

**Create**: `internal/controller/fallback_scaler.go`

For clusters without KEDA. Runs a goroutine per RunnerPool.

```go
type FallbackScaler struct {
    client    *stackweaver.Client
    k8sClient client.Client
    pool      *v1alpha1.RunnerPool
    stopCh    chan struct{}
}

func (f *FallbackScaler) Start(ctx context.Context)
  // Poll queue depth every 15s
  // Calculate: min(max, max(min, ceil(pending/jobsPerRunner)))
  // Update Deployment replicas
  // Respect cooldown before scaling down

func (f *FallbackScaler) Stop()
```

**Integration with controller:**
- Controller maintains a `map[types.NamespacedName]*FallbackScaler`
- On reconcile: if KEDA not installed, ensure fallback scaler is running for this pool
- On pool deletion: stop the fallback scaler
- If KEDA becomes available (CRD registered): stop all fallback scalers, create ScaledObjects

### Step 6: Scaler Sidecar in Manager

**Modify**: `cmd/main.go`

Start the gRPC scaler server alongside the controller manager:

```go
// Start gRPC scaler server
scalerServer := scaler.NewServer(scaler.Config{
    ListenAddr: ":9090",
    K8sClient:  mgr.GetClient(),
})
go func() {
    if err := scalerServer.Start(ctx); err != nil {
        setupLog.Error(err, "unable to start scaler server")
        os.Exit(1)
    }
}()
```

**Helm chart update**: Add scaler port (9090) to the operator Service and Deployment.

### Step 7: Integration Tests

**Test scenarios from `docs/spec/05-testing.md`:**

| # | Scenario |
|---|----------|
| 6 | Update scaling params → ScaledObject updated |
| 19-25 | KEDA scaler gRPC method tests |
| 26-29 | Fallback scaler tests (scale-up, scale-down with cooldown, maxReplicas cap, KEDA install transition) |

## API Changes Required in Stackweaver Platform

| Change | Location |
|--------|----------|
| New handler | `backend/internal/api/v2/handlers/agent_pool_queue.go` |
| New route | `backend/internal/api/v2/routes/routes.go` |
| Count queries | May need new repository methods in `core/repository/` for efficient pending job counts |

## Files Summary (Operator)

| Action | Path |
|--------|------|
| Create | `internal/scaler/server.go` |
| Create | `internal/scaler/handler.go` |
| Create | `internal/scaler/cache.go` |
| Create | `internal/scaler/metrics.go` |
| Create | `internal/scaler/handler_test.go` |
| Create | `internal/controller/builder/scaledobject.go` |
| Create | `internal/controller/fallback_scaler.go` |
| Modify | `internal/controller/runnerpool_controller.go` |
| Modify | `cmd/main.go` |
| Modify | `deploy/helm/stackweaver-operator/` (add scaler port + service) |
| Modify | `go.mod` (add grpc + KEDA API types) |
