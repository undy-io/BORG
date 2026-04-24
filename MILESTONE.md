# Milestone 2: Go Core Proxy And Kubernetes Discovery

## Status Snapshot
- Previous milestone: Milestone 1, "Freeze The Python Contract", complete.
- Current reference implementation remains Python.
- First Go core proxy implementation has been added beside Python.
- Review hardening completed for compression/header behavior and backend API key precedence.
- Go Kubernetes discovery has been added behind the existing static proxy path.
- Helm, Docker, CI defaults, and the Python runtime are unchanged.
- Verified:
  - `go test ./...`
  - `go vet ./...`
  - `go test -bench Streaming ./internal/proxy`
  - `go build -o bin/borg-go ./cmd/borg`
  - `uv run pytest -q tests/smoke`
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
- Local Go tests, benchmark, and build command
- Keeping Python tests green

Out of scope:
- Replacing the Python runtime
- Switching Helm or Docker defaults to Go
- Kubernetes watch/informer implementation
- Per-discovery upstream API keys
- Health-check eviction
- Token generation utility replacement
- CI/release cutover
- Removing or moving Python files

## Implemented Layout
```text
cmd/borg/
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

## Remaining Work
- Add a Kubernetes-capable local or KinD validation loop for Go discovery when deployment wiring is ready.
- Port or replace `borg-genkey`.
- Decide when to add Go container/Helm/CI wiring.
- Keep static-path smoke validation green while discovery evolves.
