# Phase 1: Foundation — Implementation Plan

## Goal

Get a working operator that creates runner Deployments from RunnerPool CRs with static replica counts, cloud identity support, and a Helm chart. No auto-scaling yet — that's Phase 2.

## Prerequisites

- Kubebuilder scaffolding: done
- CRD types: done (`api/v1alpha1/runnerpool_types.go`, `runner_types.go`)
- CRD manifests generated: done (`config/crd/bases/`)
- Controller stubs: done (`internal/controller/`)

## Implementation Order

The steps below are ordered by dependency — each builds on the previous.

### Step 1: Stackweaver API Client

**Create**: `internal/stackweaver/client.go`, `internal/stackweaver/types.go`

This is the foundation everything else depends on. Implements the client spec from `docs/spec/03-api-client.md`.

**Files to create:**

| File | Purpose |
|------|---------|
| `internal/stackweaver/client.go` | HTTP client with auth, retry, circuit breaker |
| `internal/stackweaver/types.go` | Go types for JSON:API responses (QueueDepth, Runner, etc.) |
| `internal/stackweaver/errors.go` | APIError type, error classification (retryable vs not) |
| `internal/stackweaver/client_test.go` | Unit tests: response parsing, retry logic, circuit breaker state machine |

**Key behaviors:**
- Constructor takes `ClientConfig{ServerURL, APIKey, HTTPClient, UserAgent}`
- Methods: `GetQueueDepth(ctx, agentPoolID)`, `ListRunners(ctx, agentPoolID)`, `GetRunner(ctx, runnerID)` 
- Exponential backoff: 1s initial, 2x multiplier, 30s max, 3 retries, 20% jitter
- Circuit breaker: open after 5 failures, half-open after 60s
- All responses parsed from JSON:API format
- Pagination handling for ListRunners

**Reference**: The runner agent uses similar API calls — see `backend/cmd/runner/agent_mode.go` for the request/response patterns. The operator client is read-only (no register/heartbeat/job calls).

### Step 2: Pod Template Builder

**Create**: `internal/controller/builder/pod_template.go`

Extracts the pod template construction logic from `docs/spec/01-controller.md` into a pure function that's easy to unit test.

**Files to create:**

| File | Purpose |
|------|---------|
| `internal/controller/builder/pod_template.go` | `BuildPodTemplate(pool *v1alpha1.RunnerPool) corev1.PodTemplateSpec` |
| `internal/controller/builder/service_account.go` | `BuildServiceAccount(pool *v1alpha1.RunnerPool) corev1.ServiceAccount` |
| `internal/controller/builder/deployment.go` | `BuildDeployment(pool *v1alpha1.RunnerPool) appsv1.Deployment` |
| `internal/controller/builder/pdb.go` | `BuildPDB(pool *v1alpha1.RunnerPool) policyv1.PodDisruptionBudget` |
| `internal/controller/builder/builder_test.go` | Table-driven tests for all builders |

**Pod template must include:**
- Labels: `app.kubernetes.io/name`, `managed-by`, `component`, `runner.stackweaver.io/pool`
- Azure WI label when `cloudIdentity.provider=azure`
- Env vars: `RUNNER_MODE=agent`, `STACKWEAVER_SERVER` (not `STACKWEAVER_URL`), `STACKWEAVER_TOKEN` (secretKeyRef), `RUNNER_AGENT_POOL_ID` (not `AGENT_POOL_ID`), `RUNNER_NAME` (set to pod name for unique identification), `RUNNER_LABELS`, `POLL_INTERVAL`
- Note: `RUNNER_TYPE` is NOT an env var — the runner infers type from installed tool versions at registration. The Terraform runner and Ansible runner are separate binaries (`backend/cmd/runner` vs `backend/cmd/ansible-runner`), so `runnerType` on the CRD controls which image is used, not an env var.
- Note: `MAX_CONCURRENT_JOBS` is NOT currently read from an env var — the runner hardcodes `1`. Acceptable for Phase 1. If `jobsPerRunner > 1` support is needed, the runner agent code must be updated first.
- `RUNNER_EPHEMERAL` does not exist yet — added in Phase 3
- preStop hook: `touch /tmp/drain && sleep 5`
- Liveness/readiness probes on port 8081

