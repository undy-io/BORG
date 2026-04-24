# Python HTTP Contract

## Purpose
This document freezes the current Python HTTP behavior that the Go implementation must match during the migration, unless we explicitly choose to normalize a bug first.

## Sources
- `src/borg/main.py`
- `src/borg/proxy.py`
- `tests/test_proxy_router.py`
- `tests/test_proxy_service_instances.py`

## Route Surface
The Python service exposes exactly these application routes:

- `GET /`
- `GET /v1/models`
- `POST /v1/{remainder:path}`

Implications:
- OpenAI-compatible request forwarding only exists on the POST catch-all route.
- `GET` requests to arbitrary `/v1/*` paths are not proxied by the app.
- The catch-all path segment is not used directly for routing decisions; the proxy forwards the original request path from `request.url.path`.

## Auth Coverage
Auth is not applied uniformly across all routes.

- `GET /`
  - no auth check
- `GET /v1/models`
  - no auth check
- `POST /v1/{remainder:path}`
  - always calls `proxy.require_auth(request)` before body parsing or upstream routing

Observed behavior:
- When auth is enabled, `/v1/models` still responds successfully without a bearer token.
- When auth is disabled, POST requests are accepted and `request.state.username` becomes `ANONYMOUS`.

This is part of the current HTTP contract and should not be changed during the Go port without an explicit decision.

## `GET /`

Response behavior:
- Status: `200`
- Content type: JSON
- Body:

```json
{
  "status": "ok",
  "detail": "Proxy router is running"
}
```

Coverage:
- Backed by `test_root_ok`

## `GET /v1/models`

Response behavior:
- Status: `200`
- Content type: JSON
- Body shape:

```json
{
  "object": "list",
  "data": [
    {
      "id": "<model-name>",
      "object": "model",
      "created": null,
      "owned_by": "vllm-proxy"
    }
  ]
}
```

Model-list semantics:
- Returns the union of currently registered models.
- Filters out empty model buckets.
- Sorts model ids ascending.
- Does not require auth, even when POST routes do.

Coverage:
- Backed by `test_list_models`
- Backed by `test_factory_apps_isolate_auth_and_models`
- Backed by `tests/test_proxy_service_instances.py::test_list_models_is_sorted_and_filters_empty`

## `POST /v1/{remainder:path}`

### Request preconditions
- The route expects a JSON request body.
- The parsed JSON must decode to an object containing a truthy `model` field.
- The proxy chooses the upstream path from the incoming request path and forwards incoming query params.

Current explicit error behavior:
- Invalid JSON body -> `400` with detail `Body must be valid JSON`
- Valid JSON that is not an object -> `400` with detail `Body must be valid JSON`
- Missing or falsey `model` -> `400` with detail `Missing 'model' in request body`
- Unknown model -> `404` with detail containing `Unknown model`

Coverage:
- Backed by `test_proxy_rejects_invalid_json_body`
- Backed by `test_proxy_rejects_non_object_json_body`
- Backed by `test_proxy_rejects_missing_model_in_body`
- Backed by `test_unknown_model_returns_404`

### Auth behavior on POST requests
When auth is enabled:
- Missing `Authorization` header -> `401` with detail `Missing API key`
- Non-bearer `Authorization` header -> `401` with detail `Missing API key`
- Undecryptable or malformed bearer token -> `401` with detail `Invalid API key`
- Token with the wrong plaintext prefix -> `401` with detail `Invalid API key`
- Valid token -> request continues to proxying

When auth is disabled:
- No bearer token is required
- The request continues to proxying

Coverage:
- Backed by `test_auth_enforced_when_configured`
- Backed by `test_default_auth_prefix_is_proxy_uppercase`

### Upstream selection
- The request body `model` selects a model bucket.
- The proxy uses round-robin endpoint selection within that model bucket.
- Unknown models do not hit upstream; they fail locally with `404`.

Coverage:
- Backed by `test_unknown_model_returns_404`
- Backed by `tests/test_proxy_service_instances.py`

