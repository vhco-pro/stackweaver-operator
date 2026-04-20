# Controller Specification

This document specifies the behavior of the two controllers in the Stackweaver Runner Operator: the **RunnerPool controller** and the **Runner controller**.

## RunnerPool Controller

The RunnerPool controller is the primary controller. It watches `RunnerPool` custom resources and manages the lifecycle of all owned Kubernetes resources: a Deployment (containing runner pods), a ServiceAccount (optionally annotated for cloud identity), a KEDA ScaledObject (for auto-scaling), and a PodDisruptionBudget (for safe voluntary disruptions).

### Owned Resources

For each `RunnerPool` CR, the controller creates and manages:

| Resource | Name Pattern | Purpose |
|----------|-------------|---------|
| Deployment | `{pool.name}` | Runs runner pods |
| ServiceAccount | `{pool.name}-runner` | Pod identity; annotated for cloud providers |
| ScaledObject | `{pool.name}-scaler` | KEDA auto-scaling trigger (omitted when KEDA not installed) |
| PodDisruptionBudget | `{pool.name}` | Protect running jobs during voluntary disruptions (omitted when minReplicas=0) |
| Runner CR(s) | `{pool.name}-{hash}` | One per pod, tracks individual runner state |

All owned resources have `ownerReferences` pointing to the `RunnerPool`, so Kubernetes garbage collection handles cleanup on deletion.

### Reconcile Loop

The controller reconciles on:
- `RunnerPool` create/update/delete events
- Changes to owned Deployments, ServiceAccounts, ScaledObjects
- Periodic resync (default 5 minutes)

#### Reconcile Steps

```
1. Fetch RunnerPool CR
   - If not found (deleted) → return (ownerReferences handle cleanup)
   - If being deleted (deletionTimestamp set) → run finalizer logic → return

2. Validate spec
   - serverUrl is reachable (lightweight HEAD request, cached 60s, single failure sets condition but does not block reconcile — only 3+ consecutive failures set phase=Error)
   - tokenSecretRef.name exists and contains tokenSecretRef.key
   - If cloudIdentity.provider != none, validate provider-specific fields are set
   - On validation failure → set phase=Error, condition APIReachable=False → requeue with backoff

3. Reconcile ServiceAccount
   - Ensure ServiceAccount {pool.name}-runner exists
   - If cloudIdentity is configured, apply provider-specific annotations:
     - AWS: eks.amazonaws.com/role-arn: {roleArn}
     - GCP: iam.gke.io/gcp-service-account: {serviceAccount}
     - Azure: azure.workload.identity/client-id: {clientId}
   - If cloudIdentity changed, update annotations (pods pick up changes on restart)

4. Reconcile Deployment
   - Build desired Deployment spec from RunnerPool spec (see Pod Template below)
   - If Deployment doesn't exist → create it, set phase=Provisioning
   - If Deployment exists but spec differs → update it
   - When KEDA is managing scaling: do NOT set replica count — KEDA owns it via ScaledObject
   - When using fallback scaler: controller sets replica count directly (see Fallback Scaler below)

5. Reconcile scaling
   - Check if KEDA is installed (ScaledObject CRD registered in the cluster)
   - **If KEDA is installed:**
     - Build desired ScaledObject from RunnerPool spec
     - Map: minReplicas, maxReplicas, scaleDownDelaySeconds → ScaledObject fields
     - Configure external scaler trigger pointing to operator's gRPC scaler (see 02-scaler.md)
     - Include jobsPerRunner as `targetSize` in trigger metadata
     - If ScaledObject doesn't exist → create it
     - If ScaledObject exists but spec differs → update it
   - **If KEDA is NOT installed:**
     - Log warning: "KEDA not detected, using built-in fallback scaler"
     - Do NOT create a ScaledObject
     - Start fallback poll loop for this RunnerPool (see Fallback Scaler below)
     - Set condition `ScalingActive=True` with message "using built-in scaler (KEDA not installed)"

6. Reconcile PodDisruptionBudget
   - If minReplicas > 0: ensure PDB exists with maxUnavailable=1 (see 04-graceful-shutdown.md)
   - If minReplicas = 0: delete PDB if it exists (can't use PDB with scale-to-zero)

7. Update status
   - Read Deployment status → set currentReplicas
   - Count Runner CRs by phase → set readyReplicas, busyReplicas
   - Poll queue depth via API client → set pendingJobs
   - If scaling just occurred → set lastScaleTime to now
   - Set phase based on aggregate state (see Phase Transitions below)
   - Set conditions
```

### Pod Template Construction

The controller builds the runner pod template from the `RunnerPool` spec:

