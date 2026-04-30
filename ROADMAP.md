# BORG Go Migration Roadmap

## Goal
Migrate BORG from Python to Go using a side-by-side strategy that preserved current behavior through cutover and then retired the Python implementation once Go became the default runtime.

The Go implementation is now the only active BORG runtime. Python remains only for retained Go smoke-test harnesses and the dummy OpenAI test backend.

## Current Status
- Milestone 1 is complete.
- The Python contract is frozen in `docs/migration/`.
- The Go service is implemented for core proxying, Kubernetes discovery, and token generation.
- Go proxy review hardening is complete for compression/header handling and backend API key precedence.
- The old Python-vs-Go side-by-side smoke harness has been removed with the Python runtime.
- Go Kubernetes discovery is implemented behind the existing static proxy path.
- Fake Kubernetes API smoke validation for Go discovery is implemented under `tests/k8s_smoke`.
- Go `borg-genkey` is implemented and the legacy Python `genkey.py` has been removed.
- The root Docker image now targets Go BORG by default; host Docker build validation is the remaining cutover gate.
- The Helm chart now deploys the Go runtime by default while preserving its values shape and runtime wiring.
- Go CI has been added; the dedicated Python CI workflow has been removed from the active branch-gating path after the Go default cutover.
- Devcontainer Docker/KinD/kubectl/Helm tooling installs, but Docker-in-Docker KinD is blocked in the current rootless/containerized WSL environment by non-writable cpuset cgroups.
- Host/raw WSL KinD validation works with the node image pinned to Kubernetes v1.34.3.
- A repeatable host/raw WSL KinD Go validation script exists at `scripts/validate-kind-go.sh` and has passed the full create/delete path.
- The KinD harness validates real discovery, `/v1/models`, missing-auth rejection, authenticated POST forwarding, upstream auth rewrite, and streaming SSE.
- The Python runtime, Python package build path, Python runtime tests, and dedicated Python CI workflow have been removed.
- The next major implementation lane is final cleanup: simplify remaining migration docs, reduce devcontainer Python assumptions, and eventually port or replace the retained Python smoke harness.

## Working Model
- This roadmap stays high level and milestone-oriented.
- We will work one milestone at a time.
- The active milestone will be expanded into a repo-root `MILESTONE.md`.
- `MILESTONE.md` will contain concrete tasks, sequencing, validation steps, and notes for the current milestone only.
- The Python implementation has been removed after the Go cutover.

## Guiding Principles
- Preserve the external contract first: config shape, env vars, auth token format, HTTP endpoints, streaming behavior, and Helm-facing deployment inputs.
- Prefer parity over early optimization.
- Keep historical Python contract docs available for context until the migration docs are simplified.
- Validate behavior with automated tests and side-by-side checks before switching production defaults.
- Keep operational changes incremental so CI, container builds, and Helm remain understandable throughout the migration.

## Milestone 1: Freeze The Python Contract
Establish the migration baseline and document the behavior the Go service must match.

Outcomes:
- Confirm the supported runtime contract of the current Python service.
- Identify the exact behaviors that must remain stable during migration.
- Capture known bugs, ambiguities, and intentional quirks so we do not accidentally "fix" behavior mid-port.
- Decide what counts as parity for HTTP behavior, auth, discovery, configuration, and deployment.

Exit criteria:
- Current behavior is documented well enough to port without guesswork.
- Parity expectations are explicit.
- We agree on what is in scope for the first Go version and what can wait until after cutover.

## Milestone 2: Create The Go Service Skeleton
Introduce a Go implementation into the repository without changing the production path yet.

Outcomes:
- Add the Go module, package layout, build targets, and local run instructions.
- Evolve the devcontainer into a dual-runtime Python+Go environment so we can build both implementations during migration.
- Stand up a minimal Go HTTP service with health/basic endpoints and configuration loading.
- Establish the internal structure for router, proxy, auth, discovery, and CLI/tooling code.
- Make the Go service runnable beside the Python service in local development.
- Keep the Python implementation, tests, Dockerfile, and Helm chart in place as the reference path during the side-by-side phase.

Exit criteria:
- The repo can build and run the Go service independently.
- The Go codebase has a stable structure we can extend without reworking foundations.
- Local development flow is clear for both Python and Go during the migration window.

