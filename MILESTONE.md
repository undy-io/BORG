# Milestone 2: Go Core Proxy And Kubernetes Discovery

## Status Snapshot
- Previous milestone: Milestone 1, "Freeze The Python Contract", complete.
- Current reference implementation remains Python.
- First Go core proxy implementation has been added beside Python.
- Review hardening completed for compression/header behavior and backend API key precedence.
- Go Kubernetes discovery has been added behind the existing static proxy path.
- Fake Kubernetes API smoke validation has been added for the Go discovery path.
- Go `borg-genkey` has been added beside Python `genkey.py`.
- Host WSL KinD validation is available with a pinned Kubernetes v1.34.3 node image.
- Manual KinD deployment validation has proven the Go BORG service discovers the annotated dummy backend in a real cluster.
- Docker-in-Docker KinD inside the devcontainer is blocked in the current rootless/containerized WSL environment.
- Helm, Docker, CI defaults, and the Python runtime are unchanged.
- Verified:
  - `go test ./...`
  - `go vet ./...`
  - `go test -bench Streaming ./internal/proxy`
  - `go build -o bin/borg-go ./cmd/borg`
  - `go build -o bin/borg-genkey ./cmd/borg-genkey`
  - `uv run pytest -q tests/smoke`
  - `uv run pytest -q tests/k8s_smoke`
  - `uv run pytest -q`

## Objective
Introduce a useful Go implementation beside the Python runtime without changing the production deployment path.

The Go service now covers static config loading, auth-compatible POST routing, model registry behavior, `/v1/models`, non-streaming forwarding, streaming forwarding, and Kubernetes pod discovery. Python remains the parity oracle and rollback path.

Local side-by-side smoke validation now runs without Kubernetes.

## Scope
In scope:
- Go module setup
- Go package layout
- Python-compatible CLI flags
- Static config loading
- Auth key/token validation
- Static backend registration
- `/`, `/v1/models`, and POST `/v1/*` routing
- Non-streaming and streaming request forwarding
- Kubernetes discovery through configured `k8s_discover` selectors
- Authoritative successful discovery reconciliation
- Failed-pass discovery snapshot preservation
- Go token generation utility
- Host WSL Docker/KinD/kubectl validation setup
- Local Go tests, benchmark, and build command
- Keeping Python tests green

Out of scope:
- Replacing the Python runtime
- Switching Helm or Docker defaults to Go
- Kubernetes watch/informer implementation
- Per-discovery upstream API keys
- Health-check eviction
- CI/release cutover
- Removing or moving Python files

## Implemented Layout
```text
cmd/borg/
cmd/borg-genkey/
internal/app/
internal/auth/
internal/config/
internal/discovery/
internal/discovery/k8s/
internal/httpapi/
internal/openai/
internal/proxy/
go.mod
go.sum
```

During migration, build the service as `bin/borg-go` to avoid confusion with the Python `borg` CLI.
Build the Go token utility as `bin/borg-genkey` while Python `genkey.py` remains available.

## Checkpoints

### Checkpoint 1: Module And Tooling
Tasks:
- [x] Add `go.mod` and `go.sum`.
- [x] Add the initial Go package layout.
- [x] Add local commands:
  - `go test ./...`
  - `go build -o bin/borg-go ./cmd/borg`
- [x] Add `bin/` to `.gitignore`.

Validation:
- [x] `go test ./...` passes.
- [x] `go build -o bin/borg-go ./cmd/borg` succeeds.
- [x] `uv run pytest -q` still passes.

### Checkpoint 2: Config And CLI
Tasks:
- [x] Implement `--config` and `-c`, defaulting to `PROXY_CONFIG`, then `config.yaml`.
- [x] Implement `--host`, defaulting to `0.0.0.0`.
- [x] Implement `--port`, defaulting to `PORT`, then `8000`.
- [x] Accept `--reload` as a no-op compatibility flag.
- [x] Parse YAML and JSON config files.
- [x] Preserve the top-level `borg` config shape.
- [x] Implement auth and backend API key precedence.

Validation:
- [x] Go config tests cover path, port, YAML/JSON parsing, auth key precedence, and backend API key precedence.

### Checkpoint 3: HTTP And Proxy Parity
Tasks:
- [x] Implement `GET /`.
- [x] Implement `GET /v1/models`.
- [x] Implement POST `/v1/*`.
- [x] Implement auth enforcement only for POST proxy routes.
- [x] Implement invalid JSON, non-object JSON, missing model, and unknown model errors.
- [x] Implement model registry listing and round-robin endpoint selection.
- [x] Implement non-streaming forwarding with upstream auth rewrite.
- [x] Implement streaming forwarding with chunk flushing and downstream cancellation propagation.
- [x] Harden compression behavior:
  - non-streaming uses Go transport-managed upstream compression and decoded downstream responses
  - streaming forces upstream `Accept-Encoding: identity` for SSE latency predictability
