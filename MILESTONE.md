# Milestone 1: Freeze The Python Contract

## Status Snapshot
- Date recovered: 2026-04-21
- Devcontainer state verified after rebuild:
  - `go version` -> `go1.26.2 linux/amd64`
  - `gopls` -> `/go/bin/gopls`
  - `goimports` -> `/usr/local/bin/goimports`
  - `dlv` -> `/go/bin/dlv`
- Python baseline verified:
  - `uv run pytest -q`
  - `35 passed in 13.71s`
- Current reference implementation remains Python.
- There was no existing repo-root `MILESTONE.md` before this file.

## Objective
Capture the current Python behavior precisely enough that we can build the first Go version for parity instead of guessing from code, docs, or Helm templates independently.

Execution approach:
- Complete Milestone 1 in a single focused pass.
- Use three checkpoints to keep coverage explicit and avoid missing contract details.

## Scope
In scope:
- HTTP contract
- Auth and token compatibility
- Config, env, and CLI contract
- Discovery behavior
- Deployment and Helm-facing runtime inputs
- Known quirks, drift, and ambiguities that must be preserved or explicitly deferred

Out of scope:
- Writing the Go service
- Changing Python behavior unless required to make the baseline observable
- Removing Python runtime, tests, container path, or Helm path

## Reference Inputs
- `src/borg/main.py`
- `src/borg/proxy.py`
- `src/borg/k8s_discovery.py`
- `genkey.py`
- `tests/test_proxy_router.py`
- `tests/test_proxy_service_instances.py`
- `tests/test_discovery.py`
- `tests/test_genkey.py`
- `charts/borg/values.yaml`
- `charts/borg/templates/config.yaml`
- `charts/borg/templates/deployment.yaml`
- `charts/borg/templates/secret.yaml`

## Recovered Baseline

### Runtime shape
- The service exposes:
  - `GET /`
  - `GET /v1/models`
  - `POST /v1/{remainder:path}`
- The runtime is built through `create_app(config_path)` and keeps app-local proxy/discovery state.
- `PROXY_CONFIG` defaults to `config.yaml`.
- `PORT` defaults to `8000`.

### Proxy behavior
- The request body must be valid JSON and include `model`.
- Unknown models return `404`.
- Streaming is selected when either:
  - request JSON contains `"stream": true`
  - `Accept` contains `text/event-stream`
- Requests are forwarded to the next endpoint for the requested model in round-robin order.
- The upstream `Authorization` header is always replaced with the backend API key.
- Query params and request body are forwarded upstream.
- Hop-by-hop headers are stripped on proxying.
- `GET /v1/models` returns the sorted union of non-empty model buckets.

### Auth behavior
- If no auth key is configured, requests are treated as anonymous.
- If auth is enabled, the proxy expects `Authorization: Bearer <token>`.
- Tokens are AES-256-GCM over `auth_prefix + username`.
- Token wire format is base64url of `nonce || ciphertext+tag`.
- Nonce length is 12 bytes.
- `AUTH_KEY` overrides `BORG_AUTH_KEY`, which overrides config `auth_key`.

### Discovery behavior
- Background discovery runs only when `update_interval > 0` and discovery services are configured.
- Kubernetes config load order is:
  - in-cluster config
  - kubeconfig fallback
- Only `Running` pods are considered.
- Endpoint construction defaults:
  - protocol: `http`
  - port: `8000`
  - base path: empty string
- Models come from the configured annotation key, or from upstream `/v1/models` enumeration when automodel is used.
- Discovery updates add and remove endpoints from proxy model buckets.

### Deployment/runtime inputs
- Helm deploys the app with:
  - `PORT`
  - `PROXY_CONFIG=/app/config.yaml`
  - `AUTH_KEY` from a Secret
  - per-instance API key env vars from a separate Secret when `apikeyEnv` is set
- Readiness and liveness probes hit `/`.
- The ConfigMap includes `auth_prefix`, `update_interval`, `instances`, and `k8s_discover`.

## Checkpoints

### Checkpoint 1: Runtime Contract
Deliverable:
- A short contract document describing CLI flags, env vars, config shape, precedence rules, and token/key compatibility requirements.
- Working doc: `docs/migration/python-runtime-contract.md`

Tasks:
- [x] Capture CLI flags and defaults from `src/borg/main.py`.
- [x] Capture env/config precedence for `PROXY_CONFIG`, `PORT`, `AUTH_KEY`, `BORG_AUTH_KEY`, `API_KEY`, and per-instance `apikeyEnv`.
- [x] Record the token payload format, nonce size, encryption mode, and encoding.
- [x] Confirm secret formats accepted by `genkey.py`.
- [x] Confirm whether any currently documented config keys are stale, unused, or tooling-only.

Validation:
- [x] Every rule is traceable to code or tests.
- [ ] Go can later verify Python-issued tokens and Python can verify Go-issued tokens.

### Checkpoint 2: HTTP Contract
Deliverable:
- A parity checklist for all externally visible HTTP behavior.
- Working doc: `docs/migration/python-http-contract.md`

Tasks:
- [x] Document exact route surface and supported methods.
- [x] Document request validation rules, including missing/invalid `model`.
- [x] Document streaming selection rules.
- [x] Document response passthrough behavior for standard and streaming calls.
- [x] Document `/v1/models` response shape and ordering behavior.
- [x] Document auth error behavior, upstream API-key rewriting, and unknown-model error expectations.

Validation:
- [x] Each behavior is backed by an existing test or called out as needing a characterization test.

### Checkpoint 3: Ops Contract
Deliverable:
- A migration note covering discovery behavior, refresh semantics, and Helm/deployment-facing runtime inputs.
- Working doc: `docs/migration/python-ops-contract.md`

Tasks:
- [x] Document selector schema used by `k8s_discover`.
- [x] Document pod filtering and endpoint synthesis defaults.
- [x] Document automodel behavior and failure handling.
- [x] Document add/remove reconciliation semantics during refresh.
- [x] Capture the current Secret, ConfigMap, and env wiring used by the chart.
- [x] Capture probe paths and port assumptions.
- [x] Identify chart fields that are part of the external contract vs chart internals.
- [x] Record chart/runtime mismatches and quirks that should not be "fixed" accidentally during the port.

Current candidate quirks:
- [x] `config.example.yaml` contains `auth_preifx`, which appears to be a typo and not the runtime key. Normalize before or during the Go port; do not preserve it.
- [x] Normalize auth prefix default to `PROXY:` before porting; do not preserve the earlier `Proxy:` bug in Go.
- [x] Helm writes `auth_key_from_env` into config for supporting tooling, but runtime auth is supplied via `AUTH_KEY`. Treat this as drift, not runtime contract.
- [x] Discovery only evicts on authoritative absence, not on failed discovery passes. Python reference is normalized to match this contract.

Validation:
- [x] Existing discovery tests cover the happy path and fallback paths.
- [x] Gaps are listed explicitly before any Go implementation starts.
- [x] Each quirk is marked as one of:
  - preserve in Go v1
  - normalize before porting
  - normalize after parity is proven

## Exit Criteria
- We have one written source of truth for the Python contract.
- The parity target for the first Go version is explicit.
- Known ambiguities are listed, not hidden in code.
- We agree on which quirks Go v1 must preserve.
- Milestone 2 can begin without re-reading the whole Python service to rediscover behavior.

## Next Concrete Step
Write the contract document(s) in checkpoint order:
1. start Milestone 2 with the Go module and service skeleton beside the Python reference
2. keep Python as the parity oracle while the first Go request path comes online
3. port discovery and deployment behavior only after the Go request path is stable