```yaml
metadata:
  labels:
    # Required labels — used by Runner controller watch predicate and PDB selector
    app.kubernetes.io/name: {pool.name}
    app.kubernetes.io/managed-by: stackweaver-operator
    app.kubernetes.io/component: runner
    runner.stackweaver.io/pool: {pool.name}
    # Azure Workload Identity (only when cloudIdentity.provider=azure)
    azure.workload.identity/use: "true"
    # Additional labels from spec.template.metadata.labels
  annotations:
    # Additional annotations from spec.template.metadata.annotations
spec:
  containers:
    - name: runner
      image: {spec.runnerImage or default per runnerType}
      env:
        - name: RUNNER_MODE
          value: "agent"
        - name: STACKWEAVER_URL
          value: {spec.serverUrl}
        - name: STACKWEAVER_TOKEN
          valueFrom:
            secretKeyRef:
              name: {spec.tokenSecretRef.name}
              key: {spec.tokenSecretRef.key}
        - name: AGENT_POOL_ID
          value: {spec.agentPoolId}
        - name: RUNNER_TYPE
          value: {spec.runnerType}
        - name: RUNNER_LABELS
          value: {comma-separated spec.labels}
        - name: MAX_CONCURRENT_JOBS
          value: {spec.jobsPerRunner}
        - name: RUNNER_EPHEMERAL
          value: {spec.ephemeral}
        # Additional env from spec.template.spec.env
      resources: {spec.template.spec.resources or defaults}
      volumeMounts: {spec.template.spec.volumeMounts}
      lifecycle:
        preStop:
          exec:
            command: ["/bin/sh", "-c", "touch /tmp/drain && sleep 5"]
      livenessProbe:
        httpGet:
          path: /healthz
          port: 8081
        initialDelaySeconds: 15
        periodSeconds: 20
      readinessProbe:
        httpGet:
          path: /readyz
          port: 8081
        initialDelaySeconds: 5
        periodSeconds: 10
  serviceAccountName: {pool.name}-runner (or spec.template.spec.serviceAccountName if no cloudIdentity)
  terminationGracePeriodSeconds: {spec.terminationGracePeriodSeconds}
  nodeSelector: {spec.template.spec.nodeSelector}
  tolerations: {spec.template.spec.tolerations}
  affinity: {spec.template.spec.affinity}
  volumes: {spec.template.spec.volumes}
  imagePullSecrets: {spec.template.spec.imagePullSecrets}
```

The `preStop` hook writes a drain signal file (`/tmp/drain`) that the runner agent watches, then sleeps 5 seconds to allow endpoint removal. See [04-graceful-shutdown.md](./04-graceful-shutdown.md) for the full shutdown protocol.

### Default Runner Images

When `spec.runnerImage` is not set, the controller uses:

| Runner Type | Default Image |
|-------------|--------------|
| terraform | `ghcr.io/michielvha/stackweaver/runner:{operator-version}` |
| ansible | `ghcr.io/michielvha/stackweaver/ansible-runner:{operator-version}` |
| combined | `ghcr.io/michielvha/stackweaver/runner:{operator-version}` |

The operator version is embedded at build time and used as the default image tag to ensure version alignment.

### Phase Transitions

```
Pending ──────► Provisioning ──────► Ready
                                       │
                                       ▼
                                    Scaling ──► Ready
                                       │
                                       ▼
                                     Error ───► Provisioning (on spec fix)
```

| Phase | Condition |
|-------|-----------|
| Pending | CR just created, reconcile not yet run |
| Provisioning | Owned resources being created (Deployment, SA, ScaledObject) |
| Ready | Deployment available, at least minReplicas pods ready (or minReplicas=0 and ScaledObject active) |
| Scaling | KEDA is actively scaling up or down (currentReplicas != desired) |
| Error | Validation failed, API unreachable, or owned resource in error state |

### Finalizer

The controller adds a finalizer `runner.stackweaver.io/cleanup` to ensure runner deregistration before deletion:

1. On `RunnerPool` deletion, the finalizer fires
2. Controller sets all Runner CRs to phase=Draining
3. Waits for all runners to deregister from the Stackweaver API (polls runner status)
4. Timeout: if runners don't deregister within 5 minutes, proceed anyway
5. Remove finalizer → Kubernetes deletes the CR and garbage-collects owned resources

### Error Handling

| Error | Behavior |
|-------|----------|
| Token Secret not found | Set condition `APIReachable=False`, phase=Error, requeue 30s |
| Stackweaver API unreachable | Set condition `APIReachable=False`, phase=Error, requeue with exponential backoff (30s → 1m → 2m → 5m cap) |
| KEDA not installed | Log warning, activate fallback scaler (see below), set condition `ScalingActive=True` with message "using built-in scaler". Deployment still created. |
| Pod crash loop | Runner controller marks Runner CR phase=Error. RunnerPool status shows degraded condition. KEDA handles replacement via Deployment. |
| Invalid spec (e.g. AWS roleArn format) | Rejected by CRD schema validation (kubebuilder markers). If somehow bypassed, controller sets phase=Error with descriptive message. |

### Fallback Scaler (No KEDA)

