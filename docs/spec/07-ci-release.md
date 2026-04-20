# CI/CD & Release Specification

This document specifies the build pipeline, release process, and versioning strategy for the Stackweaver Runner Operator.

## Repository

- **Repo**: `vhco-pro/stackweaver-operator`
- **License**: BSL 1.1 (matches main platform)
- **Primary branch**: `main`
- **CI**: GitHub Actions

## CI Pipeline

### On Every Push / PR

```yaml
name: CI
on:
  push:
    branches: [main]
  pull_request:
    branches: [main]
```

#### Jobs

| Job | Steps | Duration |
|-----|-------|----------|
| **lint** | `golangci-lint run ./...` | ~30s |
| **unit-test** | `go test ./... -short` | ~10s |
| **integration-test** | `make test` (envtest) | ~60s |
| **build** | `go build ./cmd/...`, `docker build` (no push) | ~60s |
| **e2e-test** | Create kind cluster, deploy operator, run E2E suite | ~5-10 min |
| **build-stub-runner** | Build stub runner image for E2E tests (`test/stub-runner/Dockerfile`) | ~30s |

All jobs except e2e-test run in parallel. E2E depends on `build` and `build-stub-runner` (needs both images loaded into the kind cluster).

#### Linting

```yaml
lint:
  runs-on: ubuntu-latest
  steps:
    - uses: actions/checkout@v4
    - uses: actions/setup-go@v5
      with:
        go-version-file: go.mod
    - uses: golangci/golangci-lint-action@v6
      with:
        version: latest
```

Linter config (`.golangci.yml`) should match the main Stackweaver repo's style where applicable.

#### Build Matrix

| OS | Arch | Image Platform |
|----|------|----------------|
| linux | amd64 | `linux/amd64` |
| linux | arm64 | `linux/arm64` |

Multi-arch images built with `docker buildx`.

### On Release Tag

```yaml
name: Release
on:
  push:
    tags: ["v*"]
```

#### Steps

1. **Build & push multi-arch image** to `ghcr.io/vhco-pro/stackweaver-operator:{tag}`
2. **Tag as latest** if this is the newest SemVer (not a pre-release)
3. **Package Helm chart** with `helm package`
4. **Push Helm chart** to OCI registry (`ghcr.io/vhco-pro/charts/stackweaver-operator`)
5. **Create GitHub Release** with changelog, binaries, and chart artifact
6. **Generate SBOM** for supply chain security

## Versioning

### Operator Version

Follows [Semantic Versioning](https://semver.org/):

- **MAJOR**: Breaking CRD changes (field removal, incompatible behavior change)
- **MINOR**: New features, new CRD fields (backward-compatible)
- **PATCH**: Bug fixes, dependency updates

Pre-release versions: `v0.1.0-alpha.1`, `v0.1.0-beta.1`, `v0.1.0-rc.1`

### CRD API Version

| CRD Version | Operator Version | Stability |
|-------------|-----------------|-----------|
| `v1alpha1` | 0.x.x | Unstable — fields may change between releases |
| `v1beta1` | 1.x.x (planned) | Stable schema, conversion webhooks for upgrades |
| `v1` | 2.x.x (planned) | GA — no breaking changes within major version |

### Helm Chart Version

The chart version tracks the operator version: chart `0.1.0` deploys operator `0.1.0`. The `appVersion` field in `Chart.yaml` always matches the operator image tag.

### Compatibility Matrix

| Operator Version | Min K8s Version | KEDA Version | Stackweaver API |
|-----------------|-----------------|--------------|-----------------|
| 0.1.x | 1.34+ | 2.12+ | v2 (queue-depth endpoint) |
| 0.2.x | 1.34+ | 2.12+ | v2 |

## Container Image

### Image Name

```
ghcr.io/vhco-pro/stackweaver-operator:{tag}
```

### Tags

| Tag | Description |
|-----|-------------|
| `v0.1.0` | Immutable release tag |
| `latest` | Points to newest stable release |
| `main` | Built from main branch HEAD (unstable, for development) |
| `sha-{short}` | Built from specific commit (for CI debugging) |

### Dockerfile

The operator Dockerfile (already scaffolded by kubebuilder) uses a multi-stage build:

1. **Builder stage**: Go build with CGO_ENABLED=0
2. **Runtime stage**: `gcr.io/distroless/static:nonroot`

The image runs as non-root (UID 65532) by default.

### Image Signing

Release images are signed with [cosign](https://github.com/sigstore/cosign) for supply chain verification:

```bash
cosign verify ghcr.io/vhco-pro/stackweaver-operator:v0.1.0
```

## Helm Chart Publishing

### OCI Registry

Charts are published as OCI artifacts to GitHub Container Registry:

```bash
# Push (in CI)
helm push stackweaver-operator-0.1.0.tgz oci://ghcr.io/vhco-pro/charts

# Install (by users)
helm install stackweaver-operator oci://ghcr.io/vhco-pro/charts/stackweaver-operator \
  --version 0.1.0
```

### GitHub Pages (Optional)

As an alternative to OCI, a Helm chart repository can be hosted on GitHub Pages:

```bash
helm repo add stackweaver https://vhco-pro.github.io/stackweaver-operator
helm install stackweaver-operator stackweaver/stackweaver-operator
```

This requires a `gh-pages` branch with an `index.yaml`. The OCI approach is preferred as it requires less infrastructure.

## Release Process

### Automated (Preferred)

1. Merge PRs to `main`
2. When ready to release, create and push a tag:
   ```bash
   git tag v0.1.0
   git push origin v0.1.0
   ```
3. GitHub Actions release workflow triggers automatically:
   - Builds multi-arch image
   - Pushes to GHCR
   - Packages and pushes Helm chart
   - Creates GitHub Release with auto-generated changelog
   - Signs image with cosign

### Changelog

Generated automatically from conventional commit messages since the last tag:

- `feat:` → "Features" section
- `fix:` → "Bug Fixes" section
- `BREAKING CHANGE:` → "Breaking Changes" section (highlighted)

Use [release-please](https://github.com/googleapis/release-please) or [goreleaser](https://goreleaser.com/) for automation.

## Branch Strategy

| Branch | Purpose | Protection |
|--------|---------|------------|
| `main` | Primary development branch | Required PR, CI must pass |
| `release-0.x` | Maintenance branches for patch releases | Created when 0.x+1 development starts |
| Feature branches | Development work | No protection, squash-merge to main |

## Security

### Dependency Updates

- Renovate or Dependabot for Go module and GitHub Actions updates
- Weekly update schedule for non-critical dependencies
- Immediate updates for security vulnerabilities (via Renovate's vulnerability alerts)

### Vulnerability Scanning

- `govulncheck` in CI on every push
- Container image scanning with `trivy` on every build
- Results uploaded to GitHub Security tab (SARIF format)

### Supply Chain

- SLSA Level 2 provenance via GitHub Actions
- Signed images (cosign)
- SBOM generation (syft) attached to GitHub Releases
- Reproducible builds (Go's reproducible build support)