- [x] Strip static hop-by-hop headers and headers named by `Connection` in both proxy directions.
- [x] Correct Go backend API key precedence to `apikeyEnv` value, inline `apikey`, `API_KEY`, then `EMPTY`.

Validation:
- [x] Go HTTP tests cover root, models, body validation, auth errors, valid auth, non-stream forwarding, streaming by body flag, streaming by Accept header, and downstream cancellation.
- [x] Go proxy benchmark records streaming throughput and allocations.
- [x] Go proxy tests cover compression mode, decoded non-streaming gzip responses, streaming identity, request hop-by-hop stripping, and response hop-by-hop stripping.
- [x] Go config tests cover env API keys, inline API keys, env-missing inline fallback, `API_KEY` fallback, and `EMPTY` fallback.

### Checkpoint 4: Documentation And Handoff
Tasks:
- [x] Add the Go project layout overview.
- [x] Update `README.md` with migration status and Go commands.
- [x] Update `ROADMAP.md` with current status.
- [x] Update `SESSION_RECOVERY.md` for the first Go core proxy implementation.
- [x] Document the planned local smoke/parity harness for Kubernetes-free validation.
- [x] Implement the local smoke/parity harness under `tests/smoke`.

Validation:
- [x] A future session can resume from `SESSION_RECOVERY.md` and know the next code task.
- [x] `uv run pytest -q tests/smoke` passes with real Python and Go proxy subprocesses.

### Checkpoint 5: Kubernetes Discovery
Tasks:
- [x] Add shared discovery endpoint, discoverer, registry, and reconciler types.
- [x] Preserve the last successful discovered snapshot across failed refresh passes.
- [x] Add Kubernetes discovery using `client-go`.
- [x] Load Kubernetes config from in-cluster config, then kubeconfig defaults.
- [x] List pods by configured namespace and selector.
- [x] Skip non-running pods, pods without annotations, pods without pod IPs, and pods with no resolved models.
- [x] Build endpoint URLs from pod IP plus `borg/protocol`, `borg/apiport`, and `borg/apibase` annotations.
- [x] Resolve models from configured `modelkey` annotation before automodel lookup.
- [x] Query automodel via `GET <endpoint>/v1/models` with `Authorization: Bearer EMPTY`.
- [x] Start discovery only when `update_interval > 0` and `k8s_discover` is non-empty.
- [x] Log discovery initialization failures and continue serving static config.
- [x] Run one update immediately, then poll every `update_interval` seconds.
- [x] Add `App.Close()` and close background discovery from `cmd/borg`.

Validation:
- [x] Shared reconciler tests cover initial add, authoritative removal, model-specific add/remove, and failed-pass preservation.
- [x] Kubernetes discovery tests cover pod eligibility, defaults, annotation overrides, model parsing, automodel success/failure, and list errors.
- [x] App wiring tests cover discovery gating, init failure fallback, discovered model registration, and clean shutdown.

### Checkpoint 6: Local Kubernetes Discovery Smoke
Tasks:
- [x] Add a fake Kubernetes API server smoke harness under `tests/k8s_smoke`.
- [x] Run the real `bin/borg-go` process with a temporary kubeconfig pointed at the fake API.
- [x] Start local OpenAI-compatible dummy upstreams for discovered endpoint traffic.
- [x] Cover annotation discovery and namespace/selector request shape.
- [x] Cover automodel discovery through `/v1/models`.
- [x] Cover successful reconciliation removal after pods disappear.
- [x] Cover failed Kubernetes list preservation of the last successful snapshot.
- [x] Cover endpoint annotation overrides during forwarding.

Validation:
- [x] `go build -o bin/borg-go ./cmd/borg`
- [x] `uv run pytest -q tests/k8s_smoke`

### Checkpoint 7: Go Token Utility
Tasks:
- [x] Add `cmd/borg-genkey` as a Go replacement for `genkey.py`.
- [x] Preserve Kubernetes CLI flags: username, namespace, release, key-name, auth-prefix, secret suffix, and configmap suffix.
- [x] Load local kubeconfig using default client-go loading rules.
- [x] Read ConfigMap defaults from `<release>-config` `config.yaml`.
- [x] Read auth key data from `<release>-auth`.
- [x] Preserve support for migrated printable URL-safe auth key text and legacy raw 32-byte Secret data.
- [x] Mint AES-256-GCM tokens with plaintext `auth_prefix + username`.
- [x] Keep Python `genkey.py` in place during migration.