### Forwarded request behavior
For both streaming and non-streaming requests:
- The original request method is forwarded.
- The original request path is forwarded.
- The original query params are forwarded.
- The raw request body is forwarded.
- Most inbound headers are forwarded unchanged except hop-by-hop headers.
- The inbound `Authorization` header is always replaced with `Bearer <backend-api-key>`.

Headers excluded from forwarding:
- `host`
- `content-length`
- `connection`
- `keep-alive`
- `proxy-authenticate`
- `proxy-authorization`
- `te`
- `trailers`
- `transfer-encoding`
- `upgrade`

Content-type notes:
- JSON parsing happens from the raw body, not from the `Content-Type` header.
- The proxy does not perform a separate content-type gate before attempting JSON parsing.
- For non-streaming requests, the original request `Content-Type` is forwarded upstream if present.

Coverage:
- Backed by `test_proxy_forwards_and_swaps_auth_header`
- Query-param forwarding is code-backed but not explicitly characterized yet.

### Streaming selection
The proxy takes the streaming path when either condition is true:

- request JSON contains a truthy `stream` field
- request `Accept` header contains `text/event-stream`

If neither is true, the proxy takes the non-streaming path.

Coverage:
- Backed by `test_proxy_streaming_via_stream_flag`
- Backed by `test_proxy_streaming_via_accept_header`

### Non-streaming response behavior
- Uses an `httpx.AsyncClient` with `timeout=30.0`
- Returns upstream status code unchanged
- Returns upstream body unchanged
- Returns most upstream headers unchanged
- Removes these response headers:
  - `content-encoding`
  - `transfer-encoding`
  - `connection`
- Uses upstream `content-type` as the response media type

Coverage:
- Status/body behavior is indirectly covered by `test_proxy_forwards_and_swaps_auth_header`
- Exact response-header passthrough is code-backed but not explicitly characterized yet.

### Streaming response behavior
- Uses an `httpx.AsyncClient` with no timeout
- Opens an upstream stream and returns a `StreamingResponse`
- Returns upstream status code unchanged
- Streams upstream bytes through as they arrive
- Drops empty chunks
- Treats `httpx.StreamClosed` as normal EOF
- Treats downstream cancellation as normal termination
- Closes the upstream stream and client in a `finally` block
- Removes these response headers:
  - `content-length`
  - `content-encoding`
  - `transfer-encoding`
  - `connection`
- Uses upstream `content-type` as the response media type

Coverage:
- Backed by `test_proxy_streaming_via_stream_flag`
- Backed by `test_proxy_streaming_via_accept_header`
- Exact streaming response-header passthrough is code-backed but not explicitly characterized yet.

### Error logging behavior
- Exceptions during proxying are logged with `Fault occurred proxying the request` and then re-raised.
- Unknown-model and auth errors surface as FastAPI HTTP errors to the client.

## Known Drift And Open Questions
- Query-param forwarding is implemented in code but not pinned by a characterization test yet.
- Exact response-header passthrough behavior is implemented in code but not fully pinned by tests yet.

## Go First-Pass Accepted Deltas
The first Go proxy intentionally tightens a few header behaviors while preserving client-visible decoded/plain downstream responses:

- Non-streaming Go requests do not forward client `Accept-Encoding`; Go's transport may negotiate gzip upstream and auto-decode the body before BORG responds downstream.
- Streaming Go requests send upstream `Accept-Encoding: identity` to avoid gzip buffering and protect SSE/token latency.
- Go strips both `Trailer` and `Trailers`, plus headers named by `Connection`, in request and response proxying.
- Go backend API key precedence is `apikeyEnv` value, inline `apikey`, `API_KEY`, then `EMPTY`.

The local smoke/parity harness should treat these as explicit accepted deltas rather than generic parity failures.

## Go Layout Link
The HTTP route and proxy responsibilities for the Go implementation are mapped in `docs/migration/go-project-layout.md`.
