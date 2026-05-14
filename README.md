# 🛰️ BORG — Kubernetes‑aware OpenAI Load‑Balancing Proxy

## NOTE If you couldn't tell from all the unicode icons, this is AI generated so may have errors. At some point I'll care and fix it.

> **BORG** turns a fleet of OpenAI‑compatible back‑ends (vLLM, openai‑proxy, FastAPI stubs, etc.) into **one** drop‑in `/v1` endpoint. It auto‑discovers pods in your cluster, fan‑outs requests across them, and exposes the union of all models.

![CI](https://img.shields.io/github/actions/workflow/status/undy-io/BORG/docker.yml?logo=github\&label=Build)
![License](https://img.shields.io/github/license/undy-io/BORG)

---

## Migration status

BORG now defaults to the Go runtime. The retired Python implementation has been removed from the active source tree.

- Milestone 1 froze the Python contract in `docs/migration/`.
- Milestone 2 added the Go core proxy, Kubernetes discovery, and Go `borg-genkey`.
- The cutover pass switched the root Docker image, Helm chart default image path, and CI/release validation to Go.
- The dedicated Python CI workflow, Python runtime, legacy `genkey.py`, and Python package build path have been removed.
- The fake Kubernetes API smoke harness for Go discovery is implemented in `tests/k8s_smoke` and documented in `docs/migration/go-k8s-smoke-test-harness.md`.
- The host/raw WSL KinD validation harness is implemented in `scripts/validate-kind-go.sh` and documented in `docs/migration/kind-go-validation-harness.md`.
- Docker-in-Docker KinD inside the devcontainer is blocked in the current rootless/containerized WSL environment by non-writable cpuset cgroups; run real KinD validation from raw WSL/host for now.
- The planned Go layout is documented in `docs/migration/go-project-layout.md`.

The production container exposes the Go service as `/usr/local/bin/borg`. During local smoke testing, build it as `bin/borg-go`.

---

## ✨ Features

|                           |                                                                                    |
| ------------------------- | ---------------------------------------------------------------------------------- |
| **Zero‑config discovery** | Finds pods matching a label selector and registers their models automatically      |
| **Multi‑backend fan‑out** | Routes any `/v1/*` call to the next healthy backend and returns the first success  |
| **Model union**           | `GET /v1/models` merges all discovered models                                      |
| **Pluggable auth**        | Optional AES‑256 request signing (`auth_key`) and token prefix validation (`auth_prefix`) |
| **Lightweight**           | Go `net/http` runtime with a small multi-stage container image                     |
| **Helm chart & CI**       | One‑line `helm upgrade` and GitHub Actions pipeline to GHCR                        |

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
  auth_prefix: "BORG:"             # plaintext prefix embedded in issued bearer tokens
  update_interval: 30               # seconds between K8s discovery passes

  # Static back‑ends
  instances:
    - endpoint: "http://10.0.0.5:8000"
      apikey: "sk-example"
      models: ["gpt-3.5-turbo"]

  # Dynamic discovery
  k8s_discover:
    - namespace: vllm-servers
      selector: borg/expose=vllm
      modelkey: borg/models          # pod annotation holding model list
```

The file can be mounted into the container or set via `PROXY_CONFIG` env‑var. See `config.example.yaml` for a template.

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

The publishing workflow runs from release tags like `v0.1.0`. It packages the
chart from `charts/borg/Chart.yaml`, generates `index.yaml`, and deploys the
static Helm repository through GitHub Pages Actions. When changing the chart for
a new Rancher-visible release, bump `version`, usually `appVersion`, and the
default image tag in `charts/borg/values.yaml` before creating the release tag.

One-time GitHub repository setup is required before the first release:
- Enable GitHub Pages with `GitHub Actions` as the build and deployment source.
- Ensure the `github-pages` environment protection rules allow release tags,
  for example `v*.*.*`.

Key values

| Parameter          | Description                                   | Default                |
| ------------------ | --------------------------------------------- | ---------------------- |
| `image.repository` | Image to run                                  | `ghcr.io/undy-io/borg` |
| `ingress.enabled`  | Expose via Ingress‑NGINX                      | `true`                 |
| `ingress.hosts`    | DNS names served                              | `[]`                   |
| `config`           | Inline proxy config (overrides `config.yaml`) | `{}`                   |

---

## 🔐 Token generation

Use the Go token utility for new installs:

```bash
mkdir -p bin
go build -o bin/borg-genkey ./cmd/borg-genkey
bin/borg-genkey <username> --namespace <namespace> --release <release>
```

## 🖧 How discovery works

1. Each selector in `k8s_discover` is queried via the Kubernetes API.
2. For every **Running** pod, BORG builds an endpoint URL from the pod IP and annotations (`borg/apiport`, `borg/apibase`, `borg/protocol`).
3. If no model list is supplied, BORG calls the pod’s `/v1/models` to infer models.
4. New endpoints are registered; stale ones are evicted.

---

## 🧪 Testing

Core local checks:

```bash
go test ./...
go vet ./...
go build ./cmd/borg
go build ./cmd/borg-genkey
bash -n scripts/validate-kind-go.sh
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

The harness uses this pinned Kubernetes node image by default because this WSL runtime reports cgroup v1:

```text
kindest/node:v1.34.3@sha256:08497ee19eace7b4b5348db5c6a1591d7752b164530a36f855cb0f2bdcbadd48
```

See `docs/migration/kind-go-validation-harness.md` for prerequisites, cleanup flags, and failure diagnostics.

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

## Migration docs

| Document | Purpose |
| -------- | ------- |
| `ROADMAP.md` | High-level migration milestones |
| `MILESTONE.md` | Active milestone tasks and validation |
| `SESSION_RECOVERY.md` | Durable handoff state if chat history is lost |
| `docs/migration/python-runtime-contract.md` | Historical Python CLI, config, env, and auth contract |
| `docs/migration/python-http-contract.md` | Historical Python HTTP/proxy behavior contract |
| `docs/migration/python-ops-contract.md` | Historical Python discovery, Helm, and runtime ops contract |
| `docs/migration/go-project-layout.md` | Go project layout |
| `docs/migration/go-k8s-smoke-test-harness.md` | Local fake Kubernetes API smoke harness for Go discovery |
| `docs/migration/kind-go-validation-harness.md` | Real KinD deployment validation for the Go runtime |

---

## 📦 Release workflow

* Pushes and pull requests run Go CI from `.github/workflows/go.yml`.
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
