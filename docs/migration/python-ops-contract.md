# Python Ops Contract

## Purpose
This document freezes the intended Python discovery, deployment, and operational contract that the Go implementation should match during the migration.

Where current Python behavior diverges from the intended contract, this document treats the Python behavior as a bug to normalize before or during the Go port, not as parity to preserve.

## Sources
- `src/borg/main.py`
- `src/borg/k8s_discovery.py`
- `charts/borg/templates/deployment.yaml`
- `charts/borg/templates/config.yaml`
- `charts/borg/templates/secret.yaml`
- `charts/borg/templates/serviceaccount.yaml`
- `charts/borg/templates/clusterrole.yaml`
- `charts/borg/templates/clusterrolebinding.yaml`
- `charts/borg/templates/service.yaml`
- `charts/borg/values.yaml`
- `charts/borg/values.schema.json`
- `tests/test_discovery.py`
- `tests/test_proxy_router.py`
- `README.md`

## Discovery Configuration Contract

### Background discovery gating
- Background discovery is enabled only when `borg.update_interval > 0`.
- Background discovery is started only when at least one discovery service initializes successfully.
- If discovery service initialization fails, the app continues to run with static configuration and logs `Failed to load k8s discovery service`.

Coverage:
- Code-backed by `src/borg/main.py`
- Backed by `tests/test_proxy_router.py::test_discovery_init_failure_is_logged_once`

### `k8s_discover` selector shape
`borg.k8s_discover` is a list of selector objects. Each selector may contain:

- `namespace`
  - default: `default`
- `selector`
  - default: empty string
- `modelkey`
  - optional annotation key that holds a comma-separated model list

Contract notes:
- Selectors are deployment policy, not automatic “discover everything” behavior.
- Deployments are expected to use selectors that intentionally mark pods for export to this BORG deployment, commonly with annotation-based selectors such as `borg/expose=default`.
- Go should not broaden discovery to all matching pods without that explicit export policy.

Coverage:
- Code-backed by `src/borg/k8s_discovery.py`
- Backed by `tests/test_discovery.py::test_discover_main_method`

## Pod Eligibility And Endpoint Synthesis

### Eligibility
A pod is eligible for discovery only when all of the following are true:

- it matches the configured selector
- it is in `Running` phase
- it is intentionally exportable for this BORG deployment via selector/annotation policy
- it yields a non-empty model list, either from annotations or automodel lookup

Operational expectation:
- The deployment should be annotation-driven.
- Pods that are not explicitly marked for this BORG deployment should not be exported.

### Endpoint construction
For eligible pods, the endpoint is synthesized from pod IP plus optional annotation overrides:

- protocol annotation: `borg/protocol`
  - default: `http`
- base path annotation: `borg/apibase`
  - default: empty string
- port annotation: `borg/apiport`
  - default: `8000`

Resulting endpoint shape:
- `protocol://pod_ip:apiport + apibase`

Coverage:
- Code-backed by `src/borg/k8s_discovery.py`
- Backed by `tests/test_discovery.py::test_discover_running_pods`

## Model Resolution Contract

### Annotation-driven model export
- If a selector provides `modelkey`, discovery reads that annotation from the pod.
- The annotation value is parsed as a comma-separated list.
- Empty entries are filtered out.

### Automodel fallback
- If no models are resolved and `automodel` is enabled, discovery queries `<endpoint>/v1/models`.
- Automodel uses an OpenAI-compatible `GET /v1/models` request with bearer auth and JSON response parsing.
- If automodel still yields no models, the pod is skipped for that discovery pass.

Coverage:
- Backed by `tests/test_discovery.py::test_discover_with_automodel`
- Backed by `tests/test_discovery.py::test_discover_no_models`
- Backed by `tests/test_discovery.py::test_enum_models_success`

Current limitation:
- Discovery currently assumes upstream model enumeration can be done with `Bearer EMPTY`.
- There is no discovery-level per-endpoint upstream API key contract today.
- Go should preserve that limitation only if no explicit discovery auth design is added.

## Refresh And Reconciliation Contract

### Intended refresh behavior
- Each successful refresh pass produces an authoritative snapshot of discovered endpoints grouped by model.
- Endpoints missing from that authoritative snapshot are removed from the corresponding model groups.
- Newly discovered endpoints are added to the corresponding model groups.
- The latest successful snapshot becomes the baseline for the next diff.

