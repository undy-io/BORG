# BORG Session Recovery

Use this file to resume the Go migration if chat history is lost.

## Current Branch State
- Branch: `go-migration`
- Last committed baseline: `fc39536 Add Go borg-genkey utility`
- Current uncommitted migration work: repeatable KinD Go validation harness, enhanced dummy backend, and documentation updates
- Go implementation status: static proxy path, Kubernetes discovery, fake Kubernetes discovery smoke validation, and token generation utility are implemented beside Python
- Latest Go review hardening status: compression/header behavior, backend API key precedence, Kubernetes discovery lifecycle, and discovered endpoint URL construction
- Local smoke/parity harness status: implemented in `tests/smoke/test_local_parity.py`
- Go Kubernetes smoke harness status: implemented in `tests/k8s_smoke/test_go_k8s_discovery.py`
- Go Kubernetes discovery status: implemented with `client-go` behind the existing static proxy path
- Go token utility status: implemented in `cmd/borg-genkey`
- KinD status: Docker-in-Docker inside the devcontainer is blocked by non-writable cpuset cgroups, raw WSL KinD works with the Kubernetes v1.34.3 node image pinned, and `scripts/validate-kind-go.sh --create-cluster --delete-cluster` passed
- Python implementation status: still the reference runtime and parity oracle
- Latest verified baseline:
  - `uv run pytest -q`
  - `56 passed`
  - `uv run pytest -q tests/smoke`
  - `14 passed`
  - `uv run pytest -q tests/k8s_smoke`
  - `5 passed`
  - `go test ./...`
  - `go vet ./...`
  - `go test -bench Streaming ./internal/proxy`
  - `go build -o bin/borg-go ./cmd/borg`
  - `go build -o bin/borg-genkey ./cmd/borg-genkey`
- Latest pre-rebuild devcontainer checks:
  - `jq . .devcontainer/devcontainer.json`
  - `bash -n .devcontainer/post-create.sh`
  - `UV_CACHE_DIR=/tmp/uv-cache uv run python -c "import pathlib, yaml; yaml.safe_load(pathlib.Path('.devcontainer/docker-compose.yml').read_text()); print('ok')"`
  - `git diff --check -- .devcontainer/devcontainer.json .devcontainer/docker-compose.yml .devcontainer/post-create.sh SESSION_RECOVERY.md MILESTONE.md ROADMAP.md README.md docs/migration/go-project-layout.md`
- Latest host WSL KinD checks:
  - `kind create cluster --name borg --config kind-config.yaml --image kindest/node:v1.34.3@sha256:08497ee19eace7b4b5348db5c6a1591d7752b164530a36f855cb0f2bdcbadd48`
  - `kubectl wait --for=condition=Ready node/borg-control-plane --timeout=120s`
  - `kubectl get nodes`
  - `kubectl get pods -A`
- Latest manual KinD deployment checks:
  - `docker build -t dummy-openai:kind ./dummy-openai`
  - `kind load docker-image dummy-openai:kind --name borg`
  - `helm upgrade --install dummy-openai ./dummy-openai/charts/dummy-openai -n vllm-services --set image.repository=dummy-openai --set image.tag=kind --set image.pullPolicy=IfNotPresent`
  - temporary Go image built as `borg-go:kind`; retry with `docker build --network=host` or a prebuilt binary image if `go mod download` times out
  - `kind load docker-image borg-go:kind --name borg`
  - `helm upgrade --install borg ./charts/borg -n borg ...` with image repository `borg-go`, tag `kind`, ingress disabled, update interval `2`, and discovery pointed at `vllm-services` selector `borg/expose=vllm`
  - `kubectl -n borg port-forward svc/borg-borg 18080:80`
  - `curl -fsS http://127.0.0.1:18080/` returned `{"detail":"Proxy router is running","status":"ok"}`
  - `curl -fsS http://127.0.0.1:18080/v1/models` returned a model list containing `gpt-3.5-turbo`
