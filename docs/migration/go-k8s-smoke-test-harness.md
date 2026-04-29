# Go Kubernetes Smoke Harness

## Purpose
Validate Go Kubernetes discovery locally without Docker, Helm, KinD, Minikube, or cluster credentials.

The suite runs the real `bin/borg-go` process against:
- a fake Kubernetes API server exposed over localhost
- a temporary kubeconfig pointing `client-go` at that fake API
- local OpenAI-compatible dummy upstream servers

Implemented test suite:
- `tests/k8s_smoke/test_go_k8s_discovery.py`

## Harness Shape
The fake Kubernetes API implements the single discovery path the Go service needs for polling:

```text
GET /api/v1/namespaces/<namespace>/pods?labelSelector=...
```

It returns Kubernetes-shaped `PodList` JSON, records namespace and selector requests, can mutate pods at runtime, and can force list failures.

The Go subprocess uses normal runtime wiring:
- `KUBECONFIG=<temp kubeconfig>`
- `--config <temp borg config>`
- `--host 127.0.0.1`
- `--port <ephemeral port>`

The BORG config uses `update_interval: 1` and a real `k8s_discover` selector. Static instances are omitted so `/v1/models` reflects discovered endpoints only.

## Covered Behavior
- annotation-based model discovery
- namespace and selector request shape
- automodel lookup via discovered endpoint `/v1/models`
- successful reconciliation removal when pods disappear
- failed Kubernetes list preservation of the previous successful snapshot
- endpoint annotation overrides for protocol, API port, and API base path
- forwarding through discovered endpoints with `Bearer EMPTY`

## Execution Contract
Build the Go binary first, then run the suite:

```bash
go build -o bin/borg-go ./cmd/borg
uv run pytest -q tests/k8s_smoke
```

The suite skips with a clear message when `bin/borg-go` is missing.

## Out Of Scope
- Python parity
- real Kubernetes RBAC
- real pod networking
- Helm rendering
- Docker image validation
- KinD or Minikube deployment validation

Those belong to the real KinD validation harness in `docs/migration/kind-go-validation-harness.md`.
