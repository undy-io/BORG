# BORG Go Migration Roadmap

## Goal
Migrate BORG from Python to Go using a side-by-side strategy that preserves current behavior, keeps rollback simple, and allows us to validate parity before cutover.

The Python implementation remains the reference service until the Go implementation has proven feature and operational parity.

## Working Model
- This roadmap stays high level and milestone-oriented.
- We will work one milestone at a time.
- The active milestone will be expanded into a repo-root `MILESTONE.md`.
- `MILESTONE.md` will contain concrete tasks, sequencing, validation steps, and notes for the current milestone only.
- We do not remove the Python implementation until the final cutover milestone is complete.

## Guiding Principles
- Preserve the external contract first: config shape, env vars, auth token format, HTTP endpoints, streaming behavior, and Helm-facing deployment inputs.
- Prefer parity over early optimization.
- Keep the Go service deployable in parallel with the Python service for comparison and rollback.
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

Exit criteria:
- The repo can build and run the Go service independently.
- The Go codebase has a stable structure we can extend without reworking foundations.
- Local development flow is clear for both Python and Go during the migration window.

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
- The Go service is useful in development even if Kubernetes discovery is not finished yet.

## Milestone 4: Port Discovery And Operational Tooling
Bring over the cluster-aware and operational parts that make BORG deployable in real environments.

Outcomes:
- Implement Kubernetes discovery and periodic refresh behavior in Go.
- Preserve annotation and selector-driven endpoint discovery behavior.
- Port or replace the token generation utility so issued tokens remain compatible.
- Add container build support and any required CI jobs for the Go implementation.
- Keep Helm and deployment inputs aligned while the repo supports both runtimes.

Exit criteria:
- The Go service can discover and manage backend instances in the same environments the Python service supports today.
- Supporting tooling exists for auth key/token workflows.
- Build and deployment automation can exercise the Go path without disrupting the Python fallback path.

## Milestone 5: Side-By-Side Validation And Cutover
Prove production readiness, switch defaults, and retire the Python runtime cleanly.

Outcomes:
- Run side-by-side validation between Python and Go for representative traffic and deployment scenarios.
- Close remaining parity gaps and document any accepted deltas.
- Switch CI, container images, and Helm defaults to the Go implementation.
- Simplify the devcontainer from dual-runtime migration mode to the final default developer workflow.
- Preserve rollback instructions during the transition window.
- Remove or archive the Python runtime only after the Go path is the clear default and operationally trusted.

Exit criteria:
- The Go implementation is the default supported runtime for BORG.
- Deployment, testing, and release flows target the Go service.
- The migration is complete, with rollback no longer dependent on active Python maintenance.

## Notes For Milestone Planning
When we create a `MILESTONE.md`, it should:
- Focus on one milestone only.
- Break the milestone into small, verifiable tasks.
- Define the validation needed for each task.
- Track assumptions, blockers, and scope decisions as we go.
- End with a clear definition of done for that milestone.