### Eviction rules
- Discovery-based eviction is allowed only when a refresh pass returns an authoritative set that omits an endpoint.
- Transient discovery failures must not be treated as an authoritative empty set.
- Endpoint health-check failure may be an additional eviction signal when a health-check subsystem exists.

Current intended contract for the migration:
- authoritative discovery omission is part of the current contract
- endpoint health-check eviction is conceptually allowed, but no endpoint health-check subsystem exists in current Python

### Failure handling
- Kubernetes API or model-enumeration failures during a pass should be logged.
- Failed discovery should preserve the last successful discovered snapshot instead of evicting healthy endpoints by mistake.

Coverage:
- Happy path and config fallback behavior are covered in `tests/test_discovery.py`
- Initialization failure handling is covered in `tests/test_proxy_router.py::test_discovery_init_failure_is_logged_once`
- Authoritative reconciliation is covered by `tests/test_discovery.py::test_update_applies_authoritative_snapshot`
- Failed-pass snapshot preservation is covered by `tests/test_discovery.py::test_update_preserves_snapshot_on_discovery_failure`

## Deployment And Runtime Contract

### Container runtime wiring
The Helm chart currently deploys BORG with:

- `PORT` set to `.Values.service.targetPort`
- `PROXY_CONFIG=/app/config.yaml`
- `AUTH_KEY` loaded from the auth Secret
- one env var per configured `apikeyEnv`, loaded from the API-key Secret
- `/app/config.yaml` mounted from the ConfigMap

Health and serving shape:
- liveness probe: `GET /`
- readiness probe: `GET /`
- container listens on `service.targetPort`
- Service is `ClusterIP`
- Service port maps `.Values.service.port` to `.Values.service.targetPort`

Coverage:
- Code-backed by Helm templates in `charts/borg/templates`

### ConfigMap contract
The chart writes these runtime-facing config fields into `config.yaml`:

- `auth_key_from_env`
- `auth_prefix`
- `update_interval`
- `instances`
- `k8s_discover`

Important drift:
- Python runtime does not read `auth_key_from_env`.
- Runtime auth actually comes from env var precedence (`AUTH_KEY`, then `BORG_AUTH_KEY`, then config `auth_key`).

Go guidance:
- Do not treat `auth_key_from_env` as part of the required runtime contract unless we intentionally add runtime support for it.
- Treat it as chart/tooling drift, not parity to preserve.

### Secret contract
The auth secret template supports three behaviors:

- reuse existing secret value on upgrade
- migrate legacy raw 32-byte secret content into text-safe form
- accept operator-supplied printable auth key text

The API-key secret template:
- creates stringData entries only for configured instances that declare `apikeyEnv`
- stores the instance `apikey`, defaulting to `EMPTY`

Operational contract:
- The auth secret should continue to support both migrated legacy raw-byte content and printable auth-key strings because runtime tooling already accepts both.

### RBAC contract
The chart provisions:

- a ServiceAccount for the BORG deployment
- a ClusterRole with `get`, `list`, and `watch` on pods
- a ClusterRoleBinding attaching that ClusterRole to the ServiceAccount

Operational implication:
- Discovery is intended to work across whichever namespaces appear in `k8s_discover`, so cluster-scoped pod-read RBAC is part of the current deployable contract.

## Known Drift And Normalize-Before-Go Items
- Python discovery currently skips pods with no annotations because endpoint/model extraction is annotation-driven. That aligns with the intended export policy and should stay annotation-driven in Go.
- Helm writes `auth_key_from_env` into config, but runtime ignores it. This is drift, not contract.
- Endpoint health-check eviction is not implemented in Python today and should not be inferred from current code behavior.
- `config.example.yaml` previously used `auth_preifx`; the example has been normalized to `auth_prefix`.

## Checkpoint 3 Outcome
For Milestone 1, the Go rewrite should treat the following as the intended ops contract:

- discovery is selector-driven and annotation-governed
- only successful authoritative discovery passes may evict endpoints
- transient discovery failures preserve the last successful discovered set
- deployment wiring continues to use `PORT`, `PROXY_CONFIG`, `AUTH_KEY`, mounted config, and per-instance `apikeyEnv`
- RBAC continues to allow pod discovery across configured namespaces

## Go Layout Link
The planned Go package boundaries for app wiring, config, proxying, and Kubernetes discovery are documented in `docs/migration/go-project-layout.md`.