When KEDA is not installed (ScaledObject CRD not registered), the RunnerPool controller runs a built-in poll loop instead. This is intentionally minimal — see [02-scaler.md](./02-scaler.md#fallback-scaler-no-keda) for full details.

The fallback scaler runs as a goroutine per RunnerPool within the controller process:

1. Poll `GET /api/v2/agent-pools/{id}/queue-depth` every 15 seconds
2. Calculate desired replicas: `min(maxReplicas, max(minReplicas, ceil(pendingJobs / jobsPerRunner)))`
3. Update Deployment replica count directly via the Kubernetes API
4. Respect cooldown: don't scale down if last scale event was within `scaleDownDelaySeconds`
5. On pool deletion or KEDA becoming available, stop the goroutine

The fallback scaler sets `status.lastScaleTime` and `status.pendingJobs` on each poll cycle.

### Spec Updates

| Field Changed | Behavior |
|---------------|----------|
| runnerImage | Rolling update of Deployment (Kubernetes default strategy) |
| runnerType | Rolling update — existing pods drain, new pods start with new type |
| minReplicas / maxReplicas | ScaledObject updated, KEDA adjusts |
| scaleDownDelaySeconds | ScaledObject cooldownPeriod updated |
| cloudIdentity | ServiceAccount annotations updated; existing pods must restart to pick up new identity (controller triggers rollout) |
| labels | Deployment env updated, rolling update. Existing registered runners keep old labels until replaced. |
| serverUrl / agentPoolId / tokenSecretRef | Deployment env updated, rolling update. All runners re-register with new target. |
| template changes | Deployment pod template updated, rolling update |

---

## Runner Controller

The Runner controller is secondary. It watches pods owned by RunnerPool Deployments and maintains `Runner` CRs that represent individual runner instances.

### Purpose

Runner CRs provide per-pod visibility into runner state — which runners are registered, busy, idle, or draining. This gives operators a Kubernetes-native view of runner status without querying the Stackweaver API.

### Reconcile Logic

The Runner controller reconciles on:
- Pod create/update/delete events for pods with label `app.kubernetes.io/managed-by: stackweaver-operator`
- Runner CR status updates
- Periodic resync (default 1 minute)

#### Pod Created

1. Create a corresponding Runner CR with `spec.poolRef` set to the owning RunnerPool
2. Set `status.podName` to the pod name
3. Set `status.phase = Pending`
4. Set condition `PodReady=False` with reason "PodNotYetRunning"

#### Pod Running

1. Set condition `PodReady=True`
2. Set `status.phase = Registering`
3. Poll the Stackweaver API (`GET /api/v2/agent-pools/{id}/runners`) and find the runner by matching `attributes.name` against the pod name
4. Once registered:
   - Set `status.runnerId` to the API-assigned UUID
   - Set `status.phase = Idle`
   - Set condition `Registered=True`
5. If registration doesn't happen within 2 minutes:
   - Set `status.phase = Error`
   - Set `status.message = "runner did not register with API within 2 minutes"`
   - Set condition `Registered=False` with reason "RegistrationTimeout"

#### Runner Busy

1. Detected via Stackweaver API polling (runner status = "busy") or heartbeat data
2. Set `status.phase = Busy`
3. Update `status.currentJobs`
4. Clear `status.message` if previously set

#### Runner Idle

1. Detected via API polling (runner status = "online", currentJobs = 0)
2. Set `status.phase = Idle`
3. Set `status.currentJobs = 0`
4. Update `status.lastJobFinished`

#### Pod Terminating

1. Set `status.phase = Draining`
2. Set `status.message = "pod is terminating, waiting for job completion and deregistration"`
3. The runner agent handles deregistration via its own SIGTERM handler

#### Pod Deleted / Runner Deregistered

1. Set `status.phase = Terminated`
2. Set `status.message = "runner deregistered and pod deleted"`
3. Runner CR is garbage-collected via ownerReferences shortly after

The `Terminated` phase exists as a brief transitional state between the runner deregistering and the CR being garbage-collected. It allows observability tooling to distinguish clean exits from errors.

#### Pod CrashLoopBackOff

1. Set condition `PodReady=False` with reason "CrashLoopBackOff"
2. Set `status.phase = Error`
3. Set `status.message` to the pod's last termination reason

### Heartbeat Monitoring

The Runner controller periodically checks runner health:

1. For each Runner CR in phase Idle or Busy, check `status.lastHeartbeat`
2. If no heartbeat received for >60s (6 missed heartbeats at 10s interval), set condition `Healthy=False`
3. If no heartbeat for >5 minutes, set `status.phase = Error`
4. The RunnerPool controller uses this to update its `Degraded` condition

Note: Heartbeat data comes from the Stackweaver API (the runner sends heartbeats to the API, not to the operator). The Runner controller polls `GET /api/v2/runners/{id}` to read `lastHeartbeatAt`.

### API Polling Strategy

The Runner controller polls the Stackweaver API to sync runner state. To avoid excessive API calls:

- Poll interval: 15 seconds per runner (staggered across runners in a pool)
- Batch endpoint preferred if available: `GET /api/v2/agent-pools/{id}/runners` returns all runners in one call
- Cache API responses for 10 seconds
- On API errors, use exponential backoff (15s → 30s → 60s → 2m cap)
- Circuit breaker: after 5 consecutive failures, stop polling for 5 minutes, set condition `APIReachable=False` on RunnerPool
