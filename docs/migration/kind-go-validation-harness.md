# KinD Go Validation Harness

## Purpose
Validate the Go BORG runtime against a real local Kubernetes cluster after Docker and Helm defaults switch to Go.

The harness exercises the path that unit tests and fake API smoke tests cannot fully prove:
- building the Go runtime
- packaging it into a local container image
- loading images into KinD
- deploying the dummy OpenAI backend with Helm
- deploying Go BORG with Helm
- discovering the backend through Kubernetes pod annotations
- validating root, model listing, auth failure, authenticated POST forwarding, and SSE streaming

Implemented script:
- `scripts/validate-kind-go.sh`

## Where To Run It
Run this script from raw WSL or another host environment with working Docker, KinD, kubectl, Helm, Go, and curl.

The current devcontainer includes the right tools, but Docker-in-Docker KinD is blocked in this environment because the nested Docker daemon cannot create cpuset cgroups. Until the host/runtime cgroup setup changes, use raw WSL/host KinD for real cluster validation.

## Prerequisites
Required commands:
- `go`
- `docker`
- `kind`
- `kubectl`
- `helm`
- `curl`

The script defaults to a pinned Kubernetes node image that works on the current WSL cgroup v1 runtime:

```text
kindest/node:v1.34.3@sha256:08497ee19eace7b4b5348db5c6a1591d7752b164530a36f855cb0f2bdcbadd48
```

## Quick Commands
Use an existing `kind-borg` cluster:

```bash
scripts/validate-kind-go.sh
```

Create the cluster when missing and delete it after validation:

```bash
scripts/validate-kind-go.sh --create-cluster --delete-cluster
```

Create the cluster and leave it running for debugging:

```bash
scripts/validate-kind-go.sh --create-cluster
```

Remove only the Helm releases and namespaces on exit:

```bash
scripts/validate-kind-go.sh --cleanup-resources
```

Useful options:

```bash
scripts/validate-kind-go.sh --cluster-name borg --local-port 18080
scripts/validate-kind-go.sh --node-image kindest/node:v1.34.3@sha256:08497ee19eace7b4b5348db5c6a1591d7752b164530a36f855cb0f2bdcbadd48
```

## What The Script Does
The script:
- checks required commands
- optionally creates the KinD cluster from `kind-config.yaml`
- waits for the control-plane node to become `Ready`
- builds host Go binaries into ignored `build/kind/`
- packages `borg-go:kind` from the local binary instead of running `go mod download` inside Docker
- builds `dummy-openai:kind`
- loads both images into KinD
- deploys `dummy-openai` into `vllm-services`
- deploys Go BORG into `borg`
- writes Helm values with `update_interval: 2`, auth prefix `PROXY:`, and discovery selector `borg/expose=vllm`
- mints a validation token with the built Go `borg-genkey`
- port-forwards `svc/borg-borg` to `127.0.0.1:<local-port>`
- validates HTTP behavior with `curl`

## Assertions
The validation run checks that:
- `GET /` returns a healthy BORG router response
- `GET /v1/models` includes `gpt-3.5-turbo`
- unauthenticated POST `/v1/chat/completions` returns `401`
- authenticated non-streaming POST reaches the dummy backend
- discovered endpoints rewrite upstream auth to `Bearer EMPTY`
- POST path is preserved
- authenticated streaming POST returns deterministic SSE chunks and `data: [DONE]`

## Failure Diagnostics
On failure after cluster readiness, the script prints:
- BORG deployment, service, and pod status
- dummy backend deployment and pod status
- BORG deployment describe output
- recent BORG logs
- recent dummy backend logs
- port-forward log output

By default, the script leaves resources in place for debugging. Use `--cleanup-resources` when you want the script to remove Helm releases and namespaces before exit.

## Relationship To Other Tests
Use this harness after the faster local checks are green:

```bash
go test ./...
go build -o bin/borg-go ./cmd/borg
go build -o bin/borg-genkey ./cmd/borg-genkey
uv run pytest -q tests/smoke
uv run pytest -q tests/k8s_smoke
```

The local smoke suite proves Python-vs-Go static proxy parity without Kubernetes.
The fake Kubernetes smoke suite proves Go discovery behavior without Docker or a real cluster.
The KinD harness proves that the Go runtime, Helm chart, Kubernetes discovery, auth utility, and container packaging work together in a real local cluster.

## Current Validation State
The full create/delete path has passed from raw WSL:

```bash
scripts/validate-kind-go.sh --create-cluster --delete-cluster
```

That run completed cluster creation, image build/load, Helm deployment, root/model checks, missing-auth rejection, authenticated POST forwarding, SSE streaming, and cluster deletion.
