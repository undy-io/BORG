# BORG Session Recovery

Use this file to resume the Go migration if chat history is lost.

## Current Branch State
- Branch: `go-migration`
- Latest committed baseline: `d4643b Cut over default runtime to Go`
- Current uncommitted work: KinD validation harness resilience for host image-loading failures
- Unrelated local file: `.codex`
- Default deployable runtime: Go BORG
- Python runtime status: retained in-tree as the reference/rollback path
- Deployment default status: root Docker image, Helm chart image path, and Go CI now target the Go runtime
- Cutover review fix status: non-root image hardening is deferred for port compatibility, and Go BORG has graceful SIGTERM/SIGINT shutdown.

Current uncommitted harness fix slice:
- `scripts/validate-kind-go.sh`
- `docs/migration/kind-go-validation-harness.md`
- `SESSION_RECOVERY.md`

Do not stage `.codex`; it is unrelated local state.

## Current Validation State
Latest known green baseline before this cutover:

```bash
uv run pytest -q
uv run pytest -q tests/smoke
uv run pytest -q tests/k8s_smoke
go test ./...
go vet ./...
go test -bench Streaming ./internal/proxy
go build -o bin/borg-go ./cmd/borg
go build -o bin/borg-genkey ./cmd/borg-genkey
scripts/validate-kind-go.sh --create-cluster --delete-cluster
```

Hard cutover validation run before commit:

```bash
go test ./...
go vet ./...
go build -o bin/borg-go ./cmd/borg
go build -o bin/borg-genkey ./cmd/borg-genkey
uv run pytest -q
uv run pytest -q tests/smoke
uv run pytest -q tests/k8s_smoke
docker build -t borg-go:cutover .
helm lint ./charts/borg
helm template borg ./charts/borg --debug
git diff --check
```

Cutover validation already run in the devcontainer:
- `go test ./...` passed.
- `go vet ./...` passed.
- `go build -o bin/borg-go ./cmd/borg` passed.
- `go build -o bin/borg-genkey ./cmd/borg-genkey` passed.
- `go build ./cmd/borg` passed.
- `go build ./cmd/borg-genkey` passed.
- `go test ./cmd/borg` passed with graceful shutdown coverage.
- `uv run pytest -q` passed with `61 passed`.
- `uv run pytest -q tests/smoke` passed with `14 passed` when rerun outside the sandbox. The sandboxed run hit the known localhost readiness issue.
- `uv run pytest -q tests/k8s_smoke` passed with `5 passed`.
- `helm lint ./charts/borg` passed.
- `helm template borg ./charts/borg --debug` passed.
- `git diff --check` passed.

Host/raw WSL validation note:
- A host `docker build` can hit transient `proxy.golang.org` TLS handshake timeouts during `go mod download`; retry or use `docker build --network=host -t borg-go:cutover .`.
- A host KinD run reached image loading, then `kind load docker-image` failed with `failed to detect containerd snapshotter` before Helm namespaces were created.
- The current uncommitted harness fix wraps `kind load docker-image` with a direct `docker save | docker exec <node> ctr -n k8s.io images import --all-platforms -` fallback and avoids stale port-forward logs in early-failure debug output.
- Rerun `scripts/validate-kind-go.sh --create-cluster --delete-cluster` from raw WSL/host after this fix.

The devcontainer Docker build reached `RUN go mod download`, then failed with the known nested Docker cgroup error:

```text
unable to apply cgroup configuration: mkdir /sys/fs/cgroup/cpuset/docker/...: permission denied
```

Required host/raw WSL validation before calling the current harness fix proven:

```bash
scripts/validate-kind-go.sh --create-cluster --delete-cluster
```

