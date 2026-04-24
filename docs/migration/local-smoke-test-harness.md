# Local Smoke And Parity Harness

## Purpose
Provide a Kubernetes-free validation loop for the Go migration.

The harness runs the Python reference service and the Go service side by side against local OpenAI-compatible dummy upstreams. This keeps the static proxy path easy to validate while Kubernetes discovery and deployment wiring evolve separately.

Implemented test suite:
- `tests/smoke/test_local_parity.py`

## Harness Shape
The harness is a pytest-based integration suite that reuses the existing Python test environment and spawns both runtimes as subprocesses.

Expected components:
- one or more local dummy upstream HTTP servers started on ephemeral ports
- one generated static config file per proxy process
- the Python service launched with `uv run borg --config <path> --host 127.0.0.1 --port <port>`
- the Go service launched with `bin/borg-go --config <path> --host 127.0.0.1 --port <port>`
- an HTTP client that sends the same requests to both services and compares results
- process cleanup that always terminates proxies and upstreams

The harness does not require Docker, Helm, KinD, a Kubernetes API server, or cluster credentials.

## Dummy Upstream Behavior
The local upstream is intentionally small but OpenAI-shaped:
- `GET /v1/models` returns a deterministic model list
- `POST /v1/chat/completions` records method, path, query, headers, and body
- non-streaming responses return JSON with the recorded request details
- streaming responses emit deterministic SSE chunks and a `[DONE]` marker
- optional gzip response mode for non-streaming compression checks

The upstream exposes enough recorded state for tests to verify that BORG rewrites upstream `Authorization` and forwards path, query, body, and allowed headers correctly.

## Smoke Cases
Exact Python-vs-Go comparison cases:
- `GET /` returns the expected health payload
- `GET /v1/models` returns sorted model data from static configured instances
- invalid JSON, non-object JSON, missing model, and unknown model return matching status and detail
- non-streaming forwarding preserves path, query, body, content type, upstream status, and upstream auth rewrite
- streaming via `stream: true` returns the expected SSE chunks
- streaming via `Accept: text/event-stream` returns the expected SSE chunks
- unauthenticated POSTs fail when auth is configured
- valid AES-256-GCM bearer tokens succeed against both runtimes

Go-specific accepted-delta cases:
- non-streaming requests do not forward the client `Accept-Encoding`; Go may negotiate gzip upstream and returns decoded downstream bytes
- streaming requests send upstream `Accept-Encoding: identity` to protect SSE latency
- request and response headers named by `Connection` are stripped

These accepted deltas are documented in test names and assertions rather than hidden as generic parity failures.

## Config Strategy
Generate temporary config files with static instances only:

```yaml
borg:
  auth_key: "EMPTY"
  auth_prefix: "PROXY:"
  update_interval: -1
  instances:
    - endpoint: "http://127.0.0.1:<upstream-port>"
      apikey: "sk-upstream"
      models: ["alpha", "openai/gpt-oss-20b"]
  k8s_discover: []
```

Use a second config with a URL-safe base64 AES-256 auth key for auth parity cases.

## Execution Contract
The test command is a normal local developer command:

```bash
go build -o bin/borg-go ./cmd/borg
uv run pytest -q tests/smoke
```

The smoke suite skips Go-side checks with a clear message if `bin/borg-go` is missing, but the preferred local and CI path should build the binary first.

## Out Of Scope
- Kubernetes discovery parity
- Helm rendering or deployment validation
- container image validation
- load testing beyond the existing Go streaming benchmark
- replacing the existing Python unit tests or Go package tests

## Done Criteria
The harness is useful when it can run entirely on a laptop/devcontainer and answer one question quickly:

Can the Python and Go services proxy the same static OpenAI-compatible traffic with the same client-visible behavior, except for explicitly accepted Go compression/header deltas?
