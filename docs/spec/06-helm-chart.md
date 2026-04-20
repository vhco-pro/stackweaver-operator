# Helm Chart Specification

This document specifies the Helm chart for deploying the Stackweaver Runner Operator into a Kubernetes cluster.

## Chart Metadata

```yaml
apiVersion: v2
name: stackweaver-operator
description: Kubernetes operator for auto-scaling Stackweaver runner pods
type: application
version: 0.1.0        # Chart version, follows SemVer
appVersion: "0.1.0"   # Operator version
home: https://github.com/vhco-pro/stackweaver-operator
sources:
  - https://github.com/vhco-pro/stackweaver-operator
maintainers:
  - name: Michiel VH
keywords:
  - stackweaver
  - operator
  - runner
  - terraform
  - ansible
  - iac
  - auto-scaling
```

## What the Chart Deploys

| Resource | Purpose |
|----------|---------|
| Deployment | Operator controller manager (and scaler sidecar) |
| ServiceAccount | Operator pod identity |
| ClusterRole | RBAC for managing CRDs, Deployments, ScaledObjects |
| ClusterRoleBinding | Binds ClusterRole to ServiceAccount |
| CRDs | RunnerPool and Runner custom resource definitions |
| Service | gRPC endpoint for KEDA external scaler |
| ConfigMap (optional) | Operator configuration overrides |
| ServiceMonitor (optional) | Prometheus metrics scraping |
| NetworkPolicy (optional) | Restrict operator network access |

## Values Schema

### Top-Level Values

```yaml
# -- Number of operator replicas. Use 2+ for HA with leader election.
replicaCount: 1

# -- Operator container image
image:
  repository: ghcr.io/vhco-pro/stackweaver-operator
  tag: ""        # Defaults to appVersion
  pullPolicy: IfNotPresent

# -- Image pull secrets for private registries
imagePullSecrets: []

# -- Override chart name
nameOverride: ""
fullnameOverride: ""

# -- Operator pod service account
serviceAccount:
  create: true
  annotations: {}
  name: ""       # Defaults to chart fullname

# -- Operator pod resource limits
resources:
  limits:
    cpu: 500m
    memory: 256Mi
  requests:
    cpu: 100m
    memory: 128Mi

# -- Operator pod node selector
nodeSelector: {}

# -- Operator pod tolerations
tolerations: []

# -- Operator pod affinity rules
affinity: {}

# -- Pod security context
podSecurityContext:
  runAsNonRoot: true
  seccompProfile:
    type: RuntimeDefault

# -- Container security context
securityContext:
  allowPrivilegeEscalation: false
  readOnlyRootFilesystem: true
  capabilities:
    drop:
      - ALL
```

### Operator Configuration

```yaml
operator:
  # -- Log level: debug, info, warn, error
  logLevel: info

  # -- Log format: json, text
  logFormat: json

  # -- Enable leader election for HA deployments
  leaderElection:
    enabled: true

  # -- Namespace restriction. Empty = watch all namespaces.
  watchNamespaces: []

  # -- Resync period for controller cache (Go duration)
  resyncPeriod: 5m

  # -- Concurrent reconciles per controller
  maxConcurrentReconciles: 5
```

### Scaler Configuration

```yaml
scaler:
  # -- Enable the built-in KEDA external scaler
  enabled: true

  # -- gRPC listen port
  port: 9090

  # -- Deploy as sidecar (same pod as controller) or separate Deployment
  mode: sidecar   # sidecar | deployment

  # -- TLS configuration for gRPC
  tls:
    enabled: false
    certSecretName: ""   # Secret containing tls.crt and tls.key

  # -- Cache TTL for queue depth responses
  cacheTTL: 5s

  # -- Service type for the scaler gRPC endpoint
  service:
    type: ClusterIP
    port: 9090
```

### CRD Management

```yaml
crds:
  # -- Install CRDs with the chart. Set to false if CRDs are managed externally.
  install: true

  # -- Keep CRDs on chart uninstall. CRDs are cluster-scoped and shared.
  keep: true

  # -- Annotations to add to CRDs
  annotations: {}
```

### Metrics / Monitoring

```yaml
metrics:
  # -- Expose Prometheus metrics
  enabled: true

  # -- Metrics port
  port: 8080

  # -- Create ServiceMonitor for Prometheus Operator
  serviceMonitor:
    enabled: false
    namespace: ""
    interval: 30s
    scrapeTimeout: 10s
    labels: {}

  # -- Create PrometheusRule for alerts
  prometheusRule:
    enabled: false
    namespace: ""
    labels: {}
    rules: []
```

### KEDA

```yaml
keda:
  # -- Whether KEDA is expected to be installed.
  # If true and KEDA is not found, the operator logs an error.
  # If false, the operator uses the built-in fallback scaler.
  required: false
```

### Runner Defaults

