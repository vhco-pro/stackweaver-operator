# Testing Specification

This document specifies the testing strategy for the Stackweaver Runner Operator, covering unit tests, integration tests, and end-to-end tests.

## Testing Layers

```
┌─────────────────────────────────────────────────────┐
│  E2E Tests (real cluster + Stackweaver API mock)    │  Slow, high confidence
├─────────────────────────────────────────────────────┤
│  Integration Tests (envtest + API mock)             │  Medium speed, good coverage
├─────────────────────────────────────────────────────┤
│  Unit Tests (pure Go, no cluster)                   │  Fast, focused
└─────────────────────────────────────────────────────┘
```

## Unit Tests

Unit tests cover pure logic with no Kubernetes or API dependencies. They run in milliseconds and should be the bulk of the test suite.

### What to Unit Test

| Component | Tests |
|-----------|-------|
| Pod template builder | Given a RunnerPoolSpec, assert the generated PodTemplateSpec has correct env vars, volumes, annotations, labels, service account, cloud identity annotations |
| ScaledObject builder | Given a RunnerPoolSpec, assert the generated ScaledObject has correct triggers, metadata, min/max, cooldown |
| Phase transition logic | Given current phase + observed state, assert correct next phase |
| API client response parsing | Given JSON:API responses (success + error), assert correct parsing into Go types |
| Retry/backoff logic | Assert backoff intervals, jitter, max retries |
| Circuit breaker | Assert state transitions (closed → open → half-open → closed) based on success/failure sequences |
| Queue depth metric extraction | Given queue-depth response, assert correct pending count extraction (total, by type) |
| Scaler cache | Assert cache hit/miss/expiry behavior |
| Validation helpers | Assert spec validation catches invalid roleArn, missing fields, etc. |

### Conventions

- Use standard Go `testing` package (not Ginkgo for unit tests — keep it simple)
- Table-driven tests for input/output mapping functions
- Test files co-located with source: `foo.go` → `foo_test.go`
- No mocks of Kubernetes client in unit tests — if you need a client, it's an integration test

### Example: Pod Template Builder

```go
func TestBuildPodTemplate_AWSCloudIdentity(t *testing.T) {
    pool := &v1alpha1.RunnerPool{
        Spec: v1alpha1.RunnerPoolSpec{
            ServerURL:   "https://stackweaver.example.com",
            AgentPoolID: "apool-123",
            RunnerType:  v1alpha1.RunnerTypeTerraform,
            CloudIdentity: &v1alpha1.CloudIdentityConfig{
                Provider: v1alpha1.CloudProviderAWS,
                AWS: &v1alpha1.AWSIdentityConfig{
                    RoleARN: "arn:aws:iam::123456789012:role/runner",
                },
            },
        },
    }

    template := buildPodTemplate(pool)

    // Assert ServiceAccount annotation
    sa := buildServiceAccount(pool)
    assert.Equal(t, "arn:aws:iam::123456789012:role/runner",
        sa.Annotations["eks.amazonaws.com/role-arn"])

    // Assert env vars
    envMap := envToMap(template.Spec.Containers[0].Env)
    assert.Equal(t, "agent", envMap["RUNNER_MODE"])
    assert.Equal(t, "terraform", envMap["RUNNER_TYPE"])
}
```

## Integration Tests

