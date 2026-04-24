# Milestone 2: Go Core Proxy First Pass

## Status Snapshot
- Previous milestone: Milestone 1, "Freeze The Python Contract", complete.
- Current reference implementation remains Python.
- First Go core proxy implementation has been added beside Python.
- Review hardening completed for compression/header behavior and backend API key precedence.
- Verified:
  - `go test ./...`
  - `go vet ./...`
  - `go test -bench Streaming ./internal/proxy`
  - `go build -o bin/borg-go ./cmd/borg`
  - `uv run pytest -q tests/smoke`
  - `uv run pytest -q`

## Objective
Introduce a useful Go implementation beside the Python runtime without changing the production deployment path.

The Go service now covers static config loading, auth-compatible POST routing, model registry behavior, `/v1/models`, non-streaming forwarding, and streaming forwarding. Python remains the parity oracle and rollback path.

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
- Local Go tests, benchmark, and build command
- Keeping Python tests green

Out of scope:
- Replacing the Python runtime
- Switching Helm or Docker defaults to Go
- Kubernetes discovery implementation
- Token generation utility replacement
- CI/release cutover
- Removing or moving Python files

## Implemented Layout
```text
cmd/borg/
internal/app/
internal/auth/
internal/config/
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

## Remaining Work
- Decide whether Kubernetes discovery is next or whether to harden proxy performance and observability first.