- Latest repeatable KinD harness local checks:
  - `bash -n scripts/validate-kind-go.sh`
  - `scripts/validate-kind-go.sh --help`
  - `UV_CACHE_DIR=/tmp/uv-cache uv run ruff check dummy-openai/main.py`
  - `go test ./...`
  - `uv run pytest -q tests/smoke`
  - `uv run pytest -q tests/k8s_smoke`
  - The smoke pytest commands timed out inside the Codex sandbox after the devcontainer rebuild, then passed when rerun outside the sandbox.
- Latest repeatable KinD harness host WSL check:
  - `scripts/validate-kind-go.sh --create-cluster --delete-cluster`
  - Passed cluster create, image build/load, Helm deploy, root/models checks, missing-auth rejection, authenticated POST forwarding with upstream auth rewrite to `Bearer EMPTY`, SSE streaming, and cluster delete.

Local uncommitted state:
- `.devcontainer/devcontainer.json` adds Docker-in-Docker and kubectl/Helm devcontainer features.
- `.devcontainer/docker-compose.yml` makes the `app` service explicitly `privileged`, uses host cgroup namespace, and mounts `/sys/fs/cgroup` read-write for nested Docker/KinD; this was not sufficient in the current host environment.
- `.devcontainer/post-create.sh` installs `kind` with `go install sigs.k8s.io/kind@v0.31.0`.
- `scripts/validate-kind-go.sh` adds a host/raw WSL KinD validation harness for the Go runtime.
- `dummy-openai/main.py` now supports POST `/v1/chat/completions` and deterministic SSE streaming for validation.
- `.codex` exists as an untracked local file and is unrelated to the migration changes.

## Project Goal
Migrate BORG from Python to Go using a side-by-side approach. Python remains the reference implementation until the Go service reaches HTTP, auth, discovery, config, Helm, and operational parity.

## Decisions Already Made
- Do not perform an in-place rewrite.
- Work milestone by milestone.
- Keep high-level migration sequencing in `ROADMAP.md`.
- Keep the active milestone in repo-root `MILESTONE.md`.
- Keep Python working throughout the migration.
- Add Go beside Python, then validate side by side before cutover.
- Prefer external behavior parity over early optimization.
- After KinD validation is green, a hard cutover to Go is acceptable because BORG is not deployed anywhere yet.

## Milestone Status
Milestone 1, "Freeze The Python Contract", is complete.

Milestone 2, "Go Core Proxy And Kubernetes Discovery", is now the active milestone in `MILESTONE.md`.

Milestone 1 produced the frozen Python contract docs:
- `docs/migration/python-runtime-contract.md`
- `docs/migration/python-http-contract.md`
- `docs/migration/python-ops-contract.md`

Milestone 2 documentation produced:
- `docs/migration/go-project-layout.md`
- `docs/migration/local-smoke-test-harness.md`

Milestone 2 code produced:
- `go.mod` and `go.sum`
- `cmd/borg`
- `cmd/borg-genkey`
- `internal/app`
- `internal/auth`
- `internal/config`
- `internal/discovery`
- `internal/discovery/k8s`
- `internal/httpapi`
- `internal/openai`
- `internal/proxy`
- `tests/smoke/test_local_parity.py`
- `tests/k8s_smoke/test_go_k8s_discovery.py`
- `scripts/validate-kind-go.sh`

Milestone 2 review hardening completed:
- Go non-streaming forwarding strips client `Accept-Encoding` and relies on Go transport-managed gzip/decode.
- Go streaming forwarding sends upstream `Accept-Encoding: identity` to protect SSE latency.
- Go request and response header filtering strips static hop-by-hop headers and headers named by `Connection`.
- Go backend API key precedence is `apikeyEnv` value, inline `apikey`, `API_KEY`, then `EMPTY`.

