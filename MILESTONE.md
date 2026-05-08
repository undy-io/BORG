# Milestone 6: Python Cleanup And Finalization

## Status Snapshot
- Previous milestone: Go became the default Docker, Helm, and CI runtime.
- Current milestone: remove the retired Python BORG runtime and active Python/UV tooling.
- The active service lives under `cmd/` and `internal/`.
- The production image builds `/usr/local/bin/borg` and `/usr/local/bin/borg-genkey`.
- The dedicated Python CI workflow has been removed.
- The Python package build path, legacy `genkey.py`, Python runtime tests, Python-vs-Go parity smoke suite, UV tooling, and devcontainer Python setup have been removed.
- The fake Kubernetes smoke harness is now Go-native and runs the real Go binary.
- The `dummy-openai` validation backend has been replaced with a tiny Go service.
- Host Docker build validation remains the only cutover gate that cannot be proven inside this devcontainer.

## Objective
Make Go the only active BORG runtime while preserving the useful Go validation harnesses.

This cleanup removes rollback code from the active source tree. Historical Python contract docs may remain until the migration documentation is archived or simplified.

## Scope
In scope:
- Remove `src/borg/`.
- Remove legacy `genkey.py`.
- Remove the Python package build path, package manifest, and lockfile.
- Remove Python runtime tests.
- Remove the Python-vs-Go parity smoke suite under `tests/smoke`.
- Keep `tests/k8s_smoke` as a Go-native process-level harness for the Go binary.
- Keep `dummy-openai/` as a Go test backend for KinD validation.
- Remove Python/UV setup from the devcontainer.
- Let Go CI cover smoke validation through `go test ./...`.
- Update README, roadmap, and recovery docs to describe the Go-only runtime.

Out of scope:
- Moving historical Python contract docs into an archive.
- Changing Helm values or discovery semantics.
- Moving real KinD validation into CI.

## Checkpoints

### Checkpoint 1: Runtime Removal
Tasks:
- [x] Remove `src/borg/`.
- [x] Remove `genkey.py`.
- [x] Remove `entrypoint.sh`.
- [x] Remove Python runtime tests.
- [x] Remove `tests/smoke`.

Validation:
- [x] No active docs or CI reference the removed runtime paths.

### Checkpoint 2: Go-Native Smoke Harness
Tasks:
- [x] Keep `tests/k8s_smoke`.
- [x] Port the fake Kubernetes smoke harness to Go.
- [x] Build `./cmd/borg` once in `TestMain`.
- [x] Delete the Python package manifest and lockfile.
- [x] Remove Python/UV setup from the devcontainer.
- [x] Remove the dedicated Go CI smoke job; `go test ./...` runs the smoke package.

Validation:
- [x] `go test ./tests/k8s_smoke`

### Checkpoint 3: Go Runtime Validation
Tasks:
- [x] Keep Go CI for tests, vet, and command builds.
- [x] Keep Docker publish workflow pointed at the root Go Dockerfile.
- [x] Keep Helm CI unchanged.

Validation:
- [x] `go test ./...`
- [x] `go test ./tests/k8s_smoke`
- [x] `go vet ./...`
- [x] `go build ./cmd/borg`
- [x] `go build ./cmd/borg-genkey`
- [x] `go build -o /tmp/dummy-openai ./dummy-openai`
- [x] `bash -n scripts/validate-kind-go.sh`
- [x] `bash -n .devcontainer/post-create.sh`
- [x] `helm lint ./charts/borg`
- [x] `helm template borg ./charts/borg --debug`
- [x] `helm lint ./dummy-openai/charts/dummy-openai`
- [x] `helm template dummy-openai ./dummy-openai/charts/dummy-openai --debug`
- [x] `git diff --check`

### Checkpoint 4: Host Validation
Tasks:
- [x] Keep real KinD validation available from raw WSL/host.
- [ ] Keep host Docker image build as the remaining environment-specific gate.

Validation:
- [x] `scripts/validate-kind-go.sh --create-cluster --delete-cluster`
- [x] `docker build -t dummy-openai:kind ./dummy-openai`
- [ ] `docker build -t borg-go:cutover .`

## Remaining Work
- Run `docker build -t borg-go:cutover .` from raw WSL/host or CI.
- Simplify or archive the historical migration docs after the cleanup is merged.
