# Implementation Plans

Concrete implementation plans derived from the [specifications](../spec/). Each phase builds on the previous one.

| Plan | Summary | Key Deliverables |
|------|---------|-----------------|
| [Phase 1: Foundation](./phase-1-foundation.md) | API client, pod template builder, RunnerPool + Runner controllers, Helm chart | Working operator with static replicas |
| [Phase 2: KEDA Auto-Scaling](./phase-2-keda-autoscaling.md) | Queue depth endpoint, gRPC scaler, ScaledObject generation, fallback scaler | Auto-scaling based on job demand |
| [Phase 3: Ephemeral & Lifecycle](./phase-3-ephemeral-lifecycle.md) | Ephemeral mode, drain file detection, finalizer drain wait | One-job-per-pod mode, safe shutdown |
| [Phase 4: Observability](./phase-4-observability.md) | Prometheus metrics, K8s events, HA leader election, scale rate limiting | Production-ready operator |
| [Phase 5: Advanced](./phase-5-advanced.md) | Multiple runner pools, webhook scaling, multi-cloud identity | Optional enhancements |
| [E2E & CI](./e2e-and-ci.md) | Stub runner, mock API, kind cluster E2E, GitHub Actions CI/CD | Test infrastructure + release pipeline |

## Dependency Graph

```
Phase 1 (Foundation)
    │
    ├──► Phase 2 (KEDA) ──► Phase 3 (Ephemeral) ──► Phase 4 (Observability) ──► Phase 5 (Advanced)
    │
    └──► E2E & CI (can start in parallel with Phase 1)
```

## Platform Changes

Some phases require changes in the main Stackweaver repo (not the operator):

| Phase | Platform Change |
|-------|----------------|
| Phase 2 | New endpoint: `GET /api/v2/agent-pools/:id/queue-depth` |
| Phase 3 | Runner agent: ephemeral exit mode, drain file detection |