Milestone 2 Kubernetes discovery completed:
- `internal/discovery` defines shared endpoint, discoverer, registry, and reconciler types.
- The reconciler mutates the proxy only after a successful discovery pass.
- Failed discovery passes return an error to the caller and preserve the previous successful discovered snapshot.
- `internal/discovery/k8s` uses official Kubernetes Go client packages.
- Kubernetes config loading tries in-cluster config first, then kubeconfig defaults honoring `KUBECONFIG` and `~/.kube/config`.
- Pod discovery lists configured namespaces and selectors.
- Eligible pods must be `Running`, have annotations, have a pod IP, and resolve at least one model.
- Endpoint URL construction uses `borg/protocol` default `http`, `borg/apiport` default `8000`, and `borg/apibase` default empty.
- Endpoint URL construction uses `net.JoinHostPort`, so IPv4 and IPv6 pod IPs are encoded correctly.
- Model resolution uses configured `modelkey` comma lists first, then automodel `GET <endpoint>/v1/models` with `Authorization: Bearer EMPTY`.
- Discovered endpoints use API key `EMPTY`.
- `internal/app` starts discovery only when `update_interval > 0` and `k8s_discover` is non-empty.
- Discovery initialization failures are logged and static config continues to serve.
- Discovery runs one immediate reconciliation, then repeats every `update_interval` seconds.
- `App.Close()` cancels and waits for background discovery; `cmd/borg` defers it.

Milestone 2 fake Kubernetes smoke validation completed:
- `tests/k8s_smoke/test_go_k8s_discovery.py` runs the real `bin/borg-go` subprocess.
- The suite writes a temporary kubeconfig pointing `client-go` at a localhost fake Kubernetes API.
- The fake API implements pod list responses for configured namespaces and selectors.
- Local dummy OpenAI-compatible upstreams make discovered endpoints callable from the Go process.
- Coverage includes annotation discovery, automodel discovery, authoritative removal, failed-list snapshot preservation, selector request shape, and endpoint annotation overrides.

Milestone 2 Go token utility completed:
- `cmd/borg-genkey` ports the Kubernetes-oriented `genkey.py` workflow.
- The Go utility accepts username plus `--namespace/-n`, `--release/-r`, `--key-name/-k`, `--auth-prefix`, `--secret-suffix`, and `--configmap-suffix`.
- The utility loads local kubeconfig through `client-go` default loading rules.
- ConfigMap defaults come from `<release>-config` `config.yaml` fields `auth_key_from_env` and `auth_prefix`.
- Auth keys are read from `<release>-auth` Secret data.
- Secret data supports migrated printable URL-safe auth key text and legacy raw 32-byte AES keys.
- Tokens use AES-256-GCM with plaintext `auth_prefix + username` and base64url `nonce || ciphertext+tag`.

Milestone 2 devcontainer KinD enablement in progress:
- `.devcontainer/devcontainer.json` uses the official Docker-in-Docker feature so KinD can run node containers inside the devcontainer.
- `.devcontainer/devcontainer.json` uses the official kubectl/Helm/Minikube feature with Minikube disabled.
- `.devcontainer/docker-compose.yml` explicitly sets `app.privileged: true`.
- `.devcontainer/docker-compose.yml` sets `app.cgroup: host` and mounts `/sys/fs/cgroup:/sys/fs/cgroup:rw`.
- `.devcontainer/post-create.sh` installs KinD as a Go tool at `sigs.k8s.io/kind@v0.31.0`.
- Rebuilds installed Docker/KinD/kubectl, but `kind create cluster` failed when the nested Docker daemon could not create `/sys/fs/cgroup/cpuset/docker`.
- Direct `docker run --rm kindest/node:... true` also fails with the same cgroup error, so the nested Docker daemon cannot run containers at all.
- `docker info` from the rebuilt devcontainer reports Docker Engine 29.4.1, `Cgroup Driver: cgroupfs`, and `Cgroup Version: 1`.
- Real cgroup inspection shows `/sys/fs/cgroup/cpuset` mounted read/write but the directory itself is `dr-xr-xr-x` and owned by `nobody:nogroup`, so root inside the container cannot create `/sys/fs/cgroup/cpuset/docker`.
- Treat Docker-in-Docker KinD as not viable in this environment unless the host/devcontainer runtime changes to one with writable cgroups or cgroup v2 support.
- Host/raw WSL KinD works when the node image is pinned to Kubernetes v1.34.3:
  - `kindest/node:v1.34.3@sha256:08497ee19eace7b4b5348db5c6a1591d7752b164530a36f855cb0f2bdcbadd48`