```yaml
# -- Default values for RunnerPool CRs. These are applied when the CR
# doesn't specify a value. They do NOT override CR-level settings.
runnerDefaults:
  # -- Default runner image (per type) when RunnerPool.spec.runnerImage is empty
  images:
    terraform: ""   # Defaults to ghcr.io/michielvha/stackweaver/runner:{appVersion}
    ansible: ""     # Defaults to ghcr.io/michielvha/stackweaver/ansible-runner:{appVersion}
    combined: ""

  # -- Default resource requests/limits for runner pods
  resources:
    requests:
      cpu: 500m
      memory: 512Mi
    limits:
      cpu: "2"
      memory: 2Gi

  # -- Default terminationGracePeriodSeconds
  terminationGracePeriodSeconds: 3600
```

## CRD Installation Strategy

CRDs are installed as part of the Helm chart using the `crds/` directory (Helm's built-in CRD support):

```
deploy/helm/stackweaver-operator/
├── Chart.yaml
├── values.yaml
├── crds/
│   ├── runner.stackweaver.io_runnerpools.yaml
│   └── runner.stackweaver.io_runners.yaml
├── templates/
│   ├── _helpers.tpl
│   ├── deployment.yaml
│   ├── serviceaccount.yaml
│   ├── clusterrole.yaml
│   ├── clusterrolebinding.yaml
│   ├── service.yaml
│   ├── configmap.yaml
│   ├── servicemonitor.yaml
│   └── networkpolicy.yaml
└── README.md
```

Using `crds/` directory means:
- CRDs are installed before any templates
- CRDs are NOT deleted on `helm uninstall` (prevents accidental data loss)
- CRDs are NOT upgraded on `helm upgrade` (Helm limitation) — users must apply CRDs manually on upgrade, or use a CRD management hook

For upgrades, the chart includes a pre-upgrade hook job that applies the latest CRDs:

```yaml
# templates/crds-upgrade-job.yaml
apiVersion: batch/v1
kind: Job
metadata:
  name: {{ include "stackweaver-operator.fullname" . }}-crd-upgrade
  annotations:
    helm.sh/hook: pre-upgrade
    helm.sh/hook-weight: "-1"
    helm.sh/hook-delete-policy: hook-succeeded,before-hook-creation
```

## RBAC

The operator needs cluster-wide permissions to manage CRDs and watch resources across namespaces.

### ClusterRole

```yaml
rules:
  # Manage RunnerPool and Runner CRs
  - apiGroups: ["runner.stackweaver.io"]
    resources: ["runnerpools", "runnerpools/status", "runnerpools/finalizers"]
    verbs: ["get", "list", "watch", "create", "update", "patch", "delete"]
  - apiGroups: ["runner.stackweaver.io"]
    resources: ["runners", "runners/status"]
    verbs: ["get", "list", "watch", "create", "update", "patch", "delete"]

  # Manage owned Deployments
  - apiGroups: ["apps"]
    resources: ["deployments"]
    verbs: ["get", "list", "watch", "create", "update", "patch", "delete"]

  # Manage ServiceAccounts for cloud identity
  - apiGroups: [""]
    resources: ["serviceaccounts"]
    verbs: ["get", "list", "watch", "create", "update", "patch", "delete"]

  # Read Secrets (for API tokens)
  - apiGroups: [""]
    resources: ["secrets"]
    verbs: ["get", "list", "watch"]

  # Watch pods (for Runner status)
  - apiGroups: [""]
    resources: ["pods"]
    verbs: ["get", "list", "watch"]

  # Manage PodDisruptionBudgets
  - apiGroups: ["policy"]
    resources: ["poddisruptionbudgets"]
    verbs: ["get", "list", "watch", "create", "update", "patch", "delete"]

  # Manage KEDA ScaledObjects
  - apiGroups: ["keda.sh"]
    resources: ["scaledobjects"]
    verbs: ["get", "list", "watch", "create", "update", "patch", "delete"]

  # Events
  - apiGroups: [""]
    resources: ["events"]
    verbs: ["create", "patch"]

  # Leader election
  - apiGroups: ["coordination.k8s.io"]
    resources: ["leases"]
    verbs: ["get", "list", "watch", "create", "update", "patch", "delete"]
```

## Installation

```bash
# Add the Helm repository
helm repo add stackweaver https://charts.stackweaver.io
helm repo update

# Install
helm install stackweaver-operator stackweaver/stackweaver-operator \
  --namespace stackweaver-system \
  --create-namespace

# Install with custom values
helm install stackweaver-operator stackweaver/stackweaver-operator \
  --namespace stackweaver-system \
  --create-namespace \
  -f custom-values.yaml
```

## Post-Install: Creating a RunnerPool

After the operator is installed, users create RunnerPool resources:

```bash
# Create the API token secret
kubectl create secret generic stackweaver-runner-token \
  --namespace stackweaver \
  --from-literal=token=<your-api-key>

# Apply a RunnerPool
kubectl apply -f - <<EOF
apiVersion: runner.stackweaver.io/v1alpha1
kind: RunnerPool
metadata:
  name: terraform-runners
  namespace: stackweaver
spec:
  serverUrl: https://stackweaver.example.com
  tokenSecretRef:
    name: stackweaver-runner-token
  agentPoolId: "550e8400-e29b-41d4-a716-446655440000"
  runnerType: terraform
  minReplicas: 0
  maxReplicas: 10
EOF
```
