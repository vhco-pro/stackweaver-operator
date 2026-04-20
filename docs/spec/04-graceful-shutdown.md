# Graceful Shutdown Specification

This document specifies how runner pods are safely terminated without interrupting in-progress jobs.

## Problem

IaC jobs (Terraform apply, Ansible playbook runs) can take minutes to hours. Killing a runner mid-job can leave infrastructure in a partially applied, inconsistent state. The operator must guarantee that no runner is terminated while a job is in progress.

## Shutdown Triggers

Runner pods can be terminated by:

1. **KEDA scale-down**: Pending jobs dropped, KEDA reduces Deployment replicas
2. **RunnerPool deletion**: User deletes the RunnerPool CR
3. **RunnerPool spec change**: Rolling update triggers pod replacement
4. **Node drain**: Kubernetes node maintenance (`kubectl drain`)
5. **Pod eviction**: Resource pressure or preemption
6. **Manual pod deletion**: User deletes a pod directly

All of these result in Kubernetes sending SIGTERM to the pod. The behavior is the same regardless of trigger.

## Shutdown Flow

```
SIGTERM received by pod
         │
         ▼
  preStop hook runs
  (writes /tmp/drain file)
         │
         ▼
  Runner agent detects drain signal
         │
         ├─── No active job ──► Deregister from API ──► Exit 0
         │
         └─── Active job ──► Stop accepting new jobs
                                      │
                                      ▼
                              Finish current job
                                      │
                                      ▼
                              Upload results to API
                                      │
                                      ▼
                              Deregister from API
                                      │
                                      ▼
                                   Exit 0
```

### preStop Hook

The pod template includes a preStop lifecycle hook:

```yaml
lifecycle:
  preStop:
    exec:
      command:
        - /bin/sh
        - -c
        - "touch /tmp/drain && sleep 5"
```

The runner agent watches for `/tmp/drain` as the drain signal. The 5-second sleep ensures Kubernetes has time to remove the pod from service endpoints before the container begins shutting down.

Alternative: The preStop hook could call an HTTP endpoint on the runner agent (`POST /drain`) instead of using a file signal. This is cleaner but requires the runner agent to expose an HTTP server for lifecycle management.

### Runner Agent SIGTERM Handler

The runner agent binary already handles SIGTERM (this exists in the current codebase):

1. Stop the heartbeat loop (stop accepting new jobs from the orchestrator)
2. If a job is in progress, wait for it to complete
3. Upload final job results (state file, logs) to the Stackweaver API
4. Deregister the runner from the API (`DELETE /api/v2/runners/{id}` or status update)
5. Exit with code 0

### terminationGracePeriodSeconds

This is the hard deadline. If the pod hasn't exited by this time, Kubernetes sends SIGKILL.

The RunnerPool CRD has `spec.terminationGracePeriodSeconds` (default: 3600 = 1 hour). This should be set to cover the worst-case job duration:

| Workload Type | Recommended Value | Rationale |
|---------------|-------------------|-----------|
| Short Terraform plans | 300 (5 min) | Plans are fast |
| Terraform applies | 3600 (1 hour) | Large infra changes can take 30+ min |
| Ansible playbooks | 7200 (2 hours) | Complex playbooks with many hosts |
| Mixed / unknown | 3600 (1 hour) | Safe default |

If a job exceeds `terminationGracePeriodSeconds`, it will be forcefully killed. This is an operational concern that should be documented and surfaced via metrics.

## Pod Disruption Budget

The RunnerPool controller creates a PodDisruptionBudget (PDB) for each pool to protect running jobs during voluntary disruptions (node drains, cluster upgrades):

```yaml
apiVersion: policy/v1
kind: PodDisruptionBudget
metadata:
  name: {pool.name}
  ownerReferences: [{RunnerPool}]
spec:
  # Allow at most 1 pod to be disrupted at a time
  maxUnavailable: 1
  selector:
    matchLabels:
      app.kubernetes.io/name: {pool.name}
      app.kubernetes.io/managed-by: stackweaver-operator
```

For pools with `minReplicas: 0`, the PDB is omitted (can't set minAvailable on a scale-to-zero pool).

The PDB only covers voluntary disruptions. Involuntary disruptions (OOM kill, node failure) bypass the PDB. The `terminationGracePeriodSeconds` and runner agent SIGTERM handler are the last line of defense.

## Ephemeral Mode Considerations

In ephemeral mode (`spec.ephemeral: true`), the runner exits after completing one job. This interacts with graceful shutdown:

- The runner agent exits with code 0 after job completion (not a crash)
- Kubernetes sees exit 0 and does NOT restart the pod (restartPolicy is handled by the Deployment controller and KEDA)
- KEDA detects the reduced replica count and pending jobs, and scales back up if needed
- No SIGTERM needed in the normal case — the pod simply exits

SIGTERM is still relevant for ephemeral runners when:
- KEDA scales down before the runner picks up a job (runner is idle)
- The RunnerPool is deleted while a job is in progress
- A node drain occurs mid-job

**Ephemeral + jobsPerRunner**: When `ephemeral: true`, `jobsPerRunner` is forced to 1 by the controller (regardless of what the user sets). Ephemeral mode means "one job, one pod" — allowing concurrent jobs would defeat the isolation guarantee. The controller logs a warning if the user sets both `ephemeral: true` and `jobsPerRunner > 1`.

## Runner Controller Integration

The Runner controller updates the Runner CR status during shutdown:

1. When pod enters terminating state (deletionTimestamp set) → `status.phase = Draining`
2. When pod is terminating → Runner CR stays in Draining until pod is deleted
3. When pod is deleted → Runner CR is garbage-collected

The RunnerPool controller uses Runner CR phases to report aggregate status:
- If any Runner is Draining → RunnerPool `status.phase` stays Ready (normal operation)
- If all Runners are Draining → RunnerPool `status.phase` = Scaling (scaling down)

## Failure Modes

| Scenario | Behavior |
|----------|----------|
| Runner crashes mid-job (OOM, panic) | Job fails. Stackweaver API marks job as errored after heartbeat timeout. Infrastructure may be in inconsistent state — user must reconcile manually. |
| SIGKILL after grace period | Same as crash. Job fails on timeout. Metrics record forced termination. |
| API unreachable during deregistration | Runner exits anyway. API marks runner offline after heartbeat timeout (default 60s). Orphaned runner record cleaned up by API's own pruning. |
| Node failure (sudden power loss) | Same as crash. No graceful shutdown possible. KEDA scales up replacement. |

## Metrics

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `stackweaver_runner_shutdown_duration_seconds` | Histogram | `pool`, `reason` | Time from SIGTERM to pod exit |
| `stackweaver_runner_forced_terminations_total` | Counter | `pool` | Pods killed by SIGKILL after grace period |
| `stackweaver_runner_graceful_shutdowns_total` | Counter | `pool` | Successful graceful shutdowns |
| `stackweaver_runner_drain_active` | Gauge | `pool` | Number of runners currently draining |