- `kind v0.31.0` defaulted to Kubernetes v1.35.0, which failed on the WSL cgroup v1 runtime with kubelet health errors.
- `kubectl wait --for=condition=Ready node/borg-control-plane --timeout=120s` passed on the pinned v1.34.3 cluster.
- Core pods in `kube-system` and `local-path-storage` were all `Running`.
- Manual cluster deployment validation is green for Go BORG startup, Kubernetes discovery, Service access, and `/v1/models`.
- `scripts/validate-kind-go.sh` automates Go BORG KinD validation from raw WSL/host.
- `dummy-openai/main.py` now implements `POST /v1/chat/completions` and deterministic SSE chunks, so the harness can cover POST forwarding and streaming.
- Official feature metadata was checked: Docker-in-Docker supports `moby` and `dockerDashComposeVersion`, declares privileged mode, and kubectl/Helm/Minikube supports `version`, `helm`, and `minikube`.

Milestone 1 also added or strengthened characterization coverage around:
- invalid or non-object JSON request bodies
- missing model request bodies
- auth error details and non-bearer auth
- default auth prefix normalization to `PROXY:`
- app factory isolation
- discovery authoritative reconciliation
- discovery failure snapshot preservation
- `genkey.py` support for both printable auth key secrets and legacy raw key secrets

## Python Contract Summary
Runtime and configuration:
- CLI entrypoint is `borg`, backed by `borg.__main__:run`.
- `--config` defaults to `PROXY_CONFIG`, then `config.yaml`.
- `--port` defaults to `PORT`, then `8000`.
- Config files are YAML unless the filename ends in `.json`.
- Top-level runtime config is `borg`.
- Auth key precedence is `AUTH_KEY`, then `BORG_AUTH_KEY`, then `borg.auth_key`, then no auth via `EMPTY`.
- Backend API key resolution supports per-instance `apikeyEnv`, instance `apikey`, and fallback `API_KEY`.

HTTP behavior:
- Exposed routes are `GET /`, `GET /v1/models`, and `POST /v1/{remainder:path}`.
- Auth is enforced only on POST proxy routes, not on `/` or `/v1/models`.
- Request bodies must be valid JSON objects with a truthy `model`.
- Unknown models fail locally with `404`.
- Streaming is selected by request body `stream: true` or `Accept: text/event-stream`.
- Upstream `Authorization` is always rewritten to `Bearer <backend-api-key>`.
- `/v1/models` returns the sorted union of non-empty model buckets.

Auth and token compatibility:
- Token format is base64url of `nonce || ciphertext+tag`.
- Nonce length is 12 bytes.
- Cipher is AES-256-GCM.
- Plaintext is `auth_prefix + username`.
- Intended default auth prefix is `PROXY:`.
- The earlier `Proxy:` default is treated as a bug, not Go parity.

Discovery and ops:
- Background discovery runs only when `update_interval > 0` and discovery services initialize.
- Kubernetes config load order is in-cluster config, then local kubeconfig.
- Discovery is selector-driven and annotation-governed.
- Only `Running` pods with resolved models are eligible.
- Endpoint defaults are protocol `http`, port `8000`, and empty base path.
- Models come from the configured annotation key or automodel lookup via `/v1/models`.
- Successful discovery passes are authoritative and may evict missing endpoints.
- Failed discovery passes preserve the last successful snapshot.
- Helm runtime wiring uses `PORT`, `PROXY_CONFIG=/app/config.yaml`, `AUTH_KEY`, mounted config, and per-instance `apikeyEnv` secrets.

## Code Changes Already Made
- Devcontainer was expanded for dual Python and Go development:
  - Go devcontainer feature pinned to `1.26.2`
  - VS Code Go extension added
  - post-create installs `gopls`, `goimports`, and `dlv`
- Devcontainer KinD enablement is staged:
  - Docker-in-Docker feature added with Docker CE and Docker Compose v2
  - kubectl/Helm feature added with Minikube disabled
  - `app` service marked privileged
  - `app` service uses host cgroup namespace and a read-write `/sys/fs/cgroup` mount, but this does not make nested Docker usable on the current host
  - post-create installs `kind`
