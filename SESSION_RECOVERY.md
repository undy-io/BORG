# BORG Session Recovery

Use this file to resume the Go migration if chat history is lost.

## Last Recovered Branch State
- Branch: `go-migration`
- Last recovered commit: `7d98f84 M1`
- At recovery time, the branch was one migration commit ahead of `master`
- Go implementation status: first core proxy implementation added beside Python
- Latest Go review hardening status: compression/header behavior and backend API key precedence fixed
- Local smoke/parity harness status: implemented in `tests/smoke/test_local_parity.py`
- Python implementation status: still the reference runtime and parity oracle
- Latest verified baseline:
  - `uv run pytest -q`
  - `56 passed in 38.99s`
  - `uv run pytest -q tests/smoke`
  - `14 passed in 22.76s`
  - `go test ./...`
  - `go vet ./...`
  - `go test -bench Streaming ./internal/proxy`
  - `go build -o bin/borg-go ./cmd/borg`

Local uncommitted state observed during recovery:
- `.devcontainer/Dockerfile` has a local edit adding `bubblewrap` to apt packages.
- `.codex` exists as an empty untracked file.

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
- Do not remove Python runtime, CI, container, or Helm paths before final cutover.

## Milestone Status
Milestone 1, "Freeze The Python Contract", is complete.

Milestone 2, "Go Core Proxy First Pass", is now the active milestone in `MILESTONE.md`.

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
- `internal/app`
- `internal/auth`
- `internal/config`
- `internal/httpapi`
- `internal/openai`
- `internal/proxy`
- `tests/smoke/test_local_parity.py`

Milestone 2 review hardening completed:
- Go non-streaming forwarding strips client `Accept-Encoding` and relies on Go transport-managed gzip/decode.
- Go streaming forwarding sends upstream `Accept-Encoding: identity` to protect SSE latency.
- Go request and response header filtering strips static hop-by-hop headers and headers named by `Connection`.
- Go backend API key precedence is `apikeyEnv` value, inline `apikey`, `API_KEY`, then `EMPTY`.

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
- `internal/app`
- `internal/auth`
- `internal/config`
- `internal/httpapi`
- `internal/openai`
- `internal/proxy`

Later packages:
- `cmd/borg-genkey`
- `internal/discovery`
- `internal/discovery/k8s`

During migration, build the service as `bin/borg-go` so it can run beside the Python `borg` CLI.

## Next Step
Decide the next implementation lane now that the Kubernetes-free local smoke/parity harness is green.

Recommended next tasks:
- Review whether Kubernetes discovery or proxy observability/performance hardening should be next.
- Keep `go build -o bin/borg-go ./cmd/borg && uv run pytest -q tests/smoke` as the local static-path validation loop.
- Do not change Helm defaults to Go yet.

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
uv run pytest -q tests/smoke
```