**Important**: The runner agent does NOT currently expose `/healthz` or `/readyz` endpoints. The probes will need to be either:
- (a) removed from Phase 1 and added when the runner agent gets health endpoints, or
- (b) use a TCP check on a port, or
- (c) use an exec probe that checks the process is running

Decision: use exec probe `["pgrep", "-x", "runner"]` for liveness in Phase 1 (exact match avoids false positives). Revisit when runner agent gets proper health endpoints.

**Ephemeral + jobsPerRunner**: If `ephemeral=true`, force `jobsPerRunner=1` and log a warning.

### Step 3: RunnerPool Controller — Core Reconcile

**Modify**: `internal/controller/runnerpool_controller.go`

Implement the reconcile loop from `docs/spec/01-controller.md`, steps 1-6 (without KEDA — static replicas only).

**Reconcile logic:**

```
1. Fetch RunnerPool CR (return if deleted)
2. Add finalizer runner.stackweaver.io/cleanup if not present
3. If being deleted → run finalizer (Step 5 below)
4. Validate: tokenSecretRef exists, serverUrl HEAD check (cached 60s)
5. Reconcile ServiceAccount (with cloud identity annotations)
6. Reconcile Deployment (using builders from Step 2)
   - Set replicas = minReplicas (static, no KEDA yet)
7. Reconcile PDB (if minReplicas > 0)
8. Update status: phase, currentReplicas, readyReplicas, conditions
```

**RBAC markers needed** (add to controller file):
```go
//+kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups="",resources=serviceaccounts,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch
//+kubebuilder:rbac:groups="",resources=events,verbs=create;patch
//+kubebuilder:rbac:groups=policy,resources=poddisruptionbudgets,verbs=get;list;watch;create;update;patch;delete
```

**Watch setup** in `SetupWithManager`:
- Watch RunnerPool CRs (primary)
- Watch owned Deployments (via `Owns(&appsv1.Deployment{})`)
- Watch owned ServiceAccounts
- Watch referenced Secrets (via `Watches` with enqueue mapper)

**Phase transitions:**
- Create → Pending
- Owned resources created → Provisioning
- Deployment available → Ready
- Validation failure → Error

### Step 4: Runner Controller — Pod Lifecycle Tracking

**Modify**: `internal/controller/runner_controller.go`

Implement the Runner controller from `docs/spec/01-controller.md` (Runner Controller section).

**Reconcile logic:**

```
1. Watch pods with label app.kubernetes.io/managed-by=stackweaver-operator
2. On pod create → create Runner CR, phase=Pending, condition PodReady=False
3. On pod running → phase=Registering, PodReady=True
4. Poll API (ListAgents or ListRunners) → match by runner name (= pod name) → set runnerId, phase=Idle, Registered=True
5. On API status=busy → phase=Busy, update currentJobs
6. On API status=online + currentJobs=0 → phase=Idle, update lastJobFinished
7. On pod terminating → phase=Draining
8. On pod deleted → phase=Terminated (brief, then GC'd)
9. Heartbeat monitoring: if lastHeartbeat > 60s stale → Healthy=False
```

**RBAC markers:**
```go
//+kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch
```

**API polling strategy:**

The existing API has two endpoints for listing runners in a pool:
- `GET /api/v2/agent-pools/{id}/agents` — TFE-compatible, returns `type: "agents"` with name, status, last-ping-at. Does NOT include `current-jobs`.
- `GET /api/v2/organizations/{name}/runners?filter[agent_pool_id]={id}` — returns full runner data including `current-jobs`, `last-heartbeat-at`, labels.

The organization runners endpoint has all the fields we need but requires the org name. Two options:
1. **Option A**: Use the `/agents` endpoint for basic status + heartbeat, use `GET /api/v2/runners/{id}` for individual runner detail when needed (extra calls but no org name dependency).
2. **Option B**: Add a new `GET /api/v2/agent-pools/{id}/runners` endpoint to the platform that returns full runner data without requiring org name.

**Decision for Phase 1**: Use Option A. The `/agents` endpoint gives us name + status + last-ping-at for batch sync. For `currentJobs`, fall back to `GET /api/v2/runners/{id}` only for runners in `Busy` status. This avoids platform changes in Phase 1.

- Cache responses for 10 seconds
- Stagger polling across runners in a pool
- Exponential backoff on API errors (15s → 30s → 60s → 2m cap)

### Step 5: Finalizer — Cleanup on Deletion

