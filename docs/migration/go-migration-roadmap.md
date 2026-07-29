# BORG Go Migration Roadmap (Completed)

## Completion Status

The Python-to-Go migration is complete. BORG `v0.2.0`, released on July 28,
2026, is the completion release for this roadmap.

- Go is the only active BORG runtime.
- The root container image, Helm chart, CI, and release workflows target Go.
- The retired Python runtime, packaging, tests, token utility, and active Python
  tooling have been removed.
- The remaining Python documents in this directory are historical contract
  records only.
- Future BORG features belong in a product roadmap, not this migration record.

## Completed Milestones

### Milestone 1: Freeze The Python Contract

Captured the Python HTTP, configuration, authentication, discovery, and
deployment behavior needed to guide the replacement runtime. Known quirks and
intentional compatibility requirements were recorded before implementation.

### Milestone 2: Create The Go Service Skeleton

Added the Go module, command entrypoints, internal package structure,
configuration loading, basic HTTP service, and local development workflow.

### Milestone 3: Reach Request Path Parity

Implemented model registration, round-robin routing, `/v1/models`, authenticated
OpenAI-compatible forwarding, upstream credential replacement, and streaming
responses with package-level coverage.

### Milestone 4: Port Discovery And Operational Tooling

Implemented Kubernetes Pod and Service discovery, source-isolated reconciliation,
bounded model enumeration, the Go `borg-genkey` utility, fake-Kubernetes smoke
coverage, and the host KinD validation harness.

### Milestone 5: Side-By-Side Validation And Cutover

Switched the root container image, Helm chart, CI, and release workflows to Go.
The Python fallback was retired after the Go path became the supported runtime.

### Milestone 6: Python Cleanup And Finalization

Removed the Python runtime and migration scaffolding, converted active smoke
validation to Go, completed Kubernetes readiness hardening, added stable Service
front-door discovery for llm-d-style routers, and published `v0.2.0`.

## Completion Evidence

The completion baseline includes:

- green Go CI tests, vet, and `golangci-lint`, plus a completed local race run;
- strict Helm lint and the chart render validation matrix;
- a green GitHub container workflow using the root Dockerfile and a green chart
  release workflow;
- a successful host/raw WSL KinD create/delete run on July 28, 2026;
- real-cluster checks for Pod discovery, auth, non-streaming and SSE forwarding;
- a config-only Helm upgrade that changed the checksum and created a new
  ReplicaSet; and
- the `v0.2.0` runtime image and Helm chart release.

Detailed Milestone 6 evidence is retained in
`docs/migration/milestone-6-finalization.md`.

## Historical Records

- `docs/migration/python-runtime-contract.md`
- `docs/migration/python-http-contract.md`
- `docs/migration/python-ops-contract.md`
- `docs/migration/milestone-6-finalization.md`

Current architecture and validation documentation lives outside this historical
directory and is linked from the repository README.
