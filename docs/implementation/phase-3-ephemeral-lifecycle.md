# Phase 3: Ephemeral Runners & Graceful Lifecycle — Implementation Plan

## Goal

Add ephemeral runner mode (one job per pod) and harden the graceful shutdown flow so runners are never killed mid-job.

## Prerequisites

- Phase 2 complete (KEDA auto-scaling working)
- Runner agent already handles SIGTERM (confirmed in `backend/cmd/runner/agent_mode.go` lines 97-105)
- Runner agent already calls `deregister()` on shutdown (line 119)

## Implementation Order

### Step 1: Ephemeral Mode Support

**Modify**: `internal/controller/builder/pod_template.go`

When `spec.ephemeral=true`:

1. Set env var `RUNNER_EPHEMERAL=true`
2. Force `jobsPerRunner=1` in the ScaledObject target size regardless of `spec.jobsPerRunner` (log warning if user set >1). Note: `MAX_CONCURRENT_JOBS` is not currently read from env by the runner agent — the runner hardcodes 1. This enforcement is operator-side only (scaling math).
3. Runner agent must be updated to exit after one job — see Step 1b

**Modify (Stackweaver platform)**: `backend/cmd/runner/agent_mode.go`

Add ephemeral mode logic to the runner agent:

```go
// After job completion in heartbeatLoop():
if os.Getenv("RUNNER_EPHEMERAL") == "true" {
    log.Info("Ephemeral mode: exiting after job completion")
    a.deregister()
    os.Exit(0)
}
```

The runner exits cleanly with code 0 after completing one job. KEDA detects the reduced replica count and scales back up if more jobs are pending.

### Step 2: Graceful Shutdown — preStop Hook

**Already in pod template** from Phase 1: `touch /tmp/drain && sleep 5`

**Modify (Stackweaver platform)**: `backend/cmd/runner/agent_mode.go`

Add drain file detection to the runner agent. Currently it only handles SIGTERM. Add a filesystem watcher or polling check:

```go
// In heartbeatLoop(), before checking for new jobs:
if _, err := os.Stat("/tmp/drain"); err == nil {
    log.Info("Drain signal detected, stopping job acceptance")
    close(a.shutdown)
    return
}
```

This makes the drain mechanism work even before SIGTERM is sent (the preStop hook creates the file, then SIGTERM follows after the sleep).

### Step 3: terminationGracePeriodSeconds Propagation

**Already implemented** in Phase 1's pod template builder. Verify it correctly maps `spec.terminationGracePeriodSeconds` → pod spec.

Ensure the default (3600s) covers typical Terraform applies. Document in Helm chart NOTES.txt that users should increase this for long-running jobs.

### Step 4: PodDisruptionBudget Refinement

**Already created** in Phase 1 with `maxUnavailable: 1`. Phase 3 adds:

- Validate PDB is not created when `minReplicas=0`
- Verify PDB selector matches pod labels exactly
- Test: voluntary disruption (node drain) respects PDB

### Step 5: Runner Controller — Draining Phase

**Modify**: `internal/controller/runner_controller.go`

Enhance the draining detection:

1. Watch for pod `deletionTimestamp` being set → immediately set Runner CR `phase=Draining`
2. Set `status.message = "pod is terminating, waiting for job completion and deregistration"`
3. Continue polling API for runner status during drain
4. When API returns `status=offline` (deregistered) or pod is gone → `phase=Terminated`
5. Terminated is brief — ownerReferences garbage-collect the Runner CR

### Step 6: Finalizer — Drain Wait

**Modify**: `internal/controller/runnerpool_controller.go` (finalizer logic from Phase 1)

Enhance to properly wait for draining runners:

1. On RunnerPool deletion with finalizer:
   - Scale Deployment to 0 replicas (triggers SIGTERM on all pods)
   - Wait for all Runner CRs to reach phase=Terminated or Error
   - Poll `GET /api/v2/runners/{id}` for each — wait for `status=offline` (deregister marks offline, does not delete)
   - Timeout: 5 minutes (configurable)
   - On timeout: log warning, remove finalizer anyway
2. Remove finalizer → GC cleans up everything

### Step 7: Integration Tests

**Test scenarios from `docs/spec/05-testing.md`:**

| # | Scenario |
|---|----------|
| 30 | Scale down idle runner → deregisters, exits 0 |
| 31 | Scale down busy runner → waits for job completion |
| 32 | RunnerPool deletion during jobs → finalizer holds, then cleanup |
| 33 | Ephemeral + jobsPerRunner>1 → forced to 1, warning logged |

**E2E test scenario 4**: Ephemeral mode — create pool, inject 1 job, verify runner starts, completes, exits, and is not restarted unless new jobs pending.

## Platform Changes Required

| Change | Location | Purpose |
|--------|----------|---------|
| Ephemeral exit | `backend/cmd/runner/agent_mode.go` | Exit after one job when RUNNER_EPHEMERAL=true |
| Drain file detection | `backend/cmd/runner/agent_mode.go` | Watch for /tmp/drain as early shutdown signal |

## Files Summary (Operator)

| Action | Path |
|--------|------|
| Modify | `internal/controller/builder/pod_template.go` (ephemeral enforcement) |
| Modify | `internal/controller/runner_controller.go` (draining phase) |
| Modify | `internal/controller/runnerpool_controller.go` (finalizer drain wait) |
| Modify | `internal/controller/runnerpool_controller_test.go` |
| Modify | `internal/controller/runner_controller_test.go` |
