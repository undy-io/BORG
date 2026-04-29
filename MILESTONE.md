# Milestone 3: Hard Go Runtime Cutover

## Status Snapshot
- Previous milestone: Go core proxy, Kubernetes discovery, token generation, and KinD validation are complete.
- Current milestone: switch default Docker, Helm, and CI/release validation to the Go runtime.
- Python source, Python tests, `genkey.py`, and Python package metadata remain in-tree as the reference/rollback path.
- The root Docker image now targets Go BORG by default; host Docker build validation is still pending because the devcontainer cannot run nested Docker build steps.
- The Helm chart continues to use the same values, env vars, config mount path, service port, and probes.
- The Go service now handles `SIGTERM`/`SIGINT` with graceful HTTP shutdown before app discovery cleanup.
- Go CI has been added while Python CI remains active.
- Docker-in-Docker KinD inside the devcontainer remains blocked by the current cgroup environment; run real KinD validation from raw WSL/host.

## Objective
Make Go the default deployable BORG runtime without removing the Python fallback.

The cutover should change the implementation behind the existing operational interface, not the interface itself. Existing Helm users should continue to configure image, auth Secret, API-key Secrets, runtime config, and service ports the same way.

## Scope
In scope:
- Root Dockerfile uses Go multi-stage build.
- Production container exposes `/usr/local/bin/borg`.
- Production container includes `/usr/local/bin/borg-genkey`.
- Default container command runs Go BORG with `--host 0.0.0.0`.
- Go BORG handles Kubernetes termination signals gracefully.
- `/app/config.yaml` remains the default config path and Helm mount target.
- Helm values schema and value names remain stable.
- Go CI is added.
- Python CI remains active.
- README, roadmap, and recovery docs reflect the Go default runtime.

Out of scope:
- Removing Python source, tests, package metadata, or `genkey.py`.
- Adding a Helm runtime selector.
- Changing auth Secret key defaults.
- Changing discovery semantics or proxy behavior.
- Moving KinD validation into CI.

## Checkpoints

### Checkpoint 1: Container Runtime
Tasks:
- [x] Replace the Python root Dockerfile with a Go multi-stage Dockerfile.
- [x] Build `./cmd/borg` as `/usr/local/bin/borg`.
- [x] Build `./cmd/borg-genkey` as `/usr/local/bin/borg-genkey`.
- [x] Keep `/app/config.yaml` in the image as the default config path.
- [x] Run the default container command as `/usr/local/bin/borg --host 0.0.0.0`.
- [x] Add `.dockerignore` so Docker builds do not send local caches, virtualenvs, or build artifacts.
- [x] Leave `entrypoint.sh` in place for the Python rollback path, but remove it from the default image flow.
- [x] Run the container as root for this cutover to preserve low `service.targetPort` compatibility.

Validation:
- [ ] `docker build -t borg-go:cutover .`

Notes:
- Docker build cannot complete inside the current devcontainer because nested Docker cannot create cpuset cgroups during `RUN` steps. The Dockerfile reached `RUN go mod download` before hitting the known environment error. Run this validation from raw WSL/host.
- Non-root runtime hardening is deferred to a follow-up that can add chart-level `securityContext` and port/capability policy.

### Checkpoint 2: Go Runtime Shutdown
Tasks:
- [x] Handle `SIGTERM` and `SIGINT` with `signal.NotifyContext`.
- [x] Run `ListenAndServe` in a goroutine and wait for either server exit or cancellation.
- [x] Use `server.Shutdown` with a `30s` timeout on signal.
- [x] Fall back to `server.Close` if graceful shutdown fails.
- [x] Treat `http.ErrServerClosed` as a clean exit.
- [x] Keep `App.Close()` deferred so discovery stops after HTTP shutdown.

Validation:
- [x] `go test ./cmd/borg`
- [x] Tests cover listen errors, clean `http.ErrServerClosed`, context cancellation, and close fallback after shutdown failure.

### Checkpoint 3: Helm Runtime
Tasks:
- [x] Keep existing Helm values stable.
- [x] Keep Deployment env wiring for `PORT`, `PROXY_CONFIG`, `AUTH_KEY`, and per-instance `apikeyEnv` values.
- [x] Keep the ConfigMap mounted at `/app/config.yaml`.
- [x] Keep `authKeySecret.key` defaulted to `BORG_AUTH_KEY` for Secret compatibility.
- [x] Add chart comments noting that the default image now runs Go BORG.
- [x] Avoid adding a Python/Go runtime selector.

Validation:
- [x] `helm lint ./charts/borg`
- [x] `helm template borg ./charts/borg --debug`

### Checkpoint 4: CI And Release
Tasks:
- [x] Add Go CI for tests, vet, and command builds.
- [x] Keep Python CI active.
- [x] Keep Docker publish workflow pointed at the root Dockerfile.
- [x] Rename Docker workflow labels to clarify it builds the Go runtime image.
- [x] Keep Helm CI unchanged except for compatibility with Go-default chart rendering.
- [x] Ignore root-level `borg` and `borg-genkey` binaries produced by local `go build ./cmd/...` validation commands.

Validation:
- [x] `go test ./...`
- [x] `go vet ./...`
- [x] `go build ./cmd/borg`
- [x] `go build ./cmd/borg-genkey`

### Checkpoint 5: Documentation And Recovery
Tasks:
- [x] Update README to describe Go as the default runtime.
- [x] Update README Docker, Helm, token generation, testing, and release sections.
- [x] Update `ROADMAP.md` to mark the cutover milestone as active/completed.
- [x] Update `SESSION_RECOVERY.md` with the new runtime default and next cleanup lane.
- [x] Update Go migration docs so they no longer describe Docker/Helm cutover as future work.
- [x] Keep Python removal explicitly out of scope.

Validation:
- [x] `git diff --check`

### Checkpoint 6: End-To-End Validation
Tasks:
- [x] Keep Python tests green.
- [x] Keep local Python-vs-Go smoke parity green.
- [x] Keep fake Kubernetes discovery smoke green.
- [ ] Keep real KinD validation green from raw WSL/host.

Validation:
- [x] `uv run pytest -q`
- [x] `uv run pytest -q tests/smoke`
- [x] `uv run pytest -q tests/k8s_smoke`
- [ ] `scripts/validate-kind-go.sh --create-cluster --delete-cluster`

## Remaining Work
- Run `docker build -t borg-go:cutover .` from raw WSL/host.
- Run `scripts/validate-kind-go.sh --create-cluster --delete-cluster` from raw WSL/host.
- Commit the cutover once host Docker and KinD validation are green.
- After the Go-default runtime is proven, plan the cleanup milestone for Python removal/archive, devcontainer simplification, and final documentation cleanup.
