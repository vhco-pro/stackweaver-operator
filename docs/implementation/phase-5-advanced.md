# Phase 5: Advanced Features — Implementation Plan

## Goal

Multiple runner pools per cluster, webhook-driven scaling, and multi-cloud identity. These are optional enhancements beyond the core operator functionality.

## Prerequisites

- Phase 4 complete (production-ready operator with observability)

## Features

### Feature 1: Multiple Runner Pools Per Cluster

Multiple `RunnerPool` resources with different configurations in the same cluster, each targeting a different Stackweaver agent pool.

**This already works by design** — each RunnerPool is independent (own Deployment, SA, ScaledObject). No code changes needed for basic multi-pool setups. Stackweaver's agent pool model already supports scoping pools to specific workspaces/projects, so different RunnerPools naturally target different workloads.

**What might be needed:**
- Dashboard/CLI support for viewing all pools across namespaces
- Validation: warn if two RunnerPools target the same `agentPoolId` (could cause double-scaling)
- Documentation for common patterns (GPU runners, ARM runners, high-memory runners)

**Add**: `internal/controller/runnerpool_controller.go` — on reconcile, check if another RunnerPool in any namespace targets the same `agentPoolId`. If so, set a Warning event (not an error — it might be intentional for multi-region).

### Feature 2: Webhook-Driven Scaling

Instead of polling queue depth every 15s, Stackweaver API pushes a webhook when a job is created for faster scale-up.

**Architecture:**

```
Stackweaver API  ──webhook──►  Operator webhook endpoint
                                       │
                                       ▼
                               Bump KEDA metric
                               (or directly scale Deployment)
```

**Implementation:**

| File | Purpose |
|------|---------|
| `internal/webhook/handler.go` | HTTP handler for `POST /webhook/job-created` |
| `internal/webhook/server.go` | Webhook HTTP server (separate from gRPC scaler) |
| `internal/webhook/auth.go` | Webhook signature verification (HMAC) |

**Webhook payload** (defined by Stackweaver API):
```json
{
  "event": "job.created",
  "agent_pool_id": "550e8400-e29b-41d4-a716-446655440000",
  "job_type": "terraform_run",
  "pending_count": 5
}
```

**On receipt:**
- Trigger immediate reconcile for the matching RunnerPool (via channel or direct Deployment scale)
- Update the scaler cache with the new pending count
- KEDA's next poll picks up the pre-warmed cache → faster than waiting for poll

**KEDA alternative**: Use KEDA's built-in HTTP scaler (`keda.sh/http-add-on`) instead of custom webhook handling. This is simpler but adds another dependency.

**Stackweaver platform changes:**
- New webhook delivery system (queue + retry)
- Webhook registration API (`POST /api/v2/webhook-subscriptions`)
- Webhook configuration in RunnerPool CRD (`spec.webhook.url`, `spec.webhook.secret`)

**Decision**: This is a significant feature spanning both repos. Defer until there's user demand for sub-15s scale-up latency.

### Feature 3: Multi-Cloud Identity Per Pool

A single RunnerPool where runners can target multiple cloud accounts in a single job.

**Current limitation**: `spec.cloudIdentity` supports exactly one provider. A runner with AWS IRSA can only assume one IAM role.

**Options:**
1. **Multiple cloudIdentity entries** — RunnerPool spec gets `cloudIdentities: []` (array) instead of single `cloudIdentity`. Controller creates one SA with all annotations. This works for AWS IRSA (multiple role annotations aren't standard, but the pod could use `AssumeRoleWithWebIdentity` for secondary roles).

2. **Stackweaver OIDC tokens** — The platform already supports OIDC workload identity (`TFC_WORKLOAD_IDENTITY_TOKEN`). Runners can use OIDC federation to assume roles in multiple clouds without pod-level identity. This is already built and doesn't need operator changes.

3. **Per-workspace identity** — Different workspaces in the same pool configure their own cloud credentials via Stackweaver variables. The runner gets credentials via job artifacts, not pod identity.

**Decision**: Option 2 and 3 already cover the multi-cloud use case without operator changes. Document these patterns instead of adding CRD complexity. Revisit only if users need pod-level multi-cloud identity for non-Terraform workloads.

## E2E Test Scenarios

| # | Scenario |
|---|----------|
| Multiple RunnerPools targeting different agent pools in same namespace |
| RunnerPool with duplicate agentPoolId → warning event |
| (If webhook implemented) Webhook triggers immediate scale-up |

## Files Summary

| Action | Path |
|--------|------|
| Modify | `internal/controller/runnerpool_controller.go` (duplicate agentPoolId check) |
| Create | `internal/webhook/` (if webhook feature is implemented) |
| Create | Documentation for multi-pool patterns and multi-cloud identity |
