# API Client Specification

This document specifies the Stackweaver API client used by the operator's controllers and KEDA scaler to communicate with the Stackweaver platform.

## Overview

The operator communicates with Stackweaver exclusively via its REST API. There are no shared Go modules or direct database access — the API is the only integration point. This is a deliberate architectural choice that keeps the operator fully decoupled from the platform codebase.

## Endpoints Consumed

### Authentication

All requests include an `Authorization: Bearer {api-key}` header. The API key is read from the Kubernetes Secret referenced by the RunnerPool's `spec.tokenSecretRef`.

The API key must have the `runner:register` scope (same scope used by self-hosted runners). No additional scopes are required.

### Endpoint Inventory

| Endpoint | Method | Used By | Purpose |
|----------|--------|---------|---------|
| `/api/v2/agent-pools/{id}/queue-depth` | GET | Scaler, fallback scaler | Get pending job count for scaling decisions (new endpoint, Phase 2) |
| `/api/v2/agent-pools/{id}/agents` | GET | Runner controller | List runners in pool — TFE-compatible, returns name, status, last-ping-at |
| `/api/v2/runners/{id}` | GET | Runner controller | Get individual runner detail (current-jobs, heartbeat, labels). Returns status=offline after deregistration. |
| `/api/v2/runner/register` | POST | (runner agent, not operator) | Runner self-registration — operator doesn't call this |
| `/api/v2/runner/heartbeat` | POST | (runner agent, not operator) | Runner heartbeat — operator doesn't call this |

Note: The operator does **not** register or deregister runners. The runner agent binary inside each pod handles its own registration and heartbeat lifecycle. The operator only reads runner state for status reporting.

**Registration discovery**: The Runner controller finds newly registered runners by calling `GET /api/v2/agent-pools/{id}/agents` and matching `attributes.name` against known pod names. This endpoint returns all runners in a pool in one call.

**Deregistration verification**: During finalizer cleanup, the controller polls `GET /api/v2/runners/{id}` for each known runner. A `status=offline` response indicates the runner has successfully deregistered (the platform's deregister endpoint marks runners offline, it does not delete them). The controller waits until all runners report `offline` or the 5-minute timeout expires.

### Queue Depth (Scaler)

```
GET /api/v2/agent-pools/{agentPoolId}/queue-depth

Response 200:
{
  "data": {
    "type": "queue-depths",
    "id": "{agentPoolId}",
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

Response 401: { "errors": [{ "status": "401", "title": "Unauthorized" }] }
Response 404: { "errors": [{ "status": "404", "title": "Agent pool not found" }] }
```

### List Agents in Pool (Runner Controller — batch sync)

```
GET /api/v2/agent-pools/{agentPoolId}/agents

Response 200:
{
  "data": [
    {
      "type": "agents",
      "id": "{runner-uuid}",
      "attributes": {
        "name": "terraform-runners-abc123",
        "status": "online",
        "ip-address": "10.0.0.5",
        "last-ping-at": "2026-04-20T12:00:00Z"
      }
    }
  ]
}
```

This is a TFE-compatible endpoint. It returns a simplified view (no `current-jobs` or `labels`). For full runner detail, use `GET /api/v2/runners/{id}` per runner.

### Get Runner (Runner Controller)

```
GET /api/v2/runners/{runnerId}

Response 200:
{
  "data": {
    "type": "runners",
    "id": "{runner-uuid}",
    "attributes": {
      "name": "terraform-runners-abc123",
      "status": "online",
      "runner-type": "terraform",
      "current-jobs": 1,
      "last-heartbeat-at": "2026-04-20T12:00:10Z",
      "hostname": "terraform-runners-abc123-pod",
      "labels": ["aws", "us-east-1"]
    }
  }
}
```

## Client Configuration

### Construction

```go
type ClientConfig struct {
    // ServerURL is the base URL of the Stackweaver API.
    ServerURL string

    // APIKey is the authentication token.
    APIKey string

    // HTTPClient is the underlying HTTP client (allows TLS customization).
    HTTPClient *http.Client

    // UserAgent identifies the operator version.
    UserAgent string
}
```

The client is constructed per-RunnerPool (each pool may target a different Stackweaver instance with different credentials). Clients are cached by the controller and recreated when `serverUrl` or `tokenSecretRef` changes.

### User-Agent

All requests include a `User-Agent` header:
```
Stackweaver-Operator/{version} (kubernetes/{k8s-version})
```

### Timeouts

| Setting | Value | Rationale |
|---------|-------|-----------|
| Connection timeout | 5s | Fail fast on unreachable API |
| Request timeout | 10s | Queue depth and runner list are lightweight queries |
| Idle connection timeout | 90s | Keep connections alive for frequent polling |
| Max idle connections | 10 | One connection per RunnerPool, with headroom |

### TLS

- TLS verification is enabled by default
- Custom CA certificates can be provided via a ConfigMap or Secret (for self-signed certs on private Stackweaver instances)
- Skip TLS verification is available as a flag but logged as a warning

## Retry Strategy

### Retryable Errors

| Error | Retryable | Behavior |
|-------|-----------|----------|
| Connection refused / timeout | Yes | Retry with backoff |
| HTTP 429 (Rate Limited) | Yes | Retry after `Retry-After` header value, or 30s default |
| HTTP 500, 502, 503, 504 | Yes | Retry with backoff |
| HTTP 401 (Unauthorized) | No | Token invalid — set condition, don't retry |
| HTTP 403 (Forbidden) | No | Insufficient scope — set condition, don't retry |
| HTTP 404 (Not Found) | No | Agent pool doesn't exist — set condition, don't retry |
| HTTP 400 (Bad Request) | No | Client error — log and don't retry |

### Backoff Configuration

- Strategy: exponential backoff with jitter
- Initial interval: 1 second
- Multiplier: 2
- Max interval: 30 seconds
- Max retries: 3 (per request)
- Jitter: +/- 20%

### Circuit Breaker

The client implements a circuit breaker per server URL to prevent hammering a down API:

| State | Behavior |
|-------|----------|
| Closed (normal) | Requests pass through normally |
| Open (tripped) | All requests fail immediately without making HTTP calls. Returns last cached response if available. |
| Half-open (probing) | Allow one request through. If it succeeds, close the circuit. If it fails, re-open. |

**Thresholds:**
- Open after: 5 consecutive failures
- Half-open after: 60 seconds in open state
- Close after: 1 successful request in half-open state

When the circuit opens, the controller sets condition `APIReachable=False` on affected RunnerPools.

## Secret Watching

The client needs to reload the API key when the referenced Secret changes (e.g. key rotation). The controller watches Secrets and invalidates the cached client when:

1. A Secret referenced by any RunnerPool's `tokenSecretRef` is updated
2. The controller recreates the API client with the new key on next reconcile
3. In-flight requests with the old key may fail with 401 — the retry logic handles this by fetching the new key

## Response Parsing

All Stackweaver API responses follow JSON:API format. The client:

1. Checks HTTP status code first
2. For error responses (4xx/5xx), parses the `errors` array and returns a structured error
3. For success responses, parses `data` (single resource) or `data[]` (collection)
4. Handles pagination via `meta.pagination` if present (for runner list endpoint)

```go
type APIError struct {
    StatusCode int
    Errors     []APIErrorDetail
}

type APIErrorDetail struct {
    Status string `json:"status"`
    Title  string `json:"title"`
    Detail string `json:"detail"`
}
```