Layout target:
- The planned Go tree is documented in `docs/migration/go-project-layout.md`.
- The local smoke/parity harness was removed when the Python runtime was retired.
- The fake Kubernetes API smoke harness is implemented under `tests/k8s_smoke` and documented in `docs/migration/go-k8s-smoke-test-harness.md`.
- The primary service entrypoint should live at `cmd/borg` and build to `bin/borg-go` during migration.
- Application internals should live under `internal/`.

## Milestone 3: Reach Request Path Parity
Port the core proxy behavior that makes BORG useful as an OpenAI-compatible router.

Outcomes:
- Implement model registration and round-robin endpoint selection.
- Implement the `/v1/models` union endpoint.
- Implement request forwarding for non-streaming and streaming OpenAI-compatible calls.
- Preserve authorization handling, upstream API key swapping, and error semantics closely enough for side-by-side validation.
- Port or recreate the relevant tests against the Go implementation.

Exit criteria:
- The Go service can serve the main proxy use cases with parity to the Python implementation.
- Core tests for routing, auth, and streaming pass for the Go implementation.
- The Go service is useful in development while deployment wiring is still Python-first.

## Milestone 4: Port Discovery And Operational Tooling
Bring over the cluster-aware and operational parts that make BORG deployable in real environments.

Note: Kubernetes polling discovery, the Go token utility, and local KinD validation were pulled forward into the side-by-side implementation phase so the Go runtime could be tested before cutover. The remaining operational work is now focused on switching the deployment path to Go and adding release/CI confidence.

Outcomes:
- Validate Kubernetes discovery in a local fake API smoke loop before deployment wiring.
- Validate Docker/KinD/kubectl on a host/runtime with usable cgroups, or move KinD validation to CI/VM infrastructure.
- Use the raw WSL KinD path with the pinned v1.34.3 node image for current local cluster validation.
- Continue running `scripts/validate-kind-go.sh`, which covers discovery, authenticated POST forwarding, and streaming.
- Keep the Go token generation utility compatible with Python-issued and Go-issued tokens.
- Add container build support and any required CI jobs for the Go implementation.
- Keep Helm and deployment inputs aligned while the repo supports both runtimes.

Exit criteria:
- The Go service can discover and manage backend instances in the same environments the Python service supports today.
- Supporting tooling exists for auth key/token workflows.
- Build and deployment automation can exercise the Go path without disrupting the Python fallback path.

## Milestone 5: Side-By-Side Validation And Cutover
Prove production readiness, switch defaults, and retire the Python runtime cleanly.

The Docker, Helm, and CI defaults now target the Go runtime. Python retirement is intentionally deferred to a cleanup milestone.

Outcomes:
- Run validation between Python and Go for representative traffic and deployment scenarios.
- Close remaining parity gaps and document accepted deltas.
- Switch CI, container images, and Helm defaults to the Go implementation.
- Simplify the devcontainer from dual-runtime migration mode to the final default developer workflow.
- Preserve rollback instructions during the transition window.
- Remove or archive the Python runtime only after the Go path is the clear default and operationally trusted.

Exit criteria:
- The Go implementation is the default supported runtime for BORG.
- Deployment, testing, and release flows target the Go service.
- The migration is complete, with rollback no longer dependent on active Python maintenance.

## Milestone 6: Python Cleanup And Finalization
Remove migration scaffolding once the Go-default path is proven.

Outcomes:
- Remove Python runtime code, Python-only packaging, and legacy `genkey.py`.
- Simplify documentation from migration-oriented instructions to normal Go project instructions.
- Simplify the devcontainer to the final Go-first workflow.
- Keep Python contract docs as historical references until migration docs are archived or removed.

Exit criteria:
- Go is the only active BORG runtime.
- The repo no longer requires Python for normal build, release, or deployment flows.
- Rollback no longer depends on maintaining Python source in the main tree.

## Notes For Milestone Planning
When we create a `MILESTONE.md`, it should:
- Focus on one milestone only.
- Break the milestone into small, verifiable tasks.
- Define the validation needed for each task.
- Track assumptions, blockers, and scope decisions as we go.
- End with a clear definition of done for that milestone.
