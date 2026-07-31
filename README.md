# 🛰️ BORG — Kubernetes‑aware OpenAI Load‑Balancing Proxy

## NOTE If you couldn't tell from all the unicode icons, this is AI generated so may have errors. At some point I'll care and fix it.

> **BORG** turns a fleet of OpenAI‑compatible back‑ends (vLLM, openai‑proxy, FastAPI stubs, etc.) into **one** drop‑in `/v1` endpoint. It auto‑discovers pods and Services in your cluster, fans requests across them, and exposes the union of all models.

![CI](https://img.shields.io/github/actions/workflow/status/undy-io/BORG/docker.yml?logo=github\&label=Build)
![License](https://img.shields.io/github/license/undy-io/BORG)

---

## Project status

BORG `v0.2.0` is the supported Go runtime. The Python-to-Go migration is
complete, and the retired Python implementation and tooling have been removed
from the active source tree.

- The root container image, Helm chart, CI, and release workflows target Go.
- Kubernetes discovery supports eligible Pods and stable Service front doors.
- Go CI enforces package and fake-Kubernetes tests, vet, pinned lint, and command
  builds. Helm CI enforces strict chart validation.
- The local quality baseline also includes a completed race-enabled test run.
- The host/raw WSL KinD acceptance path has passed discovery, auth, forwarding,
  SSE streaming, and config-only rollout validation.
- Docker-in-Docker KinD remains unavailable in this devcontainer because of its
  nested cgroup constraints; real-cluster validation runs from raw WSL/host.
- Current implementation details are documented in `docs/architecture.md`.

The production container exposes the Go service as `/usr/local/bin/borg`. During local smoke testing, build it as `bin/borg-go`.

---

## ✨ Features

|                           |                                                                                    |
| ------------------------- | ---------------------------------------------------------------------------------- |
| **Zero‑config discovery** | Finds pods or Services matching label selectors and registers their models automatically |
| **Multi‑backend fan‑out** | Routes any `/v1/*` call to the next healthy backend and returns the first success  |
| **Model union**           | `GET /v1/models` merges all discovered models                                      |
| **Pluggable auth**        | Optional AES‑256 request signing (`auth_key`) and token prefix validation (`auth_prefix`) |
| **Lightweight**           | Go `net/http` runtime with a small multi-stage container image                     |
| **Helm chart & CI**       | One‑line `helm upgrade` and GitHub Actions pipeline to GHCR                        |
| **Request event export**  | Filtered, ordered, privileged request streams with optional Kafka delivery         |

---

## 🚀 Quick start

### 1 – Run locally with Go

```bash
git clone https://github.com/undy-io/BORG.git
cd BORG
cp config.example.yaml config.yaml
mkdir -p bin
go build -o bin/borg-go ./cmd/borg
./bin/borg-go --config config.yaml
```

### 2 – Docker

```bash
# Build & start
docker build -t borg:dev .
docker run -p 8000:8000 -v $PWD/config.yaml:/app/config.yaml borg:dev
```

### 3 – KinD + Helm (offline loop)

```bash
kind create cluster --name borg-dev --config kind-config.yaml \
  --image kindest/node:v1.34.3@sha256:08497ee19eace7b4b5348db5c6a1591d7752b164530a36f855cb0f2bdcbadd48
helm repo add ingress-nginx https://kubernetes.github.io/ingress-nginx
helm install ingress ingress-nginx/ingress-nginx --create-namespace --namespace ingress-nginx

# load the image straight into KinD
docker build -t ghcr.io/undy-io/borg:dev .
kind load docker-image ghcr.io/undy-io/borg:dev --name borg-dev

helm upgrade --install borg charts/borg \
  --set image.repository=ghcr.io/undy-io/borg \
  --set image.tag=dev
```

> Need a dummy backend? `helm install dummy-openai dummy-openai/charts/dummy-openai` — BORG discovers it within seconds.

---

## ⚙️ Configuration

```yaml
# config.yaml
borg:
  auth_key: "EMPTY"                # base64‑url 32‑byte AES‑256 key
  auth_key_from_env: BORG_AUTH_KEY # optional env var containing auth_key
  auth_prefix: "BORG:"             # plaintext prefix embedded in issued bearer tokens
  update_interval: 30               # seconds between K8s discovery passes
  max_request_body_bytes: 67108864  # 64 MiB request body guard; 0 disables

  backend_health:
    enabled: true
    failure_threshold: 3
    cooldown_seconds: 30
    eject_on_500: false

  upstream:
    response_header_timeout_seconds: 300 # 0 disables the timeout

  request_logging:
    sink: noop # set kafka and configure filters/brokers to export events

  # Static back‑ends
  instances:
    - endpoint: "http://10.0.0.5:8000"
      apikey: "sk-example"
      models: ["gpt-3.5-turbo"]

  # Dynamic discovery
  k8s_discover:
    - id: vllm-pods
      namespace: vllm-servers
      selector: borg/expose=vllm
      modelkey: borg/models          # pod annotation holding model list

  k8s_service_discover:
    - id: qwen-router
      namespace: llm-d
      service_name: qwen-inference-scheduler
      port_name: http
      models: ["Qwen/Qwen3-32B"]
      # Or omit models and query the router's OpenAI model endpoint.
      automodel: true
      models_path: /v1/models
      apikeyEnv: LLMD_API_KEY
```

The file can be mounted into the container or set via `PROXY_CONFIG` env‑var. See `config.example.yaml` for a template.

Request logging defaults to `noop` and has no readiness dependency. Kafka mode
supports principal/model/header filters, optional request/response header events,
bounded body chunks, TLS, and PLAIN or SCRAM SASL. Captured events are privileged
and may contain credentials or personal data. See
[`docs/request-logging.md`](docs/request-logging.md) for the event contract,
sensitive-data guidance, delivery semantics, and consumer reconstruction procedure.

After BORG finishes sending an upstream request, it waits this long for the
upstream's final response headers to complete. A timeout counts against that
endpoint and returns `504`. A client may explicitly permit pre-commit failover
for that case with:

```http
Borg-Retry: response-header-timeout
```

BORG removes this header before forwarding the request upstream.

---

## 🛠️ Helm chart

```bash
helm show values charts/borg > my-values.yaml
# edit and deploy
helm upgrade --install borg charts/borg -f my-values.yaml
```

### Published Helm repository

Release builds can publish the chart as a GitHub Pages Helm repository for
Rancher or other catalog consumers:

```bash
helm repo add borg https://undy-io.github.io/BORG
helm repo update
helm upgrade --install borg borg/borg -n borg --create-namespace
```

In Rancher, add a chart repository with this URL:

```text
https://undy-io.github.io/BORG
```

The publishing workflow runs from release tags like `v0.2.0`. It packages the
chart from `charts/borg/Chart.yaml`, generates `index.yaml`, and deploys the
static Helm repository through GitHub Pages Actions. When changing the chart for
a new Rancher-visible release, bump `version`, usually `appVersion`, and the
default image tag in `charts/borg/values.yaml` before creating the release tag.

One-time GitHub repository setup is required before the first release:
- Enable GitHub Pages with `GitHub Actions` as the build and deployment source.
- Ensure the `github-pages` environment protection rules allow release tags,
  for example `v*.*.*`.

Key values

| Parameter            | Description                         | Default                |
| -------------------- | ----------------------------------- | ---------------------- |
| `image.repository`   | Image to run                        | `ghcr.io/undy-io/borg` |
| `service.type`       | Kubernetes Service type             | `ClusterIP`            |
| `ingress.enabled`    | Expose via Ingress                  | `false`                |
| `ingress.hosts`      | DNS names served by Ingress         | `borg.example.com`     |
| `certificate.enabled`| Create a cert-manager Certificate   | `false`                |
| `server.tls.enabled` | Serve HTTPS directly from TLS secret| `false`                |
| `authKeySecret.existingSecret` | Use externally managed auth Secret | `""`       |
| `authKeySecret.create` | Create the named auth Secret        | `true`               |
| `apikeySecrets.existingSecret` | Use externally managed backend API-key Secret | `""` |
| `apikeySecrets.create` | Create the backend API-key Secret   | `true`               |
| `rbac.clusterScoped` | Use cluster-wide discovery RBAC     | `true`                 |
| `rbac.namespaces`    | Namespaces for Role-based discovery | `[]`                   |
| `rbac.extraRules`    | Additional policy integration rules | `[]`                   |
| `serviceAccount.create` | Create BORG's ServiceAccount     | `true`                 |
| `podAnnotations`     | Pod-template annotations for integrations | `{}`             |
| `resources`          | Container resource requests/limits  | `{}`                   |
| `terminationGracePeriodSeconds` | Pod shutdown budget | `60`                    |
| `config.request_logging.capture.request_headers` | Emit inbound request headers | `false` |
| `config.request_logging.capture.response_headers` | Emit downstream response headers | `false` |
| `requestLoggingSecrets.kafkaCredentials.existingSecret` | Kafka SASL Secret | `""` |
| `requestLoggingSecrets.kafkaTLS.existingSecret` | Kafka TLS Secret | `""`     |
| `config`             | Inline proxy runtime config         | See `values.yaml`      |

For GitOps installs, set `authKeySecret.existingSecret` to a pre-created
Secret. That value takes precedence and the chart does not render an auth
Secret. With no existing Secret, `authKeySecret.create=true` preserves or
generates the named Secret; `create=false` only references it.

`authKeySecret.value` may be empty to preserve or generate a managed key,
`EMPTY` to disable inbound authentication, or padded/unpadded base64url text
that decodes to exactly 32 bytes. Externally managed auth Secrets use the same
printable text contract; raw 32-byte Secret payloads are rejected.

Backend API keys use the same modes through `apikeySecrets`. Static instances
and Service discovery sources name a Secret key with `apikeyEnv`. In generated
mode, provide `apikey` as the Secret input; BORG omits that value from its
ConfigMap. With `existingSecret`, or `create=false`, omit `apikey` and populate
the referenced Secret key externally.

The chart adds pod-template checksums for its ConfigMap, declared auth input,
and chart-managed backend credentials, so Helm upgrades roll pods when those
inputs change. External Secret contents are intentionally outside Helm state.
Use `podAnnotations` with the Secret reloader deployed in your cluster, or run
`kubectl rollout restart deployment/<release>-borg` after rotating an external
Secret. User `podAnnotations` cannot override BORG's internal checksum keys.

For direct Service exposure with Cilium LB IPAM, disable Ingress and set the
Service to `LoadBalancer`. Cilium pools can be selected by matching Service
labels from the pool's `serviceSelector`; a specific IP can be requested with
`lbipam.cilium.io/ips`.

```yaml
ingress:
  enabled: false

service:
  type: LoadBalancer
  labels:
    cilium.io/lb-pool: apps
  annotations:
    lbipam.cilium.io/ips: 192.0.2.50
  port: 80
  targetPort: 8000
```

To serve HTTPS directly from that LoadBalancer, enable native server TLS and
have cert-manager create the Secret. This is useful with custom issuers such as
EJBCA-backed `ClusterIssuer` resources.

```yaml
ingress:
  enabled: false

service:
  type: LoadBalancer
  labels:
    cilium.io/lb-pool: apps
  annotations:
    lbipam.cilium.io/ips: 192.0.2.50
  port: 443
  targetPort: 8000

certificate:
  enabled: true
  secretName: borg-tls
  commonName: borg.example.com
  dnsNames:
    - borg.example.com
  issuerRef:
    group: ejbca-issuer.keyfactor.com
    kind: ClusterIssuer
    name: clusterissuer-pkirules
  usages:
    - digital signature
    - key encipherment
  annotations: {}
  subject:
    organizations: []
    organizationalUnits: []
    countries: []
    localities: []
    provinces: []

server:
  tls:
    enabled: true
```

`server.tls.enabled=true` mounts the TLS Secret into the BORG pod and starts the
Go server with `TLS_CERT_FILE` and `TLS_KEY_FILE`. BORG reloads the mounted cert
and key after Kubernetes updates the Secret volume, so cert-manager renewals do
not require a pod restart. If you only want cert-manager to create a Secret for
another TLS terminator, leave `server.tls.enabled=false`.

---

## 🔐 Token generation

Use the Go token utility for new installs:

```bash
mkdir -p bin
go build -o bin/borg-genkey ./cmd/borg-genkey
bin/borg-genkey <username> --namespace <namespace> --release <release>
```

The utility discovers the chart's ConfigMap defaults and effective auth Secret
name. Use `--secret-name` to override the Secret name recorded in that ConfigMap.
The selected Secret field must contain printable padded or unpadded base64url
text that decodes to a 32-byte AES-256 key.

## 🖧 How discovery works

1. Each entry in `k8s_discover` and `k8s_service_discover` owns an independent endpoint snapshot. One failed source does not prevent the others from refreshing.
2. Eligible pods must be Running, Ready, non-deleting, and have a PodIP. Pod endpoints use that IP and default to port `8000`.
3. Service discovery registers stable `<name>.<namespace>.svc` front doors. Set exactly one of `service_name` or `selector`, plus `port` or `port_name` when the Service has multiple ports.
4. For llm-d, target the inference scheduler/router's HTTP Service. BORG forwards OpenAI requests to that Service and leaves endpoint selection to llm-d; it does not discover the Service's pods or implement the EPP protocol.
5. Service model precedence is explicit `models`, the configured `modelkey` annotation, then `models_path` enumeration when `automodel` is enabled. Enumeration uses the source's `apikeyEnv` or `apikey` credential.
6. `borg/protocol`, `borg/apiport`, and `borg/apibase` remain annotation fallbacks. Explicit Service source fields take precedence.
7. A failed Kubernetes list preserves only that source's previous snapshot. Per-endpoint model enumeration failures skip only that endpoint, while a successful empty refresh removes stale endpoints. BORG remains Ready when no backends are discovered.

Namespace-scoped RBAC requires an explicit `rbac.namespaces` list. Set
`rbac.extraRules` for deployment-specific policy integrations; runtime OPA
request authorization is not part of this release.

---

## 🧪 Testing

Core local checks:

```bash
go test ./...
go vet ./...
golangci-lint run ./...
go build ./cmd/borg
go build ./cmd/borg-genkey
bash -n scripts/validate-kind-go.sh
bash -n scripts/validate-kafka-logging.sh
```

Go fake Kubernetes smoke checks:

```bash
go test ./tests/k8s_smoke
```

On raw WSL/host, validate the Go BORG runtime against a real KinD cluster with:

```bash
scripts/validate-kind-go.sh
```

To create and delete the KinD cluster inside the validation run:

```bash
scripts/validate-kind-go.sh --create-cluster --delete-cluster
```

To include an in-cluster Kafka broker and validate Helm-configured header/body
event reconstruction:

```bash
scripts/validate-kind-go.sh --create-cluster --delete-cluster --with-kafka-logging
```

The harness uses this pinned Kubernetes node image by default because this WSL runtime reports cgroup v1:

```text
kindest/node:v1.34.3@sha256:08497ee19eace7b4b5348db5c6a1591d7752b164530a36f855cb0f2bdcbadd48
```

See `docs/testing/kind-validation.md` for prerequisites, cleanup flags, and
failure diagnostics.

For manual KinD toolchain checks:

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

Go package tests live beside the Go packages under `internal/`.
The Go smoke suite in `tests/k8s_smoke` runs the real Go proxy against a fake Kubernetes API and local dummy upstreams.
The `dummy-openai/` Go app remains as a lightweight test backend for local and KinD validation.

---

## Documentation

### Active documentation

| Document | Purpose |
| -------- | ------- |
| `MILESTONE.md` | Active request-event logging and Kafka export milestone |
| `docs/architecture.md` | Current Go runtime and deployment architecture |
| `docs/testing/fake-kubernetes-smoke.md` | Local process-level Pod and Service discovery validation |
| `docs/testing/kind-validation.md` | Real KinD deployment and rollout validation |

### Migration history

| Document | Purpose |
| -------- | ------- |
| `docs/migration/go-migration-roadmap.md` | Completed Python-to-Go migration roadmap |
| `docs/migration/milestone-6-finalization.md` | Completed cleanup milestone and acceptance evidence |
| `docs/migration/python-runtime-contract.md` | Historical Python CLI, config, env, and auth contract |
| `docs/migration/python-http-contract.md` | Historical Python HTTP/proxy behavior contract |
| `docs/migration/python-ops-contract.md` | Historical Python discovery, Helm, and runtime ops contract |

---

## 📦 Release workflow

* Pushes and pull requests run Go CI from `.github/workflows/go.yml`.
* Relevant runtime and chart changes run KinD/Kafka acceptance from `.github/workflows/integration.yml`.
* Pushes to **master** build Go runtime `:edge` and `:sha-<short>` images from `.github/workflows/docker.yml`.
* Tagging `vX.Y.Z` also produces `:latest`, `:X.Y`, and `:X.Y.Z` tags.

---

## 🤝 Contributing

1. Fork & clone
2. Make changes, add tests
3. Run `go test ./...` and `go vet ./...`; when touching smoke harness code, also run `go test ./tests/k8s_smoke`
4. PR against **master**

---

## 📄 License

MIT — see `LICENSE` for details.

Appendix – dev cheat‑sheet
```bash
kind create cluster --name borg-dev --config kind-config.yaml
#we need cert manager
helm install cert-manager jetstack/cert-manager \
    --namespace cert-manager \
    --create-namespace \
    --version v1.18.2 \
    --set crds.enabled=true

podman build -t ghcr.io/undy-io/borg:dev .
rm -f borg.tar
podman save --format docker-archive -o borg.tar ghcr.io/undy-io/borg:dev
kind load image-archive borg.tar --name borg-dev
helm uninstall borg
helm upgrade --install borg charts/borg --set image.repository=ghcr.io/undy-io/borg --set image.tag=dev
kubectl logs -f deployment/borg-borg
# Start dummy if needed
```

---

© 2025 Michael C. McMinn • Contributions welcome!
