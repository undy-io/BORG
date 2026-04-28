# Go Project Layout

## Purpose
This document defines the Go repository shape that should exist beside the current Python implementation during the migration.

The goal is to make the Go service easy to build, test, and compare without changing the production Python path until parity is proven.

## Current State
- Python remains the reference runtime.
- A first Go core proxy implementation exists beside the Python runtime.
- Go Kubernetes discovery is implemented behind the existing static proxy path.
- The devcontainer includes Go, Docker, KinD, kubectl, and Helm tooling.
- In the current rootless/containerized WSL environment, Docker-in-Docker cannot start containers because cpuset cgroups are not writable; KinD validation needs host/outside-devcontainer Docker, Docker-outside-of-Docker, or CI/VM infrastructure.
- Host/raw WSL KinD validation works with the node image pinned to Kubernetes v1.34.3.
- Manual raw WSL KinD validation has proven Go BORG startup, Kubernetes discovery, Service access, and `/v1/models` against an annotated dummy backend.
- `scripts/validate-kind-go.sh` automates host/raw WSL KinD validation for Go BORG discovery, authenticated POST forwarding, and streaming.
- The Python contract is frozen in:
  - `docs/migration/python-runtime-contract.md`
  - `docs/migration/python-http-contract.md`
  - `docs/migration/python-ops-contract.md`

## Layout Principles
- Keep Python and Go side by side until final cutover.
- Keep Go application internals under `internal/` so they are not treated as a public library API.
- Keep executable entrypoints under `cmd/`.
- Keep shared test fixtures close to the Go packages that use them.
- Do not move Python files during Milestone 2.
- Do not switch Helm, Docker, or CI defaults to Go during Milestone 2.
- Prefer standard library packages unless a dependency removes real complexity.

## Target Tree
The first Go implementation should grow toward this shape:

```text
.
├── cmd/
│   ├── borg/
│   │   └── main.go
│   └── borg-genkey/
│       └── main.go
├── internal/
│   ├── app/
│   │   ├── app.go
│   │   └── app_test.go
│   ├── auth/
│   │   ├── token.go
│   │   └── token_test.go
│   ├── config/
│   │   ├── config.go
│   │   └── config_test.go
│   ├── discovery/
│   │   ├── discovery.go
│   │   ├── discovery_test.go
│   │   └── k8s/
│   │       ├── k8s.go
│   │       └── k8s_test.go
│   ├── httpapi/
│   │   ├── router.go
│   │   ├── handlers.go
│   │   └── router_test.go
│   ├── openai/
│   │   └── models.go
│   └── proxy/
│       ├── proxy.go
│       ├── roundrobin.go
│       └── proxy_test.go
├── testdata/
│   └── config/
│       └── basic.yaml
├── go.mod
└── go.sum
```

This is a target, not a requirement to create every file in the first commit. Milestone 2 should add only enough files to establish the shape and run a minimal service.

## Entry Points
### `cmd/borg`
Primary Go service entrypoint.

Responsibilities:
- parse CLI flags compatible with the Python runtime where applicable
- load config path from `--config`, `PROXY_CONFIG`, or `config.yaml`
- load port from `--port`, `PORT`, or `8000`
- create the application through `internal/app`
- start the HTTP server

During migration, build it as `bin/borg-go` to avoid confusion with the Python `borg` CLI:

```bash
go build -o bin/borg-go ./cmd/borg
```

### `cmd/borg-genkey`
Go replacement for `genkey.py`.

Responsibilities:
- preserve AES-256-GCM token compatibility
- preserve the `auth_prefix + username` plaintext contract
- preserve support for URL-safe printable auth key Secret data and migrated legacy raw key Secret data
- load local kubeconfig using Kubernetes default loading rules
- read ConfigMap defaults and auth Secret data using `client-go`
- keep the Python utility available until final cutover

During migration, build it as `bin/borg-genkey`:

```bash
go build -o bin/borg-genkey ./cmd/borg-genkey
```

## Internal Packages
### `internal/app`
Composition root for the Go service.

Responsibilities:
- wire config, auth, proxy, router, and discovery
- own background discovery lifecycle
- support isolated app construction for tests
- avoid hidden global routing state
- start discovery only when `update_interval > 0` and `k8s_discover` is non-empty
- expose `Close()` so command and tests can stop background discovery cleanly

Python reference:
- `src/borg/main.py:create_app`

### `internal/config`
Configuration loading and normalization.

Responsibilities:
- parse YAML and JSON config files
- preserve `borg` top-level config shape
- implement env/config precedence
- normalize defaults without changing the external contract

Python reference:
- `src/borg/main.py`
- `docs/migration/python-runtime-contract.md`

### `internal/auth`
Token and auth key handling.

Responsibilities:
- decode URL-safe base64 auth keys
- validate 32-byte AES-256 keys
- decrypt bearer tokens
- enforce auth prefix checks
- generate tokens for `cmd/borg-genkey`
- decode auth Secret values from either printable URL-safe key text or legacy raw key bytes

Python reference:
- `src/borg/proxy.py`
- `genkey.py`
- `docs/migration/python-runtime-contract.md`

### `internal/httpapi`
HTTP routes and handlers.

