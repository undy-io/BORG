# Python Runtime Contract

## Purpose
This document freezes the current Python runtime/config/auth contract that the Go implementation must match during the migration.

## Sources
- `pyproject.toml`
- `src/borg/__main__.py`
- `src/borg/main.py`
- `src/borg/proxy.py`
- `genkey.py`
- `tests/test_proxy_router.py`
- `tests/test_genkey.py`

## Entrypoints
- Installed CLI:
  - `borg`
  - Defined by `pyproject.toml` as `borg.__main__:run`
- Module execution:
  - `python -m borg`
  - Runs the same `run()` function in `src/borg/__main__.py`
- ASGI factory:
  - `borg.main:create_app`
  - Used directly in tests and by `uvicorn` reload mode

## CLI Contract
The Python CLI accepts these flags:

- `--config`, `-c`
  - Default: `PROXY_CONFIG` env var, else `config.yaml`
- `--host`
  - Default: `0.0.0.0`
- `--port`
  - Default: `PORT` env var, else `8000`
- `--reload`
  - Default: `false`

Observed startup behavior:
- `run()` writes the chosen `--config` value back into `PROXY_CONFIG`.
- With `--reload`, `uvicorn` is started with the factory path `borg.main:create_app`.
- Without `--reload`, `uvicorn` is started with an already-created app instance from `main.create_app(args.config)`.

## Config File Contract
- The config file path comes from:
  - CLI `--config`, if supplied
  - else `PROXY_CONFIG`
  - else `config.yaml`
- The file must exist at startup or app initialization fails.
- File format is selected by filename suffix:
  - `.json` -> `json.load`
  - anything else -> `yaml.safe_load`
- The runtime expects the top-level config object to contain a `borg` mapping.
- Missing `borg` config is treated as `{}`.

Recognized runtime keys under `borg`:
- `auth_key`
- `auth_prefix`
- `instances`
- `update_interval`
- `k8s_discover`

Important non-runtime drift:
- Helm currently writes `auth_key_from_env` into the ConfigMap.
- The Python runtime does not read `auth_key_from_env`.
- Runtime auth instead comes from the `AUTH_KEY` or `BORG_AUTH_KEY` environment variables.

## Environment Contract

### App startup env vars
- `PROXY_CONFIG`
  - Default config path when CLI `--config` is not supplied
- `PORT`
  - Default CLI port when `--port` is not supplied
- `AUTH_KEY`
  - Highest-precedence auth key source
- `BORG_AUTH_KEY`
  - Legacy auth key env var, used only when `AUTH_KEY` is unset
- `API_KEY`
  - Fallback backend API key for instances that do not resolve a more specific key

### Auth key precedence
At startup the service resolves the auth key in this order:

1. `AUTH_KEY`
2. `BORG_AUTH_KEY`
3. `borg.auth_key`
4. no auth key, represented by the sentinel string `EMPTY`

Auth key requirements:
- Value is expected to be a URL-safe base64 string.
- Decoded value must be exactly 32 bytes.
- Any non-`EMPTY` value that fails to decode or decode to 32 bytes causes startup failure.

### Backend API key precedence
For each configured instance, the backend API key is resolved in this order:

1. If `apikeyEnv` is set on the instance, read that env var.
2. If `apikeyEnv` is set but missing in the environment, fall back to `API_KEY`, else `EMPTY`.
3. If `apikeyEnv` is not set, use instance `apikey`.
4. If neither `apikeyEnv` nor `apikey` is available, fall back to `API_KEY`, else `EMPTY`.

Examples:
- Instance with `apikeyEnv: VLLM_APIKEY_1` uses `VLLM_APIKEY_1` when present.
- Instance with only `apikey: sk-test` uses `sk-test`.
- Instance with neither key source falls back to `API_KEY` or `EMPTY`.

## Auth Prefix Contract
- Runtime config may supply `borg.auth_prefix`.
- If a truthy `auth_prefix` is supplied, the proxy uses it for token validation.
- If no truthy `auth_prefix` is supplied, `ProxyService` keeps its constructor default of `PROXY:`.

Normalization decision:
- `PROXY:` is the intended default contract.
- Earlier `Proxy:` behavior in `ProxyService` was a bug and should not be preserved in Go.
- `genkey.py` already matches the intended default by falling back to `PROXY:`.

## Token Compatibility Contract
The proxy expects bearer tokens with this structure:

- Encryption mode: AES-256-GCM
- Nonce length: 12 bytes
- Plaintext payload: `auth_prefix + username`
- Wire format: base64url of `nonce || ciphertext_and_tag`

Validation behavior in `ProxyService.require_auth()`:
- If no auth key is configured, requests are accepted and `request.state.username` becomes `ANONYMOUS`.
- If auth is enabled, `Authorization` must start with `Bearer `.
- Token decryption failure returns `401 Invalid API key`.
- Missing bearer auth returns `401 Missing API key`.
- A successfully decrypted token must start with the configured auth prefix.
- The suffix after the prefix becomes `request.state.username`.

## Secret Compatibility Contract
`genkey.py` currently accepts two Secret data formats when reading the auth key:

- Legacy raw 32-byte key material stored in Secret data
- Printable URL-safe base64 auth key text stored in Secret data

Observed from tests:
- `tests/test_genkey.py` verifies both formats are accepted.

Migration implication:
- Helm normalizes legacy raw-byte secrets and generated auth keys to URL-safe base64 text.
- The Go runtime intentionally accepts URL-safe base64 auth keys only.

## App Construction Contract
- `create_app(config_path)` returns an isolated FastAPI app with its own proxy and discovery state.
- The module-level `app` in `src/borg/main.py` is a singleton intended for ASGI servers.
- Tests should use `create_app()` instead of the singleton.

Migration implication:
- The Go service should support isolated test instances and avoid hidden global routing state.

## Known Drift And Quirks
- `config.example.yaml` previously used `auth_preifx`, which was a typo and not a runtime key. The example has been normalized to `auth_prefix`.
- Helm ConfigMap content and Python runtime auth loading are not fully aligned.
- `genkey.py` docstring still describes an older config-file-based flow, while the current CLI is Kubernetes-based.

## Go Layout Link
The target side-by-side Go project shape is documented in `docs/migration/go-project-layout.md`.

## Open Questions For Checkpoint 1
- Should `auth_key_from_env` become a real runtime key, or remain tooling/chart-only?
- Do we want an explicit characterization test for startup failure on malformed auth keys?
