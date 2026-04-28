# 🛰️ BORG — Kubernetes‑aware OpenAI Load‑Balancing Proxy

## NOTE If you couldn't tell from all the unicode icons, this is AI generated so may have errors. At some point I'll care and fix it.

> **BORG** turns a fleet of OpenAI‑compatible back‑ends (vLLM, openai‑proxy, FastAPI stubs, etc.) into **one** drop‑in `/v1` endpoint. It auto‑discovers pods in your cluster, fan‑outs requests across them, and exposes the union of all models.

![CI](https://img.shields.io/github/actions/workflow/status/undy-io/BORG/docker.yml?logo=github\&label=Build)
![License](https://img.shields.io/github/license/undy-io/BORG)

---

## Migration status

BORG is being migrated from Python to Go with a side-by-side strategy.

- The Python service in `src/borg/` remains the reference runtime and deployment fallback.
- Milestone 1 froze the Python contract in `docs/migration/`.
- Milestone 2 has a first Go core proxy implementation and Go Kubernetes discovery without changing production defaults.
- A Go `borg-genkey` replacement is available during migration as `bin/borg-genkey`.
- The devcontainer has Docker/KinD/kubectl/Helm tooling, but Docker-in-Docker KinD is blocked in the current rootless/containerized WSL environment by non-writable cpuset cgroups.
- Host/raw WSL KinD works when the node image is pinned to Kubernetes v1.34.3.
- Manual raw WSL KinD validation has proven the Go BORG service can discover an annotated dummy backend and serve `/v1/models` from the cluster.
- The planned Go layout is documented in `docs/migration/go-project-layout.md`.
- The Kubernetes-free local smoke/parity harness is implemented in `tests/smoke` and documented in `docs/migration/local-smoke-test-harness.md`.
- The fake Kubernetes API smoke harness for Go discovery is implemented in `tests/k8s_smoke` and documented in `docs/migration/go-k8s-smoke-test-harness.md`.

The Go binary is built as `bin/borg-go` during migration so it can run beside the Python `borg` CLI without ambiguity.

---

## ✨ Features

|                           |                                                                                    |
| ------------------------- | ---------------------------------------------------------------------------------- |
| **Zero‑config discovery** | Finds pods matching a label selector and registers their models automatically      |
| **Multi‑backend fan‑out** | Routes any `/v1/*` call to the next healthy backend and returns the first success  |
| **Model union**           | `GET /v1/models` merges all discovered models                                      |
| **Pluggable auth**        | Optional AES‑256 request signing (`auth_key`) and token prefix validation (`auth_prefix`) |
| **Lightweight**           | FastAPI + uvicorn on Python 3.12‑slim (< 40 MB image)                              |
| **Helm chart & CI**       | One‑line `helm upgrade` and GitHub Actions pipeline to GHCR                        |

---

## 🚀 Quick start

### 1 – Run locally with uv

```bash
git clone https://github.com/undy-io/BORG.git
cd BORG
uv sync --frozen
cp config.example.yaml config.yaml
uv run borg --reload
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

> Need a dummy backend? `helm install dummy-openai charts/dummy-openai` — Borg discovers it within seconds.

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

Key values

| Parameter          | Description                                   | Default                |
| ------------------ | --------------------------------------------- | ---------------------- |
| `image.repository` | Image to run                                  | `ghcr.io/undy-io/borg` |
| `ingress.enabled`  | Expose via Ingress‑NGINX                      | `false`                |
| `ingress.hosts`    | DNS names served                              | `[]`                   |
| `config`           | Inline proxy config (overrides `config.yaml`) | `{}`                   |

---

## 🖧 How discovery works

1. Each selector in `k8s_discover` is queried via the Kubernetes API.
2. For every **Running** pod, BORG builds an endpoint URL from the pod IP and annotations (`borg/apiport`, `borg/apibase`, `borg/protocol`).
3. If no model list is supplied, BORG calls the pod’s `/v1/models` to infer models.
4. New endpoints are registered; stale ones are evicted.

---

## 🧪 Testing

```bash
uv run pytest -q
uv run mypy src
uv run ruff check .
uv run ruff format --check .
go test ./...
go build -o bin/borg-go ./cmd/borg
go build -o bin/borg-genkey ./cmd/borg-genkey
uv run pytest -q tests/smoke
uv run pytest -q tests/k8s_smoke
```

On a host/runtime with usable Docker cgroups, validate KinD tooling with:

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

Unit tests live under `tests/`.
Go package tests live beside the Go packages under `internal/`.
The smoke suite in `tests/smoke` runs the Python and Go proxies side by side against local dummy upstreams.
The smoke suite in `tests/k8s_smoke` runs the Go proxy against a fake Kubernetes API and local dummy upstreams.

---

## Migration docs

| Document | Purpose |
| -------- | ------- |
| `ROADMAP.md` | High-level migration milestones |
| `MILESTONE.md` | Active milestone tasks and validation |
| `SESSION_RECOVERY.md` | Durable handoff state if chat history is lost |
| `docs/migration/python-runtime-contract.md` | Python CLI, config, env, and auth contract |
| `docs/migration/python-http-contract.md` | Python HTTP/proxy behavior contract |
| `docs/migration/python-ops-contract.md` | Python discovery, Helm, and runtime ops contract |
| `docs/migration/go-project-layout.md` | Target side-by-side Go project layout |
| `docs/migration/local-smoke-test-harness.md` | Local Python-vs-Go smoke/parity harness design |
| `docs/migration/go-k8s-smoke-test-harness.md` | Local fake Kubernetes API smoke harness for Go discovery |

---

## 📦 Release workflow

* Pushes and pull requests run Python CI from `.github/workflows/python.yml`.
* Pushes to **master** build `:edge` and `:sha-<short>` images from `.github/workflows/docker.yml`.
* Tagging `vX.Y.Z` also produces `:latest`, `:X.Y`, and `:X.Y.Z` tags.

---

## 🤝 Contributing

1. Fork & clone
2. `uv sync --frozen`
3. Make changes, add tests
4. Run `uv run pytest`, `uv run mypy src`, `uv run ruff check .`, and `uv run ruff format --check .`
5. PR against **master**

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
