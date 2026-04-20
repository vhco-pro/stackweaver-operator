---
status: draft
status_description: "Early draft, March 2026"
description: "Plan for a Kubernetes operator that auto-scales runner pods based on job demand"
author: "Michiel VH"
created: 2026-03-10
updated: 2026-04-17
covers:
  - "backend/cmd/runner/**"
  - "core/models/**"
  - "core/repository/agent_pool.go"
  - "backend/internal/api/v2/handlers/agent_pools.go"
  - "backend/internal/api/v2/routes/routes.go"
---
# Kubernetes Runner Operator — Plan

## Summary

Design and implement a Kubernetes operator/controller that automatically manages Stackweaver runner pods — scaling them up when jobs are pending and down when idle. This is analogous to the [GitHub Actions Runner Controller (ARC)](https://github.com/actions/actions-runner-controller) but for Stackweaver's self-hosted runner infrastructure.

## Motivation

Today, self-hosted runners are deployed manually (e.g. `docker run` or a static Kubernetes Deployment). Users must pre-provision a fixed number of runners, leading to either:

- **Over-provisioning**: Idle runners consuming cluster resources when no jobs exist.
- **Under-provisioning**: Pending jobs waiting because all runners are busy.

A Kubernetes operator solves this by dynamically spawning runner pods in response to pending jobs and terminating them when idle, providing elastic, cost-efficient runner infrastructure.

## Current Runner Architecture

Understanding the existing self-hosted runner flow is critical (see [SELF_HOSTED_RUNNERS_DESIGN.md](./SELF_HOSTED_RUNNERS_DESIGN.md)):

1. Runner starts in agent mode (`RUNNER_MODE=agent`)
2. Runner registers with the API (`POST /api/v2/runner/register`)
3. Runner enters heartbeat loop (polls `POST /api/v2/runner/heartbeat` every 10s)
4. API returns pending jobs matching the runner's agent pool and capabilities
5. Runner downloads artifacts, executes the job, streams output, uploads state
6. Runner marks job complete and returns to polling

The operator builds on top of this — it doesn't replace the runner agent protocol, it orchestrates the lifecycle of runner pods that use it.

## Design

### Custom Resource Definitions (CRDs)

#### `RunnerPool` (cluster-scoped or namespace-scoped)

```yaml
apiVersion: runner.stackweaver.io/v1alpha1
kind: RunnerPool
metadata:
  name: terraform-runners
  namespace: stackweaver
spec:
  # Connection to Stackweaver API
  serverUrl: https://stackweaver.example.com
  tokenSecretRef:
    name: stackweaver-runner-token
    key: token
  agentPoolId: "ap-xxxxxxxx"

  # Runner configuration
  runnerType: terraform          # terraform | ansible | combined
  runnerImage: ghcr.io/michielvha/stackweaver/runner:latest
  labels:
    - aws
    - us-east-1

  # Scaling
  minReplicas: 0                 # Scale to zero when idle
  maxReplicas: 10
  scaleDownDelaySeconds: 300     # Wait 5 min before scaling down idle runner
  jobsPerRunner: 1               # How many concurrent jobs per runner pod

  # Cloud pod identity (optional — for runners that need cloud credentials)
  cloudIdentity:
    provider: none             # none | aws | gcp | azure
    # AWS IRSA
    aws:
      roleArn: ""              # arn:aws:iam::123456789012:role/stackweaver-runner
    # GCP Workload Identity
    gcp:
      serviceAccount: ""       # runner@project.iam.gserviceaccount.com
    # Azure Workload Identity
    azure:
      clientId: ""             # Azure AD application client ID
      tenantId: ""             # Azure AD tenant ID

  # Pod template overrides
  template:
    metadata:
      annotations: {}
    spec:
      nodeSelector:
        workload: runners
      tolerations:
        - key: dedicated
          value: runners
          effect: NoSchedule
      resources:
        requests:
          cpu: "500m"
          memory: "512Mi"
        limits:
          cpu: "2"
          memory: "2Gi"
      volumes:
        - name: workspaces
          emptyDir:
            sizeLimit: 10Gi
      serviceAccountName: stackweaver-runner
```

**Multi-organization support**: Each `RunnerPool` targets a single agent pool, which belongs to one organization. To run runners for multiple organizations in the same cluster, deploy multiple `RunnerPool` resources — each with its own `agentPoolId` and `tokenSecretRef` pointing to an API key scoped to that organization.

#### `Runner` (namespace-scoped, managed by controller)

```yaml
apiVersion: runner.stackweaver.io/v1alpha1
kind: Runner
metadata:
  name: terraform-runners-abc123
  namespace: stackweaver
  ownerReferences:
    - apiVersion: runner.stackweaver.io/v1alpha1
      kind: RunnerPool
      name: terraform-runners
spec:
  poolRef: terraform-runners
status:
  phase: Idle              # Pending | Registering | Idle | Busy | Draining | Terminated | Error
  runnerId: "runner-uuid" # Stackweaver runner ID after registration
  currentJobs: 0
  lastJobFinished: "2026-03-10T12:00:00Z"
  podName: terraform-runners-abc123-pod
```

### Controller Architecture

```
┌──────────────────────────────────────────────────────────────┐
│                    Kubernetes Cluster                          │
│                                                               │
│  ┌────────────────────────────────────────────┐               │
│  │       Stackweaver Runner Operator          │               │
│  │                                            │               │
│  │  ┌─────────────────┐  ┌─────────────────┐  │               │
│  │  │  RunnerPool     │  │  KEDA External   │  │               │
│  │  │  Controller     │  │  Scaler (gRPC)   │──────┐          │
│  │  └────────┬────────┘  └─────────────────┘  │    │          │
│  │           │                                 │    │          │
│  └───────────│─────────────────────────────────┘    │          │
│              │                                      │          │
│              ▼              ┌──────────┐            │          │
│  ┌──────────────────┐       │  KEDA    │◀───────────┘          │
│  │  Runner Pods     │◀──────│  Scaler  │    poll queue depth   │
│  │  ┌────┐ ┌────┐   │ scale │          │            │          │
│  │  │ R1 │ │ R2 │   │       └──────────┘            ▼          │
│  │  └────┘ └────┘   │               ┌─────────────────┐       │
│  └──────────────────┘               │ Stackweaver API │       │
│                                     │ (external)      │       │
│                                     └─────────────────┘       │
└──────────────────────────────────────────────────────────────┘
```

### Scaling Logic (KEDA-Based)

Scaling is delegated to [KEDA](https://keda.sh/) via a custom **external scaler**. The operator ships a gRPC server that KEDA calls to get queue depth metrics.

#### KEDA External Scaler

The operator runs a gRPC server implementing the KEDA external scaler interface:

- `IsActive()` → returns true if pending jobs > 0 (triggers scale from zero).
- `GetMetricSpec()` → returns metric name and target value (e.g. 1 job per runner).
- `GetMetrics()` → polls `GET /api/v2/agent-pools/:id/queue-depth` and returns pending job count.

#### ScaledObject (auto-generated per RunnerPool)

```yaml
apiVersion: keda.sh/v1alpha1
kind: ScaledObject
metadata:
  name: terraform-runners-scaler
spec:
  scaleTargetRef:
    name: terraform-runners  # Deployment managed by RunnerPool controller
  minReplicaCount: 0         # From RunnerPool spec.minReplicas
  maxReplicaCount: 10        # From RunnerPool spec.maxReplicas
  cooldownPeriod: 300        # From RunnerPool spec.scaleDownDelaySeconds
  triggers:
    - type: external
      metadata:
        scalerAddress: stackweaver-operator-scaler:9090
        agentPoolId: "ap-xxxxxxxx"
```

KEDA handles all scaling decisions (stabilisation windows, cooldown, scale-from-zero). The operator focuses on pod lifecycle and runner registration.

#### Graceful Shutdown

- Runners must **never** be killed mid-job.
- Use Kubernetes `preStop` hook + `terminationGracePeriodSeconds` to allow in-progress jobs to complete.
- preStop hook writes a drain signal file (`/tmp/drain`) → runner agent detects it and stops accepting new jobs → completes current work → deregisters → exits.
- **KEDA cooldown vs termination grace period**: KEDA's `cooldownPeriod` (from `spec.scaleDownDelaySeconds`) controls how long KEDA waits before scaling down after metrics drop. Once KEDA decides to scale down, Kubernetes sends SIGTERM and the pod has `terminationGracePeriodSeconds` to finish. Set `terminationGracePeriodSeconds` high enough to cover worst-case job duration (e.g. 3600s for long Terraform applies). The runner agent already handles SIGTERM gracefully — it stops accepting new jobs, finishes the current one, deregisters, and exits.

### Reconcile Flow

```
RunnerPool Controller (continuous):

1. Read RunnerPool spec
2. Ensure KEDA ScaledObject exists and matches spec
3. Ensure runner Deployment exists with correct pod template
4. Monitor Runner CRs for lifecycle events

KEDA (every 10-15 seconds):

1. Calls external scaler → scaler polls Stackweaver API
2. Gets pending job count
3. Calculates desired replicas (pending jobs / jobs-per-runner)
4. Scales Deployment up or down

Pod Lifecycle (per runner pod):

1. Pod starts → runner registers with API in agent mode
2. Runner polls for jobs via heartbeat
3. On scale-down: preStop hook → drain signal file → finish current job → deregister → exit
```

## Implementation Phases

### Phase 1: Foundation

- Set up operator project scaffolding using [kubebuilder](https://kubebuilder.io/) or [operator-sdk](https://sdk.operatorframework.io/).
- Define CRDs (`RunnerPool`, `Runner`).
- Implement basic `RunnerPool` controller that creates/deletes runner pods.
- Static scaling only (fixed replica count, no auto-scaling).
- **Cloud pod identity**: Implement `spec.cloudIdentity` support (AWS IRSA, GCP Workload Identity, Azure Workload Identity) — controller creates annotated ServiceAccounts per `RunnerPool` based on the configured provider.
- Helm chart for deploying the operator.

### Phase 2: KEDA Auto-Scaling

- Add new API endpoint for queue depth (`GET /api/v2/agent-pools/:id/queue-depth`).
- Implement KEDA external scaler (gRPC server) that exposes queue depth as a metric.
- Configure `ScaledObject` per `RunnerPool` with KEDA handling scale-up/down decisions.
- Scale-to-zero support via KEDA's `minReplicaCount: 0`.
- Configurable cooldown and stabilisation windows.
- Optional built-in fallback scaler for clusters without KEDA.

### Phase 3: Ephemeral Runners & Graceful Lifecycle

- **Ephemeral mode**: Runner pod executes exactly one job then terminates (fresh environment per job, stronger isolation). Configured via `spec.ephemeral: true` on `RunnerPool`.
- **Long-lived mode** (default): Runner persists across jobs, benefits from workspace/provider caching.
- Implement graceful shutdown via preStop hook and drain signal file.
- `preStop` hook integration in runner pods.
- Runner deregistration on shutdown.
- Handle pod eviction and node drain scenarios.
- Liveness and readiness probes for runner pods.

### Phase 4: Observability and Hardening

- Prometheus metrics from the operator (queue depth, active runners, scale events).
- Kubernetes events for scale-up/down decisions.
- `RunnerPool` status conditions (e.g. `ScalingLimited`, `APIUnreachable`).
- Leader election for HA operator deployment.
- Rate limiting on scale-up to prevent thundering herd.
- Comprehensive integration tests.

### Phase 5: Advanced Features

- **Multiple runner pools**: Multiple `RunnerPool` resources with different configs (GPU nodes, high-memory, ARM, etc.), each targeting a different Stackweaver agent pool.
- **Webhook-driven scaling** (optional): Instead of polling, Stackweaver API sends webhooks on job creation for faster scale-up via KEDA HTTP scaler.
- **Multi-cloud identity per pool**: Support multiple cloud identity bindings on a single `RunnerPool` for runners that provision across clouds in a single job.

## API Changes Required

### New Endpoint: Queue Depth

```
GET /api/v2/agent-pools/:id/queue-depth
Authorization: Bearer <runner-api-key>

Response:
{
  "data": {
    "type": "queue-depths",
    "id": "<agent-pool-id>",
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

This endpoint is lightweight and safe for frequent polling (every 10-15s). It requires an API key with `runner:register` scope (reuse existing auth).

### API Key Provisioning

The operator requires an API key with `runner:register` scope to authenticate with the Stackweaver API. Users must create this key via the Stackweaver UI or API before deploying a `RunnerPool`, and store it in a Kubernetes Secret referenced by `spec.tokenSecretRef`. The operator Helm chart should document this prerequisite and optionally provide a setup job or CLI command to streamline key creation.

## Cloud Pod Identity

Runner pods frequently need cloud credentials to manage infrastructure (Terraform provisioning AWS/GCP/Azure resources, Ansible configuring cloud VMs, etc.). Static credentials in Secrets are a security anti-pattern — cloud pod identity is the modern standard. The operator provides first-class support via `spec.cloudIdentity` on the `RunnerPool` CRD.

When `spec.cloudIdentity.provider` is set, the controller automatically configures the runner pod's service account and annotations so that the cloud provider's identity system injects credentials into the pod at runtime. No static cloud credentials are needed.

### AWS — IAM Roles for Service Accounts (IRSA)

EKS clusters with an OIDC provider can bind IAM roles to Kubernetes service accounts. The controller:

1. Creates a dedicated ServiceAccount per `RunnerPool` (e.g. `terraform-runners-sa`).
2. Annotates it with `eks.amazonaws.com/role-arn: <spec.cloudIdentity.aws.roleArn>`.
3. Sets `spec.template.spec.serviceAccountName` on the runner Deployment to use this SA.
4. EKS automatically injects `AWS_ROLE_ARN` and `AWS_WEB_IDENTITY_TOKEN_FILE` env vars — Terraform's AWS provider picks these up natively.

```yaml
spec:
  cloudIdentity:
    provider: aws
    aws:
      roleArn: arn:aws:iam::123456789012:role/stackweaver-runner
```

**Prerequisites**: EKS cluster with OIDC provider enabled, IAM role trust policy allowing the ServiceAccount to assume it.

### GCP — Workload Identity

GKE clusters with Workload Identity bind Kubernetes SAs to Google Cloud SAs. The controller:

1. Creates a dedicated ServiceAccount per `RunnerPool`.
2. Annotates it with `iam.gke.io/gcp-service-account: <spec.cloudIdentity.gcp.serviceAccount>`.
3. Sets the SA on the runner Deployment.
4. GKE injects credentials via the metadata server — Terraform's Google provider uses Application Default Credentials (ADC) automatically.

```yaml
spec:
  cloudIdentity:
    provider: gcp
    gcp:
      serviceAccount: runner@my-project.iam.gserviceaccount.com
```

**Prerequisites**: GKE cluster with Workload Identity enabled, IAM binding between the Kubernetes SA and Google Cloud SA (`roles/iam.workloadIdentityUser`).

### Azure — Workload Identity

AKS clusters with Azure Workload Identity (federated credentials) bind Kubernetes SAs to Azure AD applications. The controller:

1. Creates a dedicated ServiceAccount per `RunnerPool`.
2. Annotates it with `azure.workload.identity/client-id: <spec.cloudIdentity.azure.clientId>`.
3. Labels the runner pods with `azure.workload.identity/use: "true"`.
4. Sets the SA on the runner Deployment.
5. The Azure Workload Identity webhook injects `AZURE_CLIENT_ID`, `AZURE_TENANT_ID`, and `AZURE_FEDERATED_TOKEN_FILE` — Terraform's AzureRM provider picks these up natively.

```yaml
spec:
  cloudIdentity:
    provider: azure
    azure:
      clientId: "00000000-0000-0000-0000-000000000000"
      tenantId: "00000000-0000-0000-0000-000000000000"
```

**Prerequisites**: AKS cluster with Azure Workload Identity enabled, federated credential configured on the Azure AD application trusting the Kubernetes SA.

### Relationship to Stackweaver OIDC Workload Identity

Cloud pod identity and Stackweaver's OIDC workload identity are complementary, not overlapping:

| Concern | Mechanism | Purpose |
|---------|-----------|---------|
| **Cloud pod identity** (this section) | IRSA / GCP WI / Azure WI | Runner pod gets cloud credentials to manage infrastructure (Terraform `aws_instance`, etc.) |
| **Stackweaver OIDC tokens** (existing) | `TFC_WORKLOAD_IDENTITY_TOKEN` JWT | Terraform can exchange a Stackweaver-issued JWT for cloud credentials via OIDC federation — an alternative to pod identity |

Users can use either or both. Cloud pod identity is simpler (no OIDC federation setup) but ties runners to a single cloud account. Stackweaver OIDC tokens are more flexible (one runner can target multiple accounts) but require OIDC federation configuration per cloud provider.

## Technology Choices

| Decision | Choice | Rationale |
|----------|--------|-----------|
| Framework | kubebuilder | Industry standard, generates boilerplate, strong community |
| Language | Go | Matches backend, kubebuilder native, team expertise |
| CRD API version | `v1alpha1` | Start unstable, promote to `v1beta1` → `v1` as design stabilises |
| Scaling | KEDA with custom external scaler | Mature, widely adopted, avoids reinventing scaling logic |
| Distribution | Helm chart + OLM bundle | Helm for direct install, OLM for marketplace/OpenShift |

## Comparison with GitHub Actions Runner Controller (ARC)

| Aspect | ARC | Stackweaver Operator |
|--------|-----|---------------------|
| Scaling signal | GitHub webhook (workflow_job) | KEDA external scaler polling queue depth |
| Runner lifecycle | Ephemeral by default | Long-lived default, ephemeral first-class |
| Job assignment | GitHub assigns to runner | Runner polls for jobs (pull-based) |
| Scale-to-zero | Supported | Supported |
| Runner image | GitHub-provided or custom | Stackweaver runner images (same as platform-hosted) |
| Multi-tenant | Runner groups per repo/org | Agent pools per org, scoped to workspaces/projects via RunnerPool CRs |

## Repository Structure

The operator is a standalone project in its own repository (`stackweaver-operator`), separate from the main Stackweaver platform. Since the operator has no shared code dependencies with the platform (it communicates purely via the Stackweaver API), a standalone repo provides cleaner separation of concerns, independent release cycles, and a simpler contributor experience.

```
stackweaver-operator/
├── api/
│   └── v1alpha1/
│       ├── runnerpool_types.go
│       ├── runner_types.go
│       └── groupversion_info.go
├── cmd/
│   └── main.go
├── config/
│   ├── crd/
│   ├── rbac/
│   ├── manager/
│   └── samples/
├── internal/
│   ├── controller/
│   │   ├── runnerpool_controller.go
│   │   └── runner_controller.go
│   └── scaler/
│       ├── external_scaler.go       # KEDA gRPC external scaler
│       └── stackweaver_client.go    # API client for queue depth
├── deploy/
│   └── helm/
│       └── stackweaver-operator/
├── Dockerfile
├── Makefile
└── go.mod
```

## Decisions

1. **Standalone repository** — The operator lives in its own repository (`stackweaver-operator`), separate from the main platform. It communicates with Stackweaver purely via the API and has no shared code dependencies, making a standalone repo the natural fit. This gives fully independent release cycles, a cleaner contributor experience, and avoids coupling the operator's Go module to the platform monorepo.
2. **Polling interval** — 10-15s default, configurable per `RunnerPool`. Too fast wastes API resources; too slow delays scale-up.
3. **Both ephemeral and long-lived runners are first-class** — Ephemeral mode (one job per pod, fresh environment, stronger isolation) and long-lived mode (workspace caching, faster startup for repeat jobs) are both supported. Long-lived is the default since IaC workloads benefit from cached state and providers.
4. **KEDA-first for scaling** — Use KEDA with a custom external scaler rather than building a proprietary scaling engine. KEDA is mature, widely adopted, and handles the hard parts (cooldown, stabilisation windows, metrics-driven scaling). The operator exposes queue depth as a KEDA external metric. This avoids reinventing the wheel and gives users a familiar scaling model. A built-in fallback scaler can be added later for clusters without KEDA — it should be minimal (simple poll-and-scale loop), not a full reimplementation of KEDA's stabilisation logic.
5. **Minimum Kubernetes version** — Target 1.34+.
