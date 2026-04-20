# Stackweaver Runner Operator

A Kubernetes operator that automatically manages [Stackweaver](https://github.com/michielvha/stackweaver) runner pods — scaling them up when jobs are pending and down when idle. Analogous to the [GitHub Actions Runner Controller (ARC)](https://github.com/actions/actions-runner-controller) but for Stackweaver's self-hosted runner infrastructure.

## Features

- **Auto-scaling**: Scale runner pods up/down based on pending job demand via [KEDA](https://keda.sh/) external scaler
- **Scale to zero**: No runners running when there are no jobs — save cluster resources
- **Cloud identity**: First-class support for AWS IRSA, GCP Workload Identity, and Azure Workload Identity
- **Ephemeral runners**: Optional one-job-per-pod mode for stronger isolation
- **Graceful shutdown**: Runners are never killed mid-job — in-progress work always completes
- **Fallback scaler**: Built-in basic scaler for clusters without KEDA

## How It Works

1. You create a `RunnerPool` custom resource targeting a Stackweaver agent pool
2. The operator creates a Deployment with runner pods that register with the Stackweaver API
3. KEDA (or the built-in fallback scaler) monitors pending job count and scales the Deployment
4. Runners pick up jobs, execute Terraform/Ansible, and report results back to Stackweaver
5. When demand drops, runners drain gracefully and scale down

## Custom Resources

### RunnerPool

User-facing resource defining a pool of auto-scaling runners:

```yaml
apiVersion: runner.stackweaver.io/v1alpha1
kind: RunnerPool
metadata:
  name: terraform-runners
  namespace: stackweaver
spec:
  serverUrl: https://stackweaver.example.com
  tokenSecretRef:
    name: stackweaver-runner-token
    key: token
  agentPoolId: "550e8400-e29b-41d4-a716-446655440000"
  runnerType: terraform
  minReplicas: 0
  maxReplicas: 10
  scaleDownDelaySeconds: 300
  cloudIdentity:
    provider: aws
    aws:
      roleArn: arn:aws:iam::123456789012:role/stackweaver-runner
```

### Runner

Controller-managed resource tracking individual runner pod state. Created automatically by the operator — do not create directly.

## Prerequisites

- Kubernetes 1.34+
- [KEDA](https://keda.sh/) 2.12+ (recommended, fallback scaler available without it)
- A running Stackweaver instance with an agent pool and API key

## Quick Start

```bash
# Install the operator via Helm
helm install stackweaver-operator oci://ghcr.io/vhco-pro/charts/stackweaver-operator \
  --namespace stackweaver-system \
  --create-namespace

# Create an API token secret
kubectl create secret generic stackweaver-runner-token \
  --namespace stackweaver \
  --from-literal=token=<your-api-key>

# Deploy a runner pool
kubectl apply -f config/samples/runner_v1alpha1_runnerpool.yaml

# Check status
kubectl get runnerpools
kubectl get runners
```

## Development

### Prerequisites

- Go 1.26+
- Docker
- kubectl
- A Kubernetes cluster (kind for local development)

### Build & Test

```bash
make build          # Build the operator binary
make test           # Run unit + integration tests (envtest)
make test-e2e       # Run E2E tests against a kind cluster
make lint           # Run golangci-lint
make manifests      # Regenerate CRD manifests and RBAC
make generate       # Regenerate deepcopy functions
```

### Run Locally

```bash
# Install CRDs into your cluster
make install

# Run the operator locally (outside the cluster)
make run

# Apply a sample RunnerPool
kubectl apply -f config/samples/runner_v1alpha1_runnerpool.yaml
```

### Deploy to Cluster

```bash
make docker-build IMG=ghcr.io/vhco-pro/stackweaver-operator:dev
make deploy IMG=ghcr.io/vhco-pro/stackweaver-operator:dev
```

## Documentation

- [Design Plan](docs/PLAN.md) — high-level architecture and design decisions
- [Specifications](docs/spec/) — detailed behavior specs for all components
- [Implementation Plans](docs/implementation/) — phased implementation roadmap

## License

Copyright 2026. Licensed under the [BSL 1.1](LICENSE).