Validation:
- [x] Go tests cover ConfigMap defaults, CLI overrides, default prefix, first Secret key fallback, missing Secret keys, Secret data formats, and token validation.
- [x] `go build -o bin/borg-genkey ./cmd/borg-genkey`

### Checkpoint 8: Devcontainer KinD Enablement
Tasks:
- [x] Add Docker-in-Docker to the devcontainer so KinD can create node containers inside the development environment.
- [x] Add kubectl and Helm tooling through the devcontainer Kubernetes feature, with Minikube disabled.
- [x] Make the app service explicitly privileged in `.devcontainer/docker-compose.yml`.
- [x] Give the app service host cgroup namespace access for nested Docker/KinD.
- [x] Mount `/sys/fs/cgroup` read-write into the app service for nested Docker cgroup creation.
- [x] Install KinD during post-create with `go install sigs.k8s.io/kind@v0.31.0`.
- [x] Rebuild/restart the devcontainer.

Validation:
- [x] `jq . .devcontainer/devcontainer.json`
- [x] `bash -n .devcontainer/post-create.sh`
- [x] `UV_CACHE_DIR=/tmp/uv-cache uv run python -c "import pathlib, yaml; yaml.safe_load(pathlib.Path('.devcontainer/docker-compose.yml').read_text()); print('ok')"`
- [x] `docker version`
- [x] `kind version`
- [x] `kubectl version --client`

Notes:
- First rebuild installed the tooling, but `kind create cluster` failed because the nested Docker daemon could not create `/sys/fs/cgroup/cpuset/docker`.
- A second rebuild with host cgroup namespace and `/sys/fs/cgroup` mounted read-write failed the same way.
- Direct `docker run --rm kindest/node:... true` also fails, so the nested Docker daemon cannot run containers in this environment.
- `docker info` reports cgroup v1, and `/sys/fs/cgroup/cpuset` is not writable by root inside the devcontainer.
- Treat in-devcontainer Docker-in-Docker KinD as blocked; use host/outside-devcontainer KinD, Docker-outside-of-Docker, or CI/VM-based KinD validation next.

### Checkpoint 9: Host WSL KinD Baseline
Tasks:
- [x] Create a KinD cluster from raw WSL instead of inside the devcontainer.
- [x] Pin the KinD node image below Kubernetes v1.35 because this WSL/Docker runtime still reports cgroup v1.
- [x] Confirm the control-plane node reaches `Ready`.
- [x] Confirm core system pods reach `Running`.
- [x] Load and deploy the dummy OpenAI backend.
- [x] Build, load, and deploy a temporary Go BORG image.
- [x] Confirm `GET /` succeeds through a port-forwarded BORG Service.
- [x] Confirm `GET /v1/models` includes the discovered dummy model.

Validation:
- [x] `kind create cluster --name borg --config kind-config.yaml --image kindest/node:v1.34.3@sha256:08497ee19eace7b4b5348db5c6a1591d7752b164530a36f855cb0f2bdcbadd48`
- [x] `kubectl wait --for=condition=Ready node/borg-control-plane --timeout=120s`
- [x] `kubectl get nodes`
- [x] `kubectl get pods -A`
- [x] `docker build -t dummy-openai:kind ./dummy-openai`
- [x] `kind load docker-image dummy-openai:kind --name borg`
- [x] `helm upgrade --install dummy-openai ./dummy-openai/charts/dummy-openai -n vllm-services --set image.repository=dummy-openai --set image.tag=kind --set image.pullPolicy=IfNotPresent`
- [x] `docker build -t borg-go:kind ...`
- [x] `kind load docker-image borg-go:kind --name borg`
- [x] `helm upgrade --install borg ./charts/borg -n borg ...`
- [x] `kubectl -n borg port-forward svc/borg-borg 18080:80`
- [x] `curl -fsS http://127.0.0.1:18080/`
- [x] `curl -fsS http://127.0.0.1:18080/v1/models`

Notes:
- `kind v0.31.0` defaults to Kubernetes v1.35.0, which fails on this cgroup v1 runtime with kubelet health errors.
- The pinned `kindest/node:v1.34.3` image successfully creates a usable local cluster.
- Temporary Go image builds may need `docker build --network=host` or a prebuilt local binary image if `go mod download` times out inside Docker.
- The manual validation currently covers BORG startup, Kubernetes discovery, Service access, and `/v1/models`; POST forwarding and streaming need an enhanced dummy backend and automated harness.

## Remaining Work
- Turn the manual KinD deployment validation into a repeatable script or pytest harness.
- Extend the KinD validation backend to cover POST forwarding and streaming.
- Switch Docker and Helm defaults to Go after KinD validation is green.
- Keep static-path smoke validation green while discovery evolves.
