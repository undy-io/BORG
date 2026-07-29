# Fake Kubernetes Smoke Harness

## Purpose

Validate the real BORG process and Kubernetes discovery wiring without Docker,
Helm, KinD, or cluster credentials.

The Go suite in `tests/k8s_smoke/k8s_smoke_test.go` builds `./cmd/borg` once,
starts it as a subprocess, and connects it to:

- a fake Kubernetes API server on localhost;
- a temporary kubeconfig pointing `client-go` at that API; and
- local OpenAI-compatible dummy upstream servers.

## Harness Shape

The fake API implements the selector-based list requests exercised by this
suite:

```text
GET /api/v1/namespaces/<namespace>/pods?labelSelector=...
GET /api/v1/namespaces/<namespace>/services?labelSelector=...
```

It returns Kubernetes-shaped Pod and Service lists, records resource, namespace,
and selector requests, supports runtime object replacement, and can force list
failures.

Each BORG subprocess uses normal runtime wiring with a temporary config,
kubeconfig, and loopback port. Discovery refreshes every second so tests observe
registration, removal, and failed-refresh preservation through the public HTTP
API.

## Covered Behavior

- Pod annotation-based model discovery;
- Service front-door endpoint registration and named-port selection;
- namespace and label-selector request shape for Pods and Services;
- automodel `/v1/models` lookup;
- successful stale endpoint removal;
- preservation of the last successful snapshot after Kubernetes list failure;
- protocol, API port, and base-path annotation overrides;
- forwarding through discovered endpoints with rewritten backend auth; and
- process startup, polling, and shutdown through the real command entrypoint.

## Running The Suite

```bash
go test ./tests/k8s_smoke
```

The suite requires loopback sockets. In restricted execution environments it
must run with permission to bind local test servers.

## Boundaries

The process harness does not implement named-Service GET requests. Package tests
under `internal/discovery/k8s` cover named-Service lookup and the successful
empty snapshot produced by `NotFound`. Because the fake suite does not resolve
cluster DNS, its Service case proves endpoint registration and port selection,
not forwarding through a Kubernetes Service.

The fake suite also does not validate real RBAC, Pod networking, Helm
installation, container packaging, or Kubernetes rollout behavior. Those paths
belong to the host KinD harness documented in `docs/testing/kind-validation.md`.