**In**: `internal/controller/runnerpool_controller.go`

When a RunnerPool is deleted:

1. Set all owned Runner CRs to phase=Draining
2. Poll `GET /api/v2/runners/{id}` for each runner — wait for `status=offline` (deregistration marks offline, does NOT delete the record or return 404)
3. Timeout after 5 minutes — proceed with cleanup anyway
4. Remove finalizer → Kubernetes GCs owned resources

Note: The platform's `POST /api/v2/runner/deregister` sets the runner record to `offline` status. It does not delete the record. The operator can optionally call `DELETE /api/v2/runners/{id}` to clean up the record entirely, but this is not strictly necessary — offline runners are harmless.

### Step 6: Helm Chart

**Create**: `deploy/helm/stackweaver-operator/`

Based on `docs/spec/06-helm-chart.md`. Uses the kustomize output as a starting point but restructured as a proper Helm chart.

**Files:**

```
deploy/helm/stackweaver-operator/
├── Chart.yaml
├── values.yaml
├── crds/
│   ├── runner.stackweaver.io_runnerpools.yaml  (copied from config/crd/bases/)
│   └── runner.stackweaver.io_runners.yaml
├── templates/
│   ├── _helpers.tpl
│   ├── deployment.yaml
│   ├── serviceaccount.yaml
│   ├── clusterrole.yaml
│   ├── clusterrolebinding.yaml
│   ├── service.yaml          (metrics service)
│   └── NOTES.txt
└── README.md
```

For Phase 1, the scaler sidecar and ServiceMonitor are not included (Phase 2 and 4 respectively).

### Step 7: Integration Tests

**Modify**: `internal/controller/runnerpool_controller_test.go`, `runner_controller_test.go`

Using the envtest framework already scaffolded in `suite_test.go`.

**Test scenarios from `docs/spec/05-testing.md`:**

| # | Scenario | Phase 1? |
|---|----------|----------|
| 1 | Create RunnerPool → Deployment + SA created, phase=Ready | Yes |
| 2 | Create with AWS cloudIdentity → SA has IRSA annotation | Yes |
| 3 | Create with GCP cloudIdentity → SA has GCP annotation | Yes |
| 4 | Create with Azure cloudIdentity → SA + pod label | Yes |
| 5 | Update runnerImage → Deployment updated | Yes |
| 7 | Delete RunnerPool → owned resources GC'd | Yes |
| 9 | Invalid token Secret → phase=Error | Yes |
| 10 | KEDA not installed → static replicas | Yes (default behavior in P1) |
| 11 | Spec validation → CRD rejects | Yes |
| 12-18 | Runner controller scenarios | Yes (with mock API) |

## Verification

After Phase 1 is complete:

```bash
# Build and run locally against a kind cluster
make docker-build IMG=stackweaver-operator:dev
kind load docker-image stackweaver-operator:dev
make deploy IMG=stackweaver-operator:dev

# Apply a RunnerPool CR
kubectl apply -f config/samples/runner_v1alpha1_runnerpool.yaml

# Verify owned resources created
kubectl get deployment terraform-runners
kubectl get serviceaccount terraform-runners-runner
kubectl get runnerpool terraform-runners -o jsonpath='{.status.phase}'
# Expected: Ready (or Error if no real Stackweaver API)

# Run integration tests
GOWORK=off make test

# Install via Helm
helm install stackweaver-operator deploy/helm/stackweaver-operator/ \
  --namespace stackweaver-system --create-namespace
```

## Files Summary

| Action | Path |
|--------|------|
| Create | `internal/stackweaver/client.go` |
| Create | `internal/stackweaver/types.go` |
| Create | `internal/stackweaver/errors.go` |
| Create | `internal/stackweaver/client_test.go` |
| Create | `internal/controller/builder/pod_template.go` |
| Create | `internal/controller/builder/service_account.go` |
| Create | `internal/controller/builder/deployment.go` |
| Create | `internal/controller/builder/pdb.go` |
| Create | `internal/controller/builder/builder_test.go` |
| Modify | `internal/controller/runnerpool_controller.go` |
| Modify | `internal/controller/runner_controller.go` |
| Modify | `internal/controller/runnerpool_controller_test.go` |
| Modify | `internal/controller/runner_controller_test.go` |
| Create | `deploy/helm/stackweaver-operator/` (chart) |
