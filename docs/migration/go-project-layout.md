# Go Project Layout

## Purpose
This document defines the active Go repository shape used by BORG.

Go is now the only active BORG runtime. The former Python implementation has been removed from the source tree; the remaining Python code exists only for Go validation helpers.

## Current State
- Go core proxying, Kubernetes discovery, and token generation are implemented.
- The root Dockerfile builds `/usr/local/bin/borg` and `/usr/local/bin/borg-genkey`.
- The Helm chart deploys the Go runtime by default while preserving its values shape.
- Go CI runs unit tests, vet, command builds, and fake Kubernetes smoke validation.
- `tests/k8s_smoke` remains as a Python-based smoke harness for the Go binary.
- `dummy-openai/` remains as a Go test backend for KinD validation.
- In the current rootless/containerized WSL environment, Docker-in-Docker cannot start containers because cpuset cgroups are not writable; KinD validation needs host/outside-devcontainer Docker, Docker-outside-of-Docker, or CI/VM infrastructure.
- Host/raw WSL KinD validation works with the node image pinned to Kubernetes v1.34.3.
- `scripts/validate-kind-go.sh` automates host/raw WSL KinD validation for Go BORG discovery, authenticated POST forwarding, and streaming.
- The full create/delete KinD harness path has passed from raw WSL.
- Historical Python contract docs remain in `docs/migration/python-*.md`.

## Layout Principles
- Keep Go application internals under `internal/` so they are not treated as a public library API.
- Keep executable entrypoints under `cmd/`.
- Keep Go tests beside the packages they exercise.
- Keep Python out of the runtime and release path.
- Treat retained Python as validation harness code only.
- Prefer standard library packages unless a dependency removes real complexity.

## Active Tree

```text
.
├── cmd/
│   ├── borg/
│   │   └── main.go
│   └── borg-genkey/
│       └── main.go
├── internal/
│   ├── app/
│   ├── auth/
│   ├── config/
│   ├── discovery/
│   │   └── k8s/
│   ├── httpapi/
│   ├── openai/
│   └── proxy/
├── tests/
│   └── k8s_smoke/
├── dummy-openai/
├── go.mod
├── go.sum
├── pyproject.toml
└── uv.lock
```

`pyproject.toml` and `uv.lock` are retained only for the fake Kubernetes smoke tests.

## Entry Points
### `cmd/borg`
Primary Go service entrypoint.

Responsibilities:
- parse CLI flags: `--config`, `-c`, `--host`, `--port`, and no-op `--reload`
- load config path from `--config`, `PROXY_CONFIG`, or `config.yaml`
- load port from `--port`, `PORT`, or `8000`
- create the application through `internal/app`
- start the HTTP server
- handle `SIGTERM` and `SIGINT` graceful shutdown

Production images install this command as `/usr/local/bin/borg`.

During local smoke testing, build it as `bin/borg-go`:

```bash
mkdir -p bin
go build -o bin/borg-go ./cmd/borg
```

### `cmd/borg-genkey`
Go token utility.

Responsibilities:
- preserve AES-256-GCM token compatibility
- preserve the `auth_prefix + username` plaintext contract
- support URL-safe printable auth key Secret data and legacy raw key Secret data
- load local kubeconfig using Kubernetes default loading rules
- read ConfigMap defaults and auth Secret data using `client-go`

Production images install this command as `/usr/local/bin/borg-genkey`.

During local testing, build it as `bin/borg-genkey`:

```bash
mkdir -p bin
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

### `internal/config`
Configuration loading and normalization.

Responsibilities:
- parse YAML and JSON config files
- preserve `borg` top-level config shape
- implement env/config precedence
- normalize defaults without changing the external contract

### `internal/auth`
Token and auth key handling.

Responsibilities:
- decode URL-safe base64 auth keys
- validate 32-byte AES-256 keys
- decrypt bearer tokens
- enforce auth prefix checks
- generate tokens for `cmd/borg-genkey`
- decode auth Secret values from either printable URL-safe key text or legacy raw key bytes

### `internal/httpapi`
HTTP routes and handlers.

Responsibilities:
- expose `GET /`
- expose `GET /v1/models`
- expose `POST /v1/{remainder}` behavior
- apply auth only to POST proxy routes
- translate proxy errors into stable HTTP responses

### `internal/proxy`
Model registry, upstream selection, and request forwarding.

Responsibilities:
- register and remove backend instances by model
- maintain round-robin endpoint selection
- forward non-streaming requests
- forward streaming requests
- rewrite upstream Authorization headers
- preserve query string, body, and header behavior

### `internal/discovery`
Discovery interfaces shared by the app and concrete discovery backends.

Responsibilities:
- define discovered endpoint data structures
- define refresh/update interfaces
- keep Kubernetes-specific code behind a narrow boundary
- reconcile only successful discovery snapshots into the proxy registry
- preserve the previous successful discovered snapshot when discovery fails

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

Expected coverage:
- config loading and env precedence
- auth key and token compatibility
- root route response
- `/v1/models` response shape
- model registration and round-robin selection
- non-streaming and streaming forwarding
- Kubernetes discovery eligibility and reconciliation behavior
- app discovery lifecycle gates and shutdown

The fake Kubernetes API smoke harness is implemented under `tests/k8s_smoke` and documented in `docs/migration/go-k8s-smoke-test-harness.md`.
The real KinD Go validation harness is implemented at `scripts/validate-kind-go.sh` and documented in `docs/migration/kind-go-validation-harness.md`.

## Build And Run Commands
These commands are valid for local Go development:

```bash
go test ./...
go vet ./...
mkdir -p bin
go build -o bin/borg-go ./cmd/borg
go build -o bin/borg-genkey ./cmd/borg-genkey
./bin/borg-go --config config.yaml --port 8001
```

Run retained smoke validation:

```bash
uv sync --frozen
uv run pytest -q tests/k8s_smoke
```

On a host/runtime with usable Docker cgroups, run the repeatable Go KinD validation harness from raw WSL/host:

```bash
scripts/validate-kind-go.sh
scripts/validate-kind-go.sh --create-cluster --delete-cluster
```

## Go Runtime Baseline
The active runtime baseline is useful when:

- `go.mod` exists
- `cmd/borg` builds into `bin/borg-go`
- `cmd/borg-genkey` builds into `bin/borg-genkey`
- `go test ./...` passes
- `go vet ./...` passes
- the Go service can serve `GET /`
- config path and port precedence are stable
- core proxy behavior is covered by Go package tests
- Kubernetes discovery is covered by Go package tests
- fake API smoke tests exercise the real `bin/borg-go` process
- `README.md`, `ROADMAP.md`, `MILESTONE.md`, and `SESSION_RECOVERY.md` describe the Go-only runtime
- historical Python docs are clearly marked as historical
