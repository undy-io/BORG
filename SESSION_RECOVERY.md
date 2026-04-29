# BORG Session Recovery

Use this file to resume the Go migration if chat history is lost.

## Current Branch State
- Branch: `go-migration`
- Latest committed baseline before this documentation pass: `10e5cb4 Kind validation script works`
- Expected current uncommitted work: documentation refresh for the completed KinD Go validation milestone
- Unrelated local file: `.codex`
- Python runtime status: still the reference runtime, deployment fallback, and parity oracle
- Go runtime status: implemented beside Python for static proxying, auth, streaming, Kubernetes discovery, and token generation
- Deployment default status: Helm, Docker, CI, and release defaults are still Python-first until the cutover pass

Current uncommitted documentation slice:
- `README.md`
- `MILESTONE.md`
- `ROADMAP.md`
- `SESSION_RECOVERY.md`
- `docs/migration/go-project-layout.md`
- `docs/migration/go-k8s-smoke-test-harness.md`
- `docs/migration/kind-go-validation-harness.md`
- `dummy-openai/README.md`

If resuming before this documentation pass is committed, stage the docs with:

```bash
git add README.md MILESTONE.md ROADMAP.md SESSION_RECOVERY.md \
  docs/migration/go-project-layout.md \
  docs/migration/go-k8s-smoke-test-harness.md \
  docs/migration/kind-go-validation-harness.md \
  dummy-openai/README.md
```

Do not stage `.codex`; it is unrelated local state.

## Current Validation State
Latest known green checks:

```bash
uv run pytest -q
uv run pytest -q tests/smoke
uv run pytest -q tests/k8s_smoke
go test ./...
go vet ./...
go test -bench Streaming ./internal/proxy
go build -o bin/borg-go ./cmd/borg
go build -o bin/borg-genkey ./cmd/borg-genkey
```

Latest documentation-pass checks:

```bash
git diff --check
bash -n scripts/validate-kind-go.sh
```

The repeatable host/raw WSL KinD validation also passed:

```bash
scripts/validate-kind-go.sh --create-cluster --delete-cluster
```

That run completed:
- KinD cluster creation with the pinned Kubernetes v1.34.3 node image
- Go binary build
- `borg-go:kind` image build from the local binary
- `dummy-openai:kind` image build
- image load into KinD
- dummy backend Helm deploy
- Go BORG Helm deploy
- root route validation
- discovered `/v1/models` validation
- missing-auth `401` validation
- authenticated POST forwarding validation
- upstream auth rewrite validation to `Bearer EMPTY`
- SSE streaming validation
- KinD cluster deletion

## Important Environment Note
Docker-in-Docker KinD inside the devcontainer is blocked in the current rootless/containerized WSL environment.

Observed failure:
- nested Docker cannot create `/sys/fs/cgroup/cpuset/docker`
- `docker info` reports cgroup v1
- `/sys/fs/cgroup/cpuset` is not writable in a way nested Docker can use
- direct `docker run --rm kindest/node:... true` fails with the same cgroup error

Use raw WSL/host KinD for real cluster validation. The pinned node image is:

```text
kindest/node:v1.34.3@sha256:08497ee19eace7b4b5348db5c6a1591d7752b164530a36f855cb0f2bdcbadd48
```

## Migration Decisions
- Keep Python and Go side by side until cutover.
- Preserve Python behavior unless an accepted Go delta is documented.
- Use URL-safe printable auth keys as the normalized auth-key representation.
- Keep request bodies buffered in memory for current Python parity.
- Use standard `net/http` and a custom Go proxy, not `httputil.ReverseProxy`.
- Use `client-go` for Kubernetes discovery.
- Use polling discovery for this phase; watches/informers remain out of scope.
- Treat failed discovery passes as non-authoritative and preserve the last successful discovered snapshot.
- After green KinD validation, a hard cutover to Go is acceptable because BORG is not deployed anywhere yet.

## Implemented Go Layout
Primary Go service:
- `cmd/borg`
- `internal/app`
- `internal/auth`
- `internal/config`
- `internal/discovery`
- `internal/discovery/k8s`
- `internal/httpapi`
- `internal/openai`
- `internal/proxy`

Go token utility:
- `cmd/borg-genkey`

Validation and helper paths:
- `tests/smoke/test_local_parity.py`
- `tests/k8s_smoke/test_go_k8s_discovery.py`
- `scripts/validate-kind-go.sh`
- `dummy-openai/`

During migration:
- build the service as `bin/borg-go`
- build the token utility as `bin/borg-genkey`
- keep Python `genkey.py` available

## Documentation Map
- `README.md`: user-facing status, commands, and migration document index
- `ROADMAP.md`: high-level migration sequencing
- `MILESTONE.md`: current milestone state and remaining work
- `docs/migration/python-runtime-contract.md`: frozen Python runtime/config/auth contract
- `docs/migration/python-http-contract.md`: frozen Python HTTP/proxy contract
- `docs/migration/python-ops-contract.md`: frozen Python discovery/Helm/ops contract
- `docs/migration/go-project-layout.md`: side-by-side Go package layout
- `docs/migration/local-smoke-test-harness.md`: Python-vs-Go static proxy smoke suite
- `docs/migration/go-k8s-smoke-test-harness.md`: fake Kubernetes API discovery smoke suite
- `docs/migration/kind-go-validation-harness.md`: real KinD Go validation harness

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

## Known Accepted Deltas
- Go auth keys are normalized to URL-safe printable base64.
- Go non-streaming forwarding does not forward client `Accept-Encoding`; Go may negotiate upstream gzip and return decoded downstream bytes.
- Go streaming forwarding forces upstream `Accept-Encoding: identity` to protect SSE latency.
- Go backend API key precedence is `apikeyEnv` value, inline `apikey`, `API_KEY`, then `EMPTY`.

## Next Step
The next implementation lane is the hard Go cutover.

Concrete planning should cover:
- switching Dockerfile/default image build to Go
- switching Helm defaults to run Go BORG
- preserving or documenting rollback to Python during transition
- adding CI/release validation for the Go runtime
- deciding whether `borg-genkey` replaces `genkey.py` in docs and packaging
- keeping these gates green:
  - `go test ./...`
  - `uv run pytest -q`
  - `uv run pytest -q tests/smoke`
  - `uv run pytest -q tests/k8s_smoke`
  - `scripts/validate-kind-go.sh --create-cluster --delete-cluster`

## Useful Commands
```bash
uv run pytest -q
uv run ruff check .
uv run ruff format --check .
go version
go test ./...
go vet ./...
go test -bench Streaming ./internal/proxy
go build -o bin/borg-go ./cmd/borg
go build -o bin/borg-genkey ./cmd/borg-genkey
uv run pytest -q tests/smoke
uv run pytest -q tests/k8s_smoke
bash -n scripts/validate-kind-go.sh
scripts/validate-kind-go.sh
scripts/validate-kind-go.sh --create-cluster --delete-cluster
docker version
kind version
kubectl version --client
helm version
```
