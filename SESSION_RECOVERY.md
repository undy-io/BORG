# BORG Session Recovery

Use this file to resume the Go migration if chat history is lost.

## Current Branch State
- Branch: `go-migration`
- Latest committed baseline: `e03e768 starting purge of the python code`
- Current uncommitted work: replace the Python/FastAPI `dummy-openai` validation backend with a tiny Go service.
- Unrelated local file: `.codex`
- Active runtime: Go BORG only
- Removed runtime state: `src/borg/`, legacy `genkey.py`, `entrypoint.sh`, Python runtime tests, Python-vs-Go parity smoke tests, and dedicated Python CI are removed.
- Retained Python state: `tests/k8s_smoke` remains as a validation helper for the Go runtime.
- Go validation helpers: `dummy-openai/` is a tiny Go OpenAI-compatible test backend for KinD validation.
- Deployment default status: root Docker image, Helm chart image path, and Go CI target the Go runtime.

Current dummy replacement slice:
- `dummy-openai/main.go`
- `dummy-openai/main_test.go`
- `dummy-openai/main.py`
- `dummy-openai/Dockerfile`
- `dummy-openai/README.md`
- `README.md`
- `ROADMAP.md`
- `MILESTONE.md`
- `SESSION_RECOVERY.md`
- `docs/migration/go-project-layout.md`

Do not stage `.codex`; it is unrelated local state.

## Validation State
Latest known green Go checks before this cleanup:

```bash
go test ./...
go vet ./...
```

Cutover validation previously run in the devcontainer:
- `go test ./...` passed.
- `go vet ./...` passed.
- `go build -o bin/borg-go ./cmd/borg` passed.
- `go build -o bin/borg-genkey ./cmd/borg-genkey` passed.
- `go build ./cmd/borg` passed.
- `go build ./cmd/borg-genkey` passed.
- `go test ./cmd/borg` passed with graceful shutdown coverage.
- `helm lint ./charts/borg` passed.
- `helm template borg ./charts/borg --debug` passed.
- `git diff --check` passed.

Historical Python checks passed before removal:
- `uv run pytest -q` passed with `61 passed`.
- `uv run pytest -q tests/smoke` passed with `14 passed` when rerun outside the sandbox.
- `uv run pytest -q tests/k8s_smoke` passed with `5 passed`.

Required validation for this cleanup:

```bash
go test ./...
go vet ./...
go build ./cmd/borg
go build ./cmd/borg-genkey
go build -o bin/borg-go ./cmd/borg
uv run pytest -q tests/k8s_smoke
helm lint ./charts/borg
helm template borg ./charts/borg --debug
git diff --check
```

This cleanup pass has run the full list above successfully. The retained `tests/k8s_smoke` suite requires localhost sockets; a sandboxed rerun failed with `PermissionError: [Errno 1] Operation not permitted`, then passed outside the sandbox with `5 passed`.

Required validation for the current dummy replacement slice:

```bash
go test ./...
go build -o /tmp/dummy-openai ./dummy-openai
bash -n scripts/validate-kind-go.sh
helm lint ./dummy-openai/charts/dummy-openai
helm template dummy-openai ./dummy-openai/charts/dummy-openai --debug
git diff --check
```

The local-safe validation for this slice has passed. `go build -o /tmp/dummy-openai ./dummy-openai` emitted a non-fatal read-only Go module stat-cache warning in this container, then exited successfully.

Host/raw WSL dummy replacement validation has also passed:

```bash
docker build -t dummy-openai:kind ./dummy-openai
scripts/validate-kind-go.sh --create-cluster --delete-cluster
```

That run built the scratch-based Go dummy image, created the KinD cluster, used the direct containerd import fallback for both images, deployed dummy and BORG with Helm, validated root/models/auth/forwarding/SSE, and deleted the cluster.

Host/raw WSL validation:
- A host `docker build` can hit transient `proxy.golang.org` TLS handshake timeouts during `go mod download`; retry or use `docker build --network=host -t borg-go:cutover .`.
- Docker-in-Docker KinD inside the devcontainer is blocked by non-writable cpuset cgroups.
- The committed KinD harness fix wraps `kind load docker-image` with a direct `docker save | docker exec <node> ctr -n k8s.io images import --all-platforms -` fallback.
- The full KinD create/delete path has passed from raw WSL/host after the image-load fallback and again after the Go dummy backend replacement.

Repeat host/raw WSL validation when touching Docker, Helm, discovery, or the harness:

```bash
scripts/validate-kind-go.sh --create-cluster --delete-cluster
```

## Current Go Capability Summary
Core proxy:
- Python-compatible flags retained for user continuity: `--config`, `-c`, `--host`, `--port`, and no-op `--reload`
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
- `MILESTONE.md`: current cleanup milestone state and validation
- `docs/migration/python-runtime-contract.md`: historical Python runtime/config/auth contract
- `docs/migration/python-http-contract.md`: historical Python HTTP/proxy contract
- `docs/migration/python-ops-contract.md`: historical Python discovery/Helm/ops contract
- `docs/migration/go-project-layout.md`: Go package and runtime layout
- `docs/migration/go-k8s-smoke-test-harness.md`: fake Kubernetes API discovery smoke suite
- `docs/migration/kind-go-validation-harness.md`: real KinD Go validation harness

## Next Step
Run the cleanup validation list above. After it is green, commit the Python runtime removal and CI/doc cleanup.
