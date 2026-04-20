# Phase 4: Observability & Hardening — Implementation Plan

## Goal

Add Prometheus metrics, Kubernetes events, leader election for HA, and rate limiting. Make the operator production-ready.

## Prerequisites

- Phase 3 complete (ephemeral mode + graceful shutdown working)

## Implementation Order

### Step 1: Prometheus Metrics

**Create**: `internal/metrics/metrics.go`

Register all metrics from the consolidated table in `docs/spec/README.md`:

```go
var (
    // Controller metrics
    ReconcileTotal = prometheus.NewCounterVec(...)        // controller, result
    ReconcileDuration = prometheus.NewHistogramVec(...)    // controller
    RunnerPoolCount = prometheus.NewGaugeVec(...)          // phase
    RunnerCount = prometheus.NewGaugeVec(...)              // pool, phase

    // Scaler metrics (already partially in internal/scaler/metrics.go from Phase 2)
    // Move here for consolidation

    // Shutdown metrics
    ShutdownDuration = prometheus.NewHistogramVec(...)     // pool, reason
    ForcedTerminations = prometheus.NewCounterVec(...)     // pool
    GracefulShutdowns = prometheus.NewCounterVec(...)      // pool
    DrainActive = prometheus.NewGaugeVec(...)              // pool
)
```

**Integration points:**
- RunnerPool controller: increment ReconcileTotal + observe ReconcileDuration on every reconcile
- RunnerPool controller: update RunnerPoolCount gauge after status update
- Runner controller: update RunnerCount gauge after phase transitions
- Runner controller: increment shutdown metrics on drain/termination

controller-runtime already exposes its own metrics (work queue depth, reconcile counts). These are in addition.

### Step 2: Kubernetes Events

**Modify**: `internal/controller/runnerpool_controller.go`, `runner_controller.go`

Add an `EventRecorder` to both controllers and emit events:

| Event | Type | Reason | Object | Message |
|-------|------|--------|--------|---------|
| RunnerPool created | Normal | Created | RunnerPool | "Created Deployment {name} with {n} replicas" |
| RunnerPool scaled up | Normal | ScaledUp | RunnerPool | "Scaled from {old} to {new} replicas ({pending} pending jobs)" |
| RunnerPool scaled down | Normal | ScaledDown | RunnerPool | "Scaled from {old} to {new} replicas (cooldown expired)" |
| API unreachable | Warning | APIUnreachable | RunnerPool | "Cannot reach Stackweaver API at {url}: {error}" |
| API recovered | Normal | APIRecovered | RunnerPool | "Stackweaver API at {url} is reachable again" |
| Runner registered | Normal | Registered | Runner | "Runner {name} registered with ID {id}" |
| Runner heartbeat lost | Warning | HeartbeatLost | Runner | "No heartbeat from runner {name} for {duration}" |
| Runner forced kill | Warning | ForcedTermination | Runner | "Runner {name} killed after exceeding termination grace period" |
| Token Secret missing | Warning | SecretNotFound | RunnerPool | "Secret {name} not found in namespace {ns}" |
| Fallback scaler active | Warning | FallbackScaler | RunnerPool | "KEDA not installed, using built-in fallback scaler" |

**Setup**: Add `record.EventRecorder` to both reconciler structs. Inject via `mgr.GetEventRecorderFor("stackweaver-operator")` in `cmd/main.go`.

### Step 3: Leader Election

**Already scaffolded** in `cmd/main.go` — kubebuilder generated leader election support using Leases. Just needs to be verified and tested:

- Flag `--leader-elect` enables it (default in production deployments)
- Lease resource in operator namespace
- Only the leader runs reconcile loops
- Standby replica takes over within ~15s if leader dies

**Helm chart**: Set `replicaCount: 2` in HA mode with leader election enabled.

**Test**: Deploy 2 replicas, verify only one is reconciling (check logs). Kill the leader pod, verify standby takes over.

### Step 4: Rate Limiting on Scale-Up

**Modify**: `internal/controller/runnerpool_controller.go` and `internal/controller/fallback_scaler.go`

Prevent thundering herd when many jobs arrive simultaneously:

```go
// In reconcile or fallback scaler:
maxScaleUpPerCycle := 5  // Add at most 5 runners per reconcile/poll cycle
desired := calculateDesiredReplicas(pending, jobsPerRunner, min, max)
current := deployment.Spec.Replicas
if desired - current > maxScaleUpPerCycle {
    desired = current + maxScaleUpPerCycle
    // Requeue quickly to continue scaling up
    return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
}
```

For KEDA-managed scaling: KEDA has its own stabilisation window. The operator can set `advanced.horizontalPodAutoscalerConfig.behavior` on the ScaledObject to limit scale velocity:

```yaml
advanced:
  horizontalPodAutoscalerConfig:
    behavior:
      scaleUp:
        stabilizationWindowSeconds: 30
        policies:
          - type: Pods
            value: 5
            periodSeconds: 60
```

### Step 5: RunnerPool Status Conditions — Full Coverage

**Modify**: `internal/controller/runnerpool_controller.go`

Ensure all 4 conditions are properly maintained:

| Condition | Set True When | Set False When |
|-----------|--------------|----------------|
| Available | readyReplicas >= minReplicas | readyReplicas < minReplicas |
| ScalingActive | KEDA ScaledObject is active OR fallback scaler running | Neither active |
| APIReachable | Last API call succeeded | Circuit breaker open |
| Degraded | Any Runner in Error phase | All runners healthy |

### Step 6: Comprehensive Integration Tests

**Additional test scenarios:**

| # | Scenario |
|---|----------|
| Events emitted on scale-up/down |
| Events emitted on API failure/recovery |
| Metrics exported and queryable via /metrics |
| Leader election — only leader reconciles |
| Rate limiting — scale-up capped per cycle |
| Status conditions correctly reflect aggregate state |

## Files Summary

| Action | Path |
|--------|------|
| Create | `internal/metrics/metrics.go` |
| Modify | `internal/controller/runnerpool_controller.go` (events, metrics, rate limiting, conditions) |
| Modify | `internal/controller/runner_controller.go` (events, metrics) |
| Modify | `internal/controller/fallback_scaler.go` (rate limiting) |
| Modify | `internal/controller/builder/scaledobject.go` (HPA behavior config) |
| Modify | `cmd/main.go` (event recorder injection) |
| Modify | `deploy/helm/stackweaver-operator/values.yaml` (HA defaults) |