Responsibilities:
- expose `GET /`
- expose `GET /v1/models`
- expose `POST /v1/{remainder:path}` equivalent behavior
- apply auth only to POST proxy routes
- translate proxy errors into Python-compatible HTTP responses

Python reference:
- `src/borg/main.py`
- `docs/migration/python-http-contract.md`

### `internal/proxy`
Model registry, upstream selection, and request forwarding.

Responsibilities:
- register and remove backend instances by model
- maintain round-robin endpoint selection
- forward non-streaming requests
- forward streaming requests
- rewrite upstream Authorization headers
- preserve query string, body, and header behavior

Python reference:
- `src/borg/proxy.py`
- `tests/test_proxy_service_instances.py`
- `docs/migration/python-http-contract.md`

### `internal/discovery`
Discovery interfaces shared by the app and concrete discovery backends.

Responsibilities:
- define discovered endpoint data structures
- define a refresh/update interface
- keep Kubernetes-specific code behind a narrow boundary
- reconcile only successful discovery snapshots into the proxy registry
- preserve the previous successful discovered snapshot when discovery fails

Python reference:
- `src/borg/k8s_discovery.py`
- `docs/migration/python-ops-contract.md`

### `internal/discovery/k8s`
Kubernetes implementation of discovery.

Responsibilities:
- load in-cluster config with kubeconfig fallback
- list pods by namespace and selector
- apply Running-pod and annotation/model eligibility rules
- synthesize endpoints from pod IP and annotations
- preserve authoritative refresh semantics
- resolve models from the configured annotation key before automodel fallback
- query automodel via `GET <endpoint>/v1/models` with `Authorization: Bearer EMPTY`

The first Go discovery pass uses polling only. Kubernetes watches/informers, health-check eviction, per-discovery upstream API keys, and Helm/Docker cutover remain later work.

### `internal/openai`
Small OpenAI-compatible response/request structs.

Responsibilities:
- define `/v1/models` response structures
- define lightweight helper types only when they reduce duplication

Avoid turning this into a full OpenAI client library.

## Testing Layout
Use normal Go package tests for unit behavior:

```bash
go test ./...
```

Expected early coverage:
- config loading and env precedence
- auth key and token compatibility
- root route response
- `/v1/models` response shape
- model registration and round-robin selection
- Kubernetes discovery eligibility and reconciliation behavior
- app discovery lifecycle gates and shutdown

Parity tests can start small and grow:
- keep Python tests green with `uv run pytest -q`
- add Go tests beside the package being implemented
- add side-by-side integration tests only after the Go request path exists

The Kubernetes-free local smoke/parity harness is implemented under `tests/smoke` and documented in `docs/migration/local-smoke-test-harness.md`.
The fake Kubernetes API smoke harness is implemented under `tests/k8s_smoke` and documented in `docs/migration/go-k8s-smoke-test-harness.md`.

On a host/runtime with usable Docker cgroups, validate the local KinD toolchain with:

```bash
docker version
kind version
kubectl version --client
kind create cluster --name borg --config kind-config.yaml \
  --image kindest/node:v1.34.3@sha256:08497ee19eace7b4b5348db5c6a1591d7752b164530a36f855cb0f2bdcbadd48
kubectl wait --for=condition=Ready node/borg-control-plane --timeout=120s
kubectl get nodes
kubectl get pods -A
kind delete cluster --name borg
```

Run the repeatable Go KinD validation harness from raw WSL/host:

```bash
scripts/validate-kind-go.sh
scripts/validate-kind-go.sh --create-cluster --delete-cluster
```

## Build And Run Commands
These commands are valid for the side-by-side Go implementation:

```bash
go test ./...
go build -o bin/borg-go ./cmd/borg
go build -o bin/borg-genkey ./cmd/borg-genkey
./bin/borg-go --config config.yaml --port 8001
```

Python remains available during the same phase:

```bash
uv run borg --config config.yaml --port 8000
```

## Files That Stay In Place
Do not move or remove these during Milestone 2:

- `src/borg/`
- `tests/`
- `pyproject.toml`
- `uv.lock`
- `genkey.py`
- `Dockerfile`
- `charts/borg/`
- `config.example.yaml`

Those files remain the Python reference and deployment fallback until the cutover milestone.

## Milestone 2 Baseline
The side-by-side Go baseline is useful when:

- `go.mod` exists
- `cmd/borg` builds into `bin/borg-go`
- `cmd/borg-genkey` builds into `bin/borg-genkey`
- `go test ./...` passes
- the Go service can serve `GET /`
- config path and port precedence match the Python contract
- core proxy behavior is covered by Go package tests and local smoke tests
- Kubernetes discovery is covered by Go package tests
- Go Kubernetes discovery is covered by a fake API smoke test against the real `bin/borg-go` process
- `README.md`, `ROADMAP.md`, `MILESTONE.md`, and `SESSION_RECOVERY.md` describe the side-by-side workflow
- `docs/migration/local-smoke-test-harness.md` describes how to validate the static proxy path locally without Kubernetes
- `docs/migration/go-k8s-smoke-test-harness.md` describes how to validate Go discovery locally without a real cluster
- Python tests still pass