Integration tests use [envtest](https://pkg.go.dev/sigs.k8s.io/controller-runtime/pkg/envtest) — a real etcd + API server running locally, but no kubelet or node. These test the full controller reconcile loop against a real Kubernetes API.

### Setup

Kubebuilder scaffolds the `suite_test.go` with envtest setup. The integration test suite:

1. Starts envtest (etcd + API server)
2. Installs CRDs (RunnerPool, Runner, and KEDA ScaledObject if testing KEDA integration)
3. Starts the controller manager
4. Runs tests that create/update/delete CRs and assert resulting state

### Stackweaver API Mock

Since integration tests can't reach a real Stackweaver instance, the API client is backed by a mock HTTP server:

```go
type MockStackweaverAPI struct {
    // QueueDepth is returned by GET /api/v2/agent-pools/{id}/queue-depth
    QueueDepth QueueDepthResponse

    // Runners is returned by GET /api/v2/agent-pools/{id}/runners
    Runners []RunnerResponse

    // RegisterCalls tracks calls to the register endpoint
    RegisterCalls []RegisterRequest

    server *httptest.Server
}
```

The mock is configurable per test — set `QueueDepth.TotalPending = 5` to simulate pending jobs.

### Test Scenarios

#### RunnerPool Controller

| # | Scenario | Setup | Assert |
|---|----------|-------|--------|
| 1 | Create RunnerPool | Create CR with valid spec | Deployment created, ServiceAccount created, phase=Provisioning then Ready |
| 2 | Create with cloud identity (AWS) | Set cloudIdentity.provider=aws | ServiceAccount has IRSA annotation |
| 3 | Create with cloud identity (GCP) | Set cloudIdentity.provider=gcp | ServiceAccount has GCP WI annotation |
| 4 | Create with cloud identity (Azure) | Set cloudIdentity.provider=azure | SA has Azure annotation, pods have WI label |
| 5 | Update runnerImage | Change spec.runnerImage | Deployment pod template updated |
| 6 | Update scaling params | Change min/max replicas | ScaledObject updated (or Deployment replicas if no KEDA) |
| 7 | Delete RunnerPool | Delete CR | Owned resources garbage-collected |
| 8 | Delete with running jobs | Delete CR while Runner CRs are in Busy phase | Finalizer blocks until drain completes (or timeout) |
| 9 | Invalid token secret | tokenSecretRef points to non-existent Secret | Phase=Error, condition APIReachable=False |
| 10 | KEDA not installed | ScaledObject CRD not registered | Fallback scaler activates, Deployment gets static replica count |
| 11 | Spec validation | Create CR with invalid roleArn, empty agentPoolId | CR rejected by CRD schema validation (kubebuilder markers) |

#### Runner Controller

| # | Scenario | Setup | Assert |
|---|----------|-------|--------|
| 12 | Pod created | Create a pod with operator labels | Runner CR created, phase=Pending |
| 13 | Pod running + registered | Mock API returns runner as online | Runner CR phase=Idle, runnerId set, condition Registered=True |
| 14 | Runner picks up job | Mock API returns runner status=busy | Runner CR phase=Busy, currentJobs updated |
| 15 | Runner finishes job | Mock API returns runner status=online | Runner CR phase=Idle, lastJobFinished set |
| 16 | Pod terminating | Delete pod | Runner CR phase=Draining |
| 17 | Registration timeout | Mock API never returns the runner | Runner CR phase=Error after 2 min |
| 18 | Heartbeat timeout | Mock API returns stale lastHeartbeatAt | Condition Healthy=False |

#### KEDA Scaler

| # | Scenario | Setup | Assert |
|---|----------|-------|--------|
| 19 | IsActive with pending jobs | Mock API returns totalPending=3 | IsActive returns true |
| 20 | IsActive with no pending jobs | Mock API returns totalPending=0 | IsActive returns false |
| 21 | GetMetrics returns pending count | Mock API returns totalPending=5 | Metric value = 5 |
| 22 | GetMetrics with type filter | Metadata has runnerType=terraform | Returns pendingTerraformJobs only |
| 23 | API failure with cache | API returns 500, cache has recent value | Returns cached value |
| 24 | API failure without cache | API returns 500, no cache | Returns 0 |
| 25 | API 401 unauthorized | API returns 401 | Error propagated, no retry |

#### Fallback Scaler (No KEDA)

| # | Scenario | Setup | Assert |
|---|----------|-------|--------|
| 26 | Fallback scale-up | KEDA not installed, inject 3 pending jobs | Deployment scaled to 3 replicas |
| 27 | Fallback scale-down with cooldown | Pending drops to 0, cooldown=30s | Replicas stay until cooldown expires, then scale down |
| 28 | Fallback respects maxReplicas | Inject 20 jobs, maxReplicas=5 | Deployment scaled to 5 (not 20) |
| 29 | KEDA installed after fallback | Install KEDA CRDs while fallback is active | Controller creates ScaledObject, stops fallback loop |

#### Graceful Shutdown

| # | Scenario | Setup | Assert |
|---|----------|-------|--------|
| 30 | Scale down idle runner | Reduce replicas, runner is idle | Runner deregisters, pod exits 0 |
| 31 | Scale down busy runner | Reduce replicas, runner has active job | Pod stays alive until job completes (within grace period) |
| 32 | RunnerPool deletion during jobs | Delete RunnerPool, runners busy | Finalizer holds, runners drain, then cleanup |

#### Edge Cases

| # | Scenario | Setup | Assert |
|---|----------|-------|--------|
| 33 | Ephemeral + jobsPerRunner>1 | Create pool with ephemeral=true, jobsPerRunner=3 | Controller forces jobsPerRunner=1, logs warning |
| 34 | jobsPerRunner scaling math | jobsPerRunner=3, inject 9 jobs | 3 runners created (not 9) |
| 35 | Secret rotation mid-operation | Update token Secret while runners are active | Operator picks up new key, runners continue |

### Running Integration Tests

```bash
# Requires envtest binaries (installed via make setup)
make test

# With verbose output
make test ARGS="-v -count=1"

# Specific test
go test ./internal/controller/ -run TestRunnerPoolReconcile_Create -v
```

## End-to-End Tests

E2E tests run against a real Kubernetes cluster (kind or k3d) with the operator deployed as a container image, a mock Stackweaver API running as a pod, and optionally KEDA installed.

### Infrastructure

```
┌─────────────────────────────────────────────────────────┐
│  kind cluster                                           │
│                                                         │
│  ┌──────────────┐  ┌──────────────┐  ┌───────────────┐  │
│  │  Operator    │  │  Mock API    │  │  KEDA         │  │
│  │  (under test)│  │  (httptest)  │  │  (optional)   │  │
│  └──────────────┘  └──────────────┘  └───────────────┘  │
│                                                         │
│  ┌──────────────────────────────────────────────────┐   │
│  │  Runner pods (created by operator)               │   │
│  │  Using a stub runner image that simulates        │   │
│  │  registration, heartbeats, and job execution     │   │
│  └──────────────────────────────────────────────────┘   │
│                                                         │
└─────────────────────────────────────────────────────────┘
```

### Stub Runner Image

A minimal Docker image that simulates the runner agent without actually executing IaC:

1. Calls `POST /api/v2/runner/register` on startup
2. Polls `POST /api/v2/runner/heartbeat` every 10s
3. When heartbeat returns a job assignment, sleeps for a configurable duration (simulating work)
4. Marks job complete
5. Handles SIGTERM by finishing current "job" and deregistering

This avoids needing Terraform/Ansible binaries in the test environment.

### Mock Stackweaver API

A Go HTTP server deployed as a pod in the kind cluster:

- Implements all endpoints the operator consumes (queue-depth, runners list, runner detail)
- Implements runner agent endpoints (register, heartbeat) for the stub runner
- State is controlled via a test admin API:
  - `POST /test/set-queue-depth` — set pending job count
  - `POST /test/assign-job` — make the next heartbeat return a job
  - `GET /test/registered-runners` — list runners that have registered
  - `POST /test/reset` — clear all state

### E2E Test Scenarios

| # | Scenario | Steps | Assert |
|---|----------|-------|--------|
| 1 | Full lifecycle | Create RunnerPool → wait for Ready → inject pending jobs → verify scale-up → drain jobs → verify scale-down | Runner count matches demand at each stage |
| 2 | Scale to zero | Create pool with minReplicas=0, no pending jobs | 0 runner pods after cooldown |
| 3 | Scale from zero | Pool at 0 replicas → inject 3 pending jobs | 3 runner pods created, register with API |
| 4 | Ephemeral mode | Create pool with ephemeral=true → inject 1 job | Runner starts, completes job, pod exits, not restarted (unless new jobs) |
| 5 | RunnerPool deletion | Create pool → wait for runners → delete pool | All runners deregister, all pods deleted, no orphaned resources |
| 6 | Rolling update | Create pool → change runnerImage → observe rollout | Old pods drain, new pods start with new image |
| 7 | Cloud identity annotations | Create pool with AWS cloudIdentity | SA has annotation, pod has expected env vars |
| 8 | Token rotation | Create pool → update Secret → observe reconnection | Operator picks up new token, runners continue working |
| 9 | API downtime | Create pool → take mock API offline → bring back | Operator recovers, condition APIReachable transitions False → True |
| 10 | Max replicas cap | Inject 20 jobs, maxReplicas=5 | Only 5 runner pods created |

### Running E2E Tests

```bash
# Create kind cluster + deploy operator + run tests
make test-e2e

# With existing cluster (for development)
make test-e2e SKIP_CLUSTER_CREATE=true

# Specific test
make test-e2e ARGS="-run TestE2E_ScaleFromZero"
```

### E2E Test Framework

Use Go's `testing` package with helper functions, not a BDD framework. Each test:

1. Creates a unique namespace
2. Deploys test fixtures (RunnerPool CR, Secrets)
3. Waits for expected state with `Eventually` (from gomega or custom polling)
4. Asserts final state
5. Cleans up namespace

Timeout: each E2E test has a 5-minute timeout. Tests that need longer (scale-down after cooldown) use shorter cooldown values in the test CRs.

## CI Pipeline Integration

See [07-ci-release.md](./07-ci-release.md) for how tests integrate into the CI pipeline.

| Test Layer | CI Trigger | Duration | Required to Merge |
|------------|-----------|----------|-------------------|
| Unit | Every push | ~10s | Yes |
| Integration (envtest) | Every push | ~60s | Yes |
| E2E (kind) | PR to main, release branches | ~5-10 min | Yes |

## Coverage Goals

| Component | Target Coverage |
|-----------|----------------|
| Pod template builder | 90%+ (critical, many code paths) |
| ScaledObject builder | 90%+ |
| API client | 80%+ (response parsing, error handling) |
| Controller reconcile | 70%+ (hard to unit test, covered by integration) |
| Scaler gRPC methods | 80%+ |
| Overall | 75%+ |

Coverage is tracked in CI but not enforced as a gate — some code paths (e.g. SIGTERM handlers) are inherently hard to cover in automated tests.