## Cutover Decisions
- Production image binary name is `/usr/local/bin/borg`.
- Local migration binary name remains `bin/borg-go`.
- The production image also includes `/usr/local/bin/borg-genkey`.
- The Docker image command is `/usr/local/bin/borg --host 0.0.0.0`.
- The Docker image intentionally runs as root for this cutover to preserve compatibility with low `service.targetPort` values.
- Non-root hardening is deferred until the chart has explicit `securityContext` and port/capability support.
- `/app/config.yaml` remains the default container config path and Helm mount target.
- Helm values shape remains stable; no Python/Go runtime selector is added.
- `authKeySecret.key` remains `BORG_AUTH_KEY` for Secret compatibility.
- The Deployment still maps the auth Secret key into runtime env var `AUTH_KEY`.
- Python source, tests, package metadata, and `genkey.py` are not removed in this pass.
- The Go service handles `SIGTERM` and `SIGINT` with graceful `server.Shutdown`, then falls back to `server.Close` on shutdown failure.

## Important Environment Note
Docker-in-Docker KinD inside the devcontainer is blocked in the current rootless/containerized WSL environment.

Observed failure:
- nested Docker cannot create `/sys/fs/cgroup/cpuset/docker`
- `docker info` reports cgroup v1
- direct `docker run --rm kindest/node:... true` fails with the same cgroup error

Use raw WSL/host KinD for real cluster validation. The pinned node image is:

```text
kindest/node:v1.34.3@sha256:08497ee19eace7b4b5348db5c6a1591d7752b164530a36f855cb0f2bdcbadd48
```

## Current Go Capability Summary
Core proxy:
- Python-compatible flags: `--config`, `-c`, `--host`, `--port`, and no-op `--reload`
- YAML and JSON config loading under top-level `borg`
- static backend registration
- auth-compatible POST routing
- `/` health route
- `/v1/models` model union route
- POST `/v1/*` forwarding
- round-robin endpoint selection
- non-streaming and streaming forwarding
- upstream `Authorization` rewrite
- request and response hop-by-hop header filtering
- non-streaming decoded/plain downstream compression behavior
- streaming upstream `Accept-Encoding: identity`

Kubernetes discovery:
- in-cluster config first, then kubeconfig fallback
- pod listing by configured namespace and selector
- eligible pods must be `Running`, annotated, have a pod IP, and resolve at least one model
- endpoint annotations: `borg/protocol`, `borg/apiport`, and `borg/apibase`
- model resolution from configured `modelkey` first, then automodel `/v1/models`
- automodel uses `Authorization: Bearer EMPTY`
- discovered endpoints use API key `EMPTY`
- reconciliation mutates the proxy only after successful discovery
- failed discovery preserves the previous successful snapshot

Token utility:
- reads ConfigMap defaults and Secret auth keys with `client-go`
- supports printable URL-safe auth key text and legacy raw 32-byte Secret data
- mints AES-256-GCM bearer tokens using plaintext `auth_prefix + username`

## Documentation Map
- `README.md`: user-facing status, commands, token generation, and validation
- `ROADMAP.md`: high-level migration sequencing
- `MILESTONE.md`: current cutover milestone state and validation
- `docs/migration/python-runtime-contract.md`: frozen Python runtime/config/auth contract
- `docs/migration/python-http-contract.md`: frozen Python HTTP/proxy contract
- `docs/migration/python-ops-contract.md`: frozen Python discovery/Helm/ops contract
- `docs/migration/go-project-layout.md`: Go package and runtime layout
- `docs/migration/local-smoke-test-harness.md`: Python-vs-Go static proxy smoke suite
- `docs/migration/go-k8s-smoke-test-harness.md`: fake Kubernetes API discovery smoke suite
- `docs/migration/kind-go-validation-harness.md`: real KinD Go validation harness

## Next Step
Rerun the KinD validation from raw WSL/host. If the fallback path passes, commit the harness resilience fix.

After the cutover is committed and KinD remains green, plan the cleanup milestone:
- remove or archive Python runtime code
- remove or archive `genkey.py`
- simplify README and migration docs for a normal Go-first project
- simplify the devcontainer from dual-runtime migration mode
- decide whether Python contract docs stay as history or move under an archive
