# BORG Architecture

## Purpose

BORG is a Go OpenAI-compatible proxy that combines static backends and
Kubernetes-discovered model endpoints behind one `/v1` API. It can discover
eligible Pods directly or register Kubernetes Services as stable router front
doors, including llm-d scheduler/router Services.

The active runtime is organized under `cmd/` and `internal/`. Application
packages remain internal implementation details rather than a public Go SDK.

## Runtime Shape

```text
.
├── cmd/
│   ├── borg/
│   └── borg-genkey/
├── internal/
│   ├── app/
│   ├── auth/
│   ├── config/
│   ├── discovery/
│   │   └── k8s/
│   ├── httpapi/
│   ├── openai/
│   └── proxy/
├── tests/k8s_smoke/
├── dummy-openai/
├── charts/borg/
├── go.mod
└── go.sum
```

`cmd/borg` loads runtime configuration, creates the application, starts the HTTP
server, and performs graceful shutdown on `SIGTERM` or `SIGINT`.

`cmd/borg-genkey` reads the rendered ConfigMap defaults and effective auth Secret
through Kubernetes, then mints AES-256-GCM bearer tokens using the configured
auth prefix. Secret values must contain printable padded or unpadded base64url
text that decodes to exactly 32 bytes.

## Application Composition

`internal/app` is the composition root. Startup proceeds in this order:

1. Load and resolve configuration.
2. Create the request authenticator.
3. Create the proxy with backend-health and response-header-timeout settings.
4. Register the static backend snapshot.
5. Build the HTTP handler with the resolved request-body limit.
6. Create one reconciler and refresh loop for every configured discovery source.

Discovery refreshes immediately at startup, then waits `update_interval` after
each completed attempt. Sources refresh independently, and attempts for one
source never overlap. Shutdown cancels all source contexts and waits for their
workers to exit.

## Configuration

`internal/config` parses YAML or JSON under the top-level `borg` key and resolves
environment-backed credentials without mutating the parsed input.

Important runtime contracts:

- Inbound auth key precedence is `AUTH_KEY`, the variable named by
  `auth_key_from_env`, `BORG_AUTH_KEY`, inline `auth_key`, then `EMPTY`.
- Static and Service backends support inline `apikey` or `apikeyEnv`; the named
  environment variable wins when populated.
- `max_request_body_bytes` defaults to 64 MiB, `0` is unlimited, and negative
  values are rejected.
- Response-header timeout defaults to 300 seconds, `0` is unlimited, and
  negative values are rejected.
- Backend health defaults to three consecutive failures and a 30-second
  cooldown; ejection on HTTP 500 is disabled by default.
- Discovery source IDs are unique. Pod source IDs can be derived when omitted;
  Service source IDs are explicit.

## HTTP And Authentication

`internal/httpapi` exposes:

- `GET /` for process health and readiness;
- `GET /v1/models` for the sorted union of registered models; and
- authenticated `POST /v1/*` forwarding.

BORG is healthy and Ready with zero registered backends. Unknown models return
404 without changing process readiness.

POST bodies are bounded before JSON parsing and proxy forwarding. Oversized
requests return 413. Authentication is disabled when the resolved auth key is
`EMPTY`; otherwise bearer tokens must decrypt with the configured 32-byte key
and contain the configured plaintext prefix.

## Proxy And Backend Health

`internal/proxy` stores an authoritative endpoint snapshot per source and
materializes those snapshots into per-model round-robin buckets. A failed source
refresh leaves its previous snapshot intact; a successful empty refresh removes
that source's stale endpoints. Updates are atomic, including API-key conflict
validation across sources.

Requests preserve the path, query, and raw JSON body. BORG forwards applicable
end-to-end headers while removing hop-by-hop headers and `Borg-Retry`;
compression and framing headers are normalized rather than preserved verbatim.
BORG replaces upstream `Authorization` with the selected backend credential and
streams both normal and SSE response bodies without buffering the complete
upstream response.

Transport errors, connection drops, and HTTP 502, 503, and 504 responses count
against endpoint health. HTTP 4xx responses do not count, and HTTP 500 only
counts when `eject_on_500` is enabled. Quarantined endpoints are skipped when an
alternative exists; when every endpoint is quarantined, one fallback attempt is
allowed.

Response-header timeout starts only after the upstream request upload completes
and stops when final response headers are available. The default response is a
structured 504 with no automatic timeout retry. A client can permit pre-commit
failover for this case with:

```http
Borg-Retry: response-header-timeout
```

BORG removes this header before forwarding upstream. No retry occurs after
downstream response bytes have been committed.

## Kubernetes Discovery

`internal/discovery/k8s` uses in-cluster configuration first and kubeconfig
fallback second. Kubernetes API requests use the stricter of an inherited
positive timeout and BORG's 30-second maximum.

Pod discovery requires a Pod to be Running, Ready, non-deleting, and assigned a
PodIP. Endpoint protocol, port, and base path come from BORG annotations, with
model IDs read from the configured annotation key or `/v1/models` enumeration.

Service discovery treats each selected Service as a stable front door. BORG
connects through `<name>.<namespace>.svc`, chooses an explicit port, named port,
valid annotation port, or the Service's sole port, and does not expand the
Service into backing Pods. Model precedence is explicit configuration, the
configured Service annotation, then model enumeration.

Per-endpoint enumeration failures are logged and skipped without failing the
source refresh. Enumeration uses a fixed worker pool of eight by default,
preserves candidate ordering, observes cancellation, and has its own 30-second
HTTP timeout. Kubernetes list failures and named-Service GET errors other than
`NotFound` fail only that source refresh and preserve its previous snapshot. A
named Service that returns `NotFound` is a successful empty refresh that removes
that source's stale endpoint.

## Helm And Kubernetes Resources

The Helm chart supports generated, named-but-not-created, and existing auth and
backend credential Secrets. Inline chart-managed backend credentials are Secret
inputs and are omitted from the rendered ConfigMap. Environment references are
portable, collision-checked, and deduplicated.

Pod-template checksums cover the ConfigMap, declared auth input, and
chart-managed backend credentials so relevant Helm upgrades create a new
ReplicaSet. External Secret contents remain outside Helm state and require an
external reloader annotation or an explicit rollout restart.

RBAC can be cluster-scoped or emitted as Roles for an explicit namespace list.
The chart requests only `get` and `list` for the configured Pod and Service
discovery resources and accepts deployment-specific extra rules. The chart also
supports an existing ServiceAccount, security contexts, resource settings,
Ingress, direct TLS, and `LoadBalancer` exposure.

## Validation

Core local quality checks are:

```bash
go test ./...
go test -race ./...
go vet ./...
golangci-lint run ./...
helm lint --strict charts/borg
bash scripts/validate-helm-chart.sh
bash -n scripts/validate-kind-go.sh
git diff --check
```

The fake Kubernetes process-level suite is documented in
`docs/testing/fake-kubernetes-smoke.md`. Real-cluster acceptance is documented
in `docs/testing/kind-validation.md`.
