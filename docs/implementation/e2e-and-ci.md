# E2E Testing & CI — Implementation Plan

## Goal

Set up the E2E test infrastructure (kind cluster, mock API, stub runner) and CI pipeline. This runs in parallel with Phase 1-2 implementation.

## Stub Runner Image

**Create**: `test/stub-runner/`

A minimal Go binary that simulates the runner agent without Terraform/Ansible:

| File | Purpose |
|------|---------|
| `test/stub-runner/main.go` | Simulated runner agent |
| `test/stub-runner/Dockerfile` | Minimal container image |

**Behavior:**
1. Read env vars: `STACKWEAVER_SERVER`, `STACKWEAVER_TOKEN`, `RUNNER_AGENT_POOL_ID`, `RUNNER_NAME`, `RUNNER_EPHEMERAL`
2. `POST /api/v2/runner/register` on startup
3. Poll `POST /api/v2/runner/heartbeat` every 10s
4. When heartbeat returns a job: sleep for `JOB_DURATION` seconds (default: 5), then `POST /api/v2/runner/jobs/{id}/complete`
5. Handle SIGTERM: finish current sleep, deregister, exit 0
6. If `RUNNER_EPHEMERAL=true`: exit after one job
7. Watch for `/tmp/drain` file (preStop hook signal)

## Mock Stackweaver API

**Create**: `test/mock-api/`

A Go HTTP server that implements the endpoints the operator and stub runner need:

| File | Purpose |
|------|---------|
| `test/mock-api/main.go` | Server entry point |
| `test/mock-api/handlers.go` | Endpoint handlers |
| `test/mock-api/state.go` | In-memory state (runners, jobs, queue depth) |
| `test/mock-api/Dockerfile` | Container image |

**Endpoints:**

Operator endpoints (read-only):
- `GET /api/v2/agent-pools/{id}/queue-depth` — returns configurable pending counts
- `GET /api/v2/agent-pools/{id}/runners` — returns registered runners
- `GET /api/v2/runners/{id}` — returns individual runner (status=offline after deregistration)

Runner agent endpoints:
- `POST /api/v2/runner/register` — accepts registration, assigns UUID
- `POST /api/v2/runner/heartbeat` — returns pending jobs from test state
- `POST /api/v2/runner/jobs/{id}/start` — marks job as running
- `POST /api/v2/runner/jobs/{id}/complete` — marks job as done
- `POST /api/v2/runner/deregister` — marks runner offline

Test control API (for test scripts):
- `POST /test/set-queue-depth` — set pending job counts
- `POST /test/create-job` — add a job to the queue
- `GET /test/registered-runners` — list all runners that registered
- `GET /test/completed-jobs` — list all completed jobs
- `POST /test/reset` — clear all state

## E2E Test Framework

**Create**: `test/e2e/`

| File | Purpose |
|------|---------|
| `test/e2e/e2e_suite_test.go` | Suite setup (kind cluster, deploy operator + mock API) |
| `test/e2e/helpers_test.go` | Wait helpers, kubectl wrappers, assertion utilities |
| `test/e2e/lifecycle_test.go` | Tests 1, 5: full lifecycle, deletion cleanup |
| `test/e2e/scaling_test.go` | Tests 2, 3, 10: scale-to-zero, scale-from-zero, max cap |
| `test/e2e/ephemeral_test.go` | Test 4: ephemeral mode |
| `test/e2e/rolling_update_test.go` | Test 6: image change rollout |
| `test/e2e/cloud_identity_test.go` | Test 7: cloud identity annotations |
| `test/e2e/resilience_test.go` | Tests 8, 9: token rotation, API downtime |

**Makefile targets:**

```makefile
.PHONY: test-e2e
test-e2e: ## Run E2E tests
    test/e2e/setup.sh       # Create kind cluster, load images, deploy
    go test ./test/e2e/ -v -timeout 30m
    test/e2e/teardown.sh    # Delete kind cluster

.PHONY: test-e2e-keep
test-e2e-keep: ## Run E2E tests, keep cluster for debugging
    test/e2e/setup.sh
    go test ./test/e2e/ -v -timeout 30m
```

**Setup script** (`test/e2e/setup.sh`):
1. Create kind cluster
2. Build operator image → load into kind
3. Build stub-runner image → load into kind
4. Build mock-api image → load into kind
5. Deploy CRDs (`make install`)
6. Deploy mock API as a Deployment + Service
7. Deploy operator (using kustomize or Helm)
8. Wait for operator pod to be ready

## CI Pipeline

**Create**: `.github/workflows/ci.yml`

```yaml
name: CI
on:
  push:
    branches: [main]
  pull_request:
    branches: [main]

jobs:
  lint:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with: { go-version-file: go.mod }
      - uses: golangci/golangci-lint-action@v6

  unit-test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with: { go-version-file: go.mod }
      - run: make test-unit   # go test ./... -short

  integration-test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with: { go-version-file: go.mod }
      - run: make test         # envtest-based

  build:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with: { go-version-file: go.mod }
      - run: make build
      - run: make docker-build IMG=stackweaver-operator:ci

  e2e-test:
    runs-on: ubuntu-latest
    needs: [build]
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with: { go-version-file: go.mod }
      - uses: engineerd/setup-kind@v0.6.2
      - run: make test-e2e
```

**Create**: `.github/workflows/release.yml`

```yaml
name: Release
on:
  push:
    tags: ["v*"]

jobs:
  release:
    runs-on: ubuntu-latest
    permissions:
      contents: write
      packages: write
      id-token: write   # cosign
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with: { go-version-file: go.mod }
      - uses: docker/setup-buildx-action@v3
      - uses: docker/login-action@v3
        with:
          registry: ghcr.io
          username: ${{ github.actor }}
          password: ${{ secrets.GITHUB_TOKEN }}
      - run: make docker-buildx IMG=ghcr.io/vhco-pro/stackweaver-operator:${GITHUB_REF_NAME}
      - uses: sigstore/cosign-installer@v3
      - run: cosign sign ghcr.io/vhco-pro/stackweaver-operator:${GITHUB_REF_NAME}
      - run: helm package deploy/helm/stackweaver-operator
      - run: helm push stackweaver-operator-*.tgz oci://ghcr.io/vhco-pro/charts
      - uses: softprops/action-gh-release@v2
        with:
          generate_release_notes: true
```

## Files Summary

| Action | Path |
|--------|------|
| Create | `test/stub-runner/main.go` |
| Create | `test/stub-runner/Dockerfile` |
| Create | `test/mock-api/main.go` |
| Create | `test/mock-api/handlers.go` |
| Create | `test/mock-api/state.go` |
| Create | `test/mock-api/Dockerfile` |
| Create | `test/e2e/e2e_suite_test.go` |
| Create | `test/e2e/helpers_test.go` |
| Create | `test/e2e/lifecycle_test.go` |
| Create | `test/e2e/scaling_test.go` |
| Create | `test/e2e/ephemeral_test.go` |
| Create | `test/e2e/rolling_update_test.go` |
| Create | `test/e2e/cloud_identity_test.go` |
| Create | `test/e2e/resilience_test.go` |
| Create | `test/e2e/setup.sh` |
| Create | `test/e2e/teardown.sh` |
| Create | `.github/workflows/ci.yml` |
| Create | `.github/workflows/release.yml` |
| Modify | `Makefile` (add test-unit, test-e2e targets) |