- Host/raw WSL KinD harness added:
  - `scripts/validate-kind-go.sh` assumes an existing `kind-borg` cluster by default.
  - `--create-cluster` creates the cluster when missing with the pinned v1.34.3 node image.
  - `--delete-cluster` is only allowed with `--create-cluster` and deletes only a cluster created by the script.
  - `--cleanup-resources` removes test Helm releases and namespaces on exit.
  - The harness builds host Go binaries into ignored `build/kind/`, packages `borg-go:kind`, loads images into KinD, deploys via Helm, mints a token with Go `borg-genkey`, and validates root, models, auth, POST, and streaming.
- Python auth/runtime updates:
  - `AUTH_KEY` added as preferred auth env var
  - `BORG_AUTH_KEY` retained as legacy fallback
  - default auth prefix normalized to `PROXY:`
- Python proxy updates:
  - rejects non-object JSON bodies with `400 Body must be valid JSON`
- Python discovery updates:
  - Kubernetes API errors are logged and surfaced during `_discover`
  - failed update passes preserve the previous endpoint snapshot
  - successful update passes reconcile add/remove changes authoritatively
- `genkey.py` now accepts both legacy raw 32-byte Secret data and printable base64/base64url auth key text.
- Helm updates:
  - deployment now injects auth through `AUTH_KEY`
  - auth Secret template preserves existing values and migrates legacy raw-byte secrets to text-safe form
  - API-key Secret uses `stringData`
  - `authKeySecret.value` was added to values schema

## Known Drift And Normalization Decisions
- `config.example.yaml` previously contained `auth_preifx`; this typo was normalized to `auth_prefix` before the Go skeleton.
- Helm writes `auth_key_from_env` into config, but Python runtime ignores it. Treat this as chart/tooling drift unless intentionally promoted into a runtime feature.
- Discovery-level per-endpoint API keys do not exist today; automodel lookup uses `Bearer EMPTY`.
- Endpoint health-check eviction is not implemented in Python and should not be inferred as current behavior.
- `/v1/models` remains unauthenticated even when POST proxying requires auth.

## Go Layout
The side-by-side Go layout lives in `docs/migration/go-project-layout.md`.

Implemented packages:
- `go.mod`
- `cmd/borg/main.go`
- `cmd/borg-genkey/main.go`
- `internal/app`
- `internal/auth`
- `internal/config`
- `internal/discovery`
- `internal/discovery/k8s`
- `internal/httpapi`
- `internal/openai`
- `internal/proxy`

During migration, build the service as `bin/borg-go` so it can run beside the Python `borg` CLI.
Build the token utility as `bin/borg-genkey` while Python `genkey.py` remains available.

## Next Step
Use the green repeatable KinD harness as the gate for the hard Go cutover. The next implementation lane is switching Docker and Helm defaults to Go while keeping static and KinD validation green.

Recommended next tasks:
- Use this host WSL cluster setup command:
  - `kind create cluster --name borg --config kind-config.yaml --image kindest/node:v1.34.3@sha256:08497ee19eace7b4b5348db5c6a1591d7752b164530a36f855cb0f2bdcbadd48`
- Verify it with:
  - `kubectl wait --for=condition=Ready node/borg-control-plane --timeout=120s`
  - `kubectl get nodes`
  - `kubectl get pods -A`
- Commit the devcontainer and KinD harness/documentation changes once reviewed.
- After KinD validation is green, switch Docker/Helm defaults to Go.
- Keep `go build -o bin/borg-go ./cmd/borg && uv run pytest -q tests/smoke` as the local static-path validation loop.
- Keep `go build -o bin/borg-go ./cmd/borg && uv run pytest -q tests/k8s_smoke` as the local discovery validation loop.
- Keep `go build -o bin/borg-genkey ./cmd/borg-genkey && go test ./cmd/borg-genkey ./internal/auth` as the token utility validation loop.

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
scripts/validate-kind-go.sh
scripts/validate-kind-go.sh --create-cluster --delete-cluster
docker version
kind version
kubectl version --client
kind create cluster --name borg --config kind-config.yaml --image kindest/node:v1.34.3@sha256:08497ee19eace7b4b5348db5c6a1591d7752b164530a36f855cb0f2bdcbadd48
kubectl wait --for=condition=Ready node/borg-control-plane --timeout=120s
kubectl get nodes
kubectl get pods -A
kind delete cluster --name borg
```
