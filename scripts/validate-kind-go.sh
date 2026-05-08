#!/usr/bin/env bash
set -Eeuo pipefail

CLUSTER_NAME="borg"
NODE_IMAGE="kindest/node:v1.34.3@sha256:08497ee19eace7b4b5348db5c6a1591d7752b164530a36f855cb0f2bdcbadd48"
LOCAL_PORT="18080"
CREATE_CLUSTER=0
DELETE_CLUSTER=0
CLEANUP_RESOURCES=0
CREATED_CLUSTER=0
DEBUG_READY=0

BORG_NAMESPACE="borg"
BORG_RELEASE="borg"
BORG_DEPLOYMENT="borg-borg"
BORG_SERVICE="borg-borg"
BORG_IMAGE="borg-go:kind"

DUMMY_NAMESPACE="vllm-services"
DUMMY_RELEASE="dummy-openai"
DUMMY_DEPLOYMENT="dummy-openai-dummy-openai"
DUMMY_IMAGE="dummy-openai:kind"

AUTH_KEY_VALUE="BwcHBwcHBwcHBwcHBwcHBwcHBwcHBwcHBwcHBwcHBwc="
AUTH_PREFIX="PROXY:"
MODEL_ID="gpt-3.5-turbo"

PORT_FORWARD_PID=""

usage() {
  cat <<EOF
Usage: scripts/validate-kind-go.sh [options]

Validate the Go BORG runtime against a host/WSL KinD cluster.

Options:
  --create-cluster       Create the KinD cluster if it does not exist.
  --delete-cluster       Delete the cluster on exit if this script created it.
  --cleanup-resources    Uninstall test Helm releases and delete test namespaces on exit.
  --cluster-name NAME    KinD cluster name. Default: ${CLUSTER_NAME}
  --node-image IMAGE     KinD node image. Default: pinned Kubernetes v1.34.3 image.
  --local-port PORT      Local port for BORG port-forward. Default: ${LOCAL_PORT}
  -h, --help             Show this help.
EOF
}

log() {
  printf '\n==> %s\n' "$*"
}

die() {
  printf 'ERROR: %s\n' "$*" >&2
  exit 1
}

require_command() {
  command -v "$1" >/dev/null 2>&1 || die "required command not found: $1"
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --create-cluster)
      CREATE_CLUSTER=1
      ;;
    --delete-cluster)
      DELETE_CLUSTER=1
      ;;
    --cleanup-resources)
      CLEANUP_RESOURCES=1
      ;;
    --cluster-name)
      [[ $# -ge 2 ]] || die "--cluster-name requires a value"
      CLUSTER_NAME="$2"
      shift
      ;;
    --node-image)
      [[ $# -ge 2 ]] || die "--node-image requires a value"
      NODE_IMAGE="$2"
      shift
      ;;
    --local-port)
      [[ $# -ge 2 ]] || die "--local-port requires a value"
      LOCAL_PORT="$2"
      shift
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      die "unknown option: $1"
      ;;
  esac
  shift
done

if [[ "$DELETE_CLUSTER" -eq 1 && "$CREATE_CLUSTER" -ne 1 ]]; then
  die "--delete-cluster is only allowed with --create-cluster"
fi

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
BUILD_DIR="${REPO_ROOT}/build/kind"
KUBE_CONTEXT="kind-${CLUSTER_NAME}"
KUBECONFIG_COPY="${BUILD_DIR}/kubeconfig"
PORT_FORWARD_LOG="${BUILD_DIR}/port-forward.log"

cleanup() {
  local status=$?
  set +e

  if [[ "$status" -ne 0 && "$DEBUG_READY" -eq 1 ]]; then
    print_debug
  fi

  if [[ -n "$PORT_FORWARD_PID" ]]; then
    kill "$PORT_FORWARD_PID" >/dev/null 2>&1
    wait "$PORT_FORWARD_PID" >/dev/null 2>&1
  fi

  if [[ "$CLEANUP_RESOURCES" -eq 1 ]]; then
    log "Cleaning up Helm releases and namespaces"
    helm --kube-context "$KUBE_CONTEXT" uninstall "$BORG_RELEASE" -n "$BORG_NAMESPACE" >/dev/null 2>&1
    helm --kube-context "$KUBE_CONTEXT" uninstall "$DUMMY_RELEASE" -n "$DUMMY_NAMESPACE" >/dev/null 2>&1
    kubectl --context "$KUBE_CONTEXT" delete namespace "$BORG_NAMESPACE" "$DUMMY_NAMESPACE" --ignore-not-found >/dev/null 2>&1
  fi

  if [[ "$DELETE_CLUSTER" -eq 1 && "$CREATED_CLUSTER" -eq 1 ]]; then
    log "Deleting KinD cluster ${CLUSTER_NAME}"
    kind delete cluster --name "$CLUSTER_NAME" >/dev/null 2>&1
  fi

  exit "$status"
}

print_debug() {
  {
    printf '\n--- KinD validation debug ---\n'
    printf 'cluster=%s context=%s\n' "$CLUSTER_NAME" "$KUBE_CONTEXT"
    printf '\n# BORG resources\n'
    kubectl --context "$KUBE_CONTEXT" -n "$BORG_NAMESPACE" get deploy,svc,pods -o wide
    printf '\n# Dummy resources\n'
    kubectl --context "$KUBE_CONTEXT" -n "$DUMMY_NAMESPACE" get deploy,pods -o wide
    printf '\n# BORG deployment describe\n'
    kubectl --context "$KUBE_CONTEXT" -n "$BORG_NAMESPACE" describe deploy "$BORG_DEPLOYMENT"
    printf '\n# BORG logs\n'
    kubectl --context "$KUBE_CONTEXT" -n "$BORG_NAMESPACE" logs "deploy/${BORG_DEPLOYMENT}" --tail=100
    printf '\n# Dummy logs\n'
    kubectl --context "$KUBE_CONTEXT" -n "$DUMMY_NAMESPACE" logs "deploy/${DUMMY_DEPLOYMENT}" --tail=100
    printf '\n# Port-forward log\n'
    if [[ -n "$PORT_FORWARD_PID" && -s "$PORT_FORWARD_LOG" ]]; then
      cat "$PORT_FORWARD_LOG"
    elif [[ -n "$PORT_FORWARD_PID" ]]; then
      printf '(port-forward started but did not write log output)\n'
    else
      printf '(port-forward was not started)\n'
    fi
    printf '\n--- end debug ---\n'
  } >&2
}

trap cleanup EXIT

cd "$REPO_ROOT"

for cmd in go docker kind kubectl helm curl; do
  require_command "$cmd"
done

mkdir -p "$BUILD_DIR"
: > "$PORT_FORWARD_LOG"

ensure_cluster() {
  if kind get clusters | grep -Fxq "$CLUSTER_NAME"; then
    log "Using existing KinD cluster ${CLUSTER_NAME}"
    return
  fi

  if [[ "$CREATE_CLUSTER" -ne 1 ]]; then
    die "KinD cluster ${CLUSTER_NAME} does not exist; rerun with --create-cluster"
  fi

  log "Creating KinD cluster ${CLUSTER_NAME}"
  kind create cluster \
    --name "$CLUSTER_NAME" \
    --config "${REPO_ROOT}/kind-config.yaml" \
    --image "$NODE_IMAGE"
  CREATED_CLUSTER=1
}

prepare_kubeconfig_copy() {
  kubectl config view --raw > "$KUBECONFIG_COPY"
  KUBECONFIG="$KUBECONFIG_COPY" kubectl config use-context "$KUBE_CONTEXT" >/dev/null
}

wait_for_cluster() {
  log "Waiting for cluster node readiness"
  kubectl --context "$KUBE_CONTEXT" wait \
    --for=condition=Ready \
    "node/${CLUSTER_NAME}-control-plane" \
    --timeout=120s
  DEBUG_READY=1
}

build_images() {
  log "Building Go BORG binaries"
  CGO_ENABLED=0 go build -o "${BUILD_DIR}/borg-go" ./cmd/borg
  CGO_ENABLED=0 go build -o "${BUILD_DIR}/borg-genkey" ./cmd/borg-genkey

  cat > "${BUILD_DIR}/borg-go.Dockerfile" <<'EOF'
FROM debian:trixie-slim
COPY borg-go /usr/local/bin/borg-go
EXPOSE 8000
CMD ["/usr/local/bin/borg-go"]
EOF

  log "Building local BORG image ${BORG_IMAGE}"
  docker build -t "$BORG_IMAGE" -f "${BUILD_DIR}/borg-go.Dockerfile" "$BUILD_DIR"

  log "Building dummy backend image ${DUMMY_IMAGE}"
  docker build -t "$DUMMY_IMAGE" "${REPO_ROOT}/dummy-openai"
}

load_image() {
  local image="$1"

  if kind load docker-image "$image" --name "$CLUSTER_NAME"; then
    return
  fi

  log "kind load failed for ${image}; falling back to direct containerd import"
  local node
  local imported=0
  while IFS= read -r node; do
    [[ -n "$node" ]] || continue
    imported=1
    log "Importing ${image} into ${node}"
    docker save "$image" | docker exec -i "$node" ctr -n k8s.io images import --all-platforms -
  done < <(kind get nodes --name "$CLUSTER_NAME")

  [[ "$imported" -eq 1 ]] || die "no KinD nodes found for cluster ${CLUSTER_NAME}"
}

load_images() {
  log "Loading images into KinD"
  load_image "$DUMMY_IMAGE"
  load_image "$BORG_IMAGE"
}

deploy_dummy() {
  log "Deploying dummy OpenAI backend"
  kubectl --context "$KUBE_CONTEXT" create namespace "$DUMMY_NAMESPACE" \
    --dry-run=client \
    -o yaml | kubectl --context "$KUBE_CONTEXT" apply -f -

  helm --kube-context "$KUBE_CONTEXT" upgrade --install "$DUMMY_RELEASE" \
    "${REPO_ROOT}/dummy-openai/charts/dummy-openai" \
    -n "$DUMMY_NAMESPACE" \
    --set "image.repository=dummy-openai" \
    --set "image.tag=kind" \
    --set "image.pullPolicy=IfNotPresent"

  kubectl --context "$KUBE_CONTEXT" -n "$DUMMY_NAMESPACE" rollout status \
    "deploy/${DUMMY_DEPLOYMENT}" \
    --timeout=120s
}

deploy_borg() {
  log "Deploying Go BORG"
  kubectl --context "$KUBE_CONTEXT" create namespace "$BORG_NAMESPACE" \
    --dry-run=client \
    -o yaml | kubectl --context "$KUBE_CONTEXT" apply -f -

  cat > "${BUILD_DIR}/borg-values.yaml" <<EOF
replicaCount: 1
image:
  repository: borg-go
  tag: kind
  pullPolicy: IfNotPresent
authKeySecret:
  name: borg-auth
  key: BORG_AUTH_KEY
  value: "${AUTH_KEY_VALUE}"
config:
  auth_prefix: "${AUTH_PREFIX}"
  update_interval: 2
  instances: []
  k8s_discover:
    - namespace: "${DUMMY_NAMESPACE}"
      selector: "borg/expose=vllm"
      modelkey: "borg/models"
ingress:
  enabled: false
EOF

  helm --kube-context "$KUBE_CONTEXT" upgrade --install "$BORG_RELEASE" \
    "${REPO_ROOT}/charts/borg" \
    -n "$BORG_NAMESPACE" \
    -f "${BUILD_DIR}/borg-values.yaml"

  kubectl --context "$KUBE_CONTEXT" -n "$BORG_NAMESPACE" rollout status \
    "deploy/${BORG_DEPLOYMENT}" \
    --timeout=120s
}

mint_token() {
  KUBECONFIG="$KUBECONFIG_COPY" "${BUILD_DIR}/borg-genkey" validator \
    --namespace "$BORG_NAMESPACE" \
    --release "$BORG_RELEASE" \
    --key-name BORG_AUTH_KEY \
    --auth-prefix "$AUTH_PREFIX"
}

start_port_forward() {
  log "Starting port-forward on 127.0.0.1:${LOCAL_PORT}"
  kubectl --context "$KUBE_CONTEXT" -n "$BORG_NAMESPACE" port-forward \
    "svc/${BORG_SERVICE}" \
    "${LOCAL_PORT}:80" \
    > "$PORT_FORWARD_LOG" 2>&1 &
  PORT_FORWARD_PID=$!
  wait_for_http "http://127.0.0.1:${LOCAL_PORT}/"
}

wait_for_http() {
  local url="$1"
  local deadline=$((SECONDS + 60))
  until curl -fsS --max-time 2 "$url" >/dev/null 2>&1; do
    if (( SECONDS >= deadline )); then
      die "timed out waiting for ${url}"
    fi
    sleep 1
  done
}

assert_contains() {
  local haystack="$1"
  local needle="$2"
  local description="$3"
  if [[ "$haystack" != *"$needle"* ]]; then
    printf 'Assertion failed: %s\nExpected to find: %s\nResponse:\n%s\n' \
      "$description" "$needle" "$haystack" >&2
    exit 1
  fi
}

validate_http() {
  local base_url="http://127.0.0.1:${LOCAL_PORT}"
  local token
  token="$(mint_token)"

  log "Validating root route"
  local root_response
  root_response="$(curl -fsS "${base_url}/")"
  assert_contains "$root_response" '"status":"ok"' "root route should report ok"

  log "Validating discovered model list"
  local models_response
  models_response="$(curl -fsS "${base_url}/v1/models")"
  assert_contains "$models_response" "\"id\":\"${MODEL_ID}\"" "models should include dummy backend model"

  log "Validating missing auth is rejected"
  local payload='{"model":"gpt-3.5-turbo","messages":[{"role":"user","content":"hello"}]}'
  local missing_status
  missing_status="$(curl -sS -o "${BUILD_DIR}/missing-auth.json" -w '%{http_code}' \
    -H "Content-Type: application/json" \
    --data "$payload" \
    "${base_url}/v1/chat/completions")"
  if [[ "$missing_status" != "401" ]]; then
    printf 'Expected missing auth status 401, got %s\nResponse:\n' "$missing_status" >&2
    cat "${BUILD_DIR}/missing-auth.json" >&2
    printf '\n' >&2
    exit 1
  fi

  log "Validating authenticated non-streaming forwarding"
  local forward_response
  forward_response="$(curl -fsS \
    -H "Authorization: Bearer ${token}" \
    -H "Content-Type: application/json" \
    --data "$payload" \
    "${base_url}/v1/chat/completions")"
  assert_contains "$forward_response" '"upstream":"dummy-openai"' "POST should reach dummy backend"
  assert_contains "$forward_response" '"auth":"Bearer EMPTY"' "BORG should rewrite upstream auth for discovered endpoints"
  assert_contains "$forward_response" "\"path\":\"/v1/chat/completions\"" "POST path should be preserved"

  log "Validating authenticated streaming forwarding"
  local stream_payload='{"model":"gpt-3.5-turbo","stream":true,"messages":[{"role":"user","content":"hello"}]}'
  local stream_response
  stream_response="$(curl -fsS --no-buffer \
    -H "Authorization: Bearer ${token}" \
    -H "Content-Type: application/json" \
    --data "$stream_payload" \
    "${base_url}/v1/chat/completions")"
  assert_contains "$stream_response" 'data: {"id":"dummy"' "stream should include SSE chunks"
  assert_contains "$stream_response" 'data: [DONE]' "stream should include DONE sentinel"
}

ensure_cluster
prepare_kubeconfig_copy
wait_for_cluster
build_images
load_images
deploy_dummy
deploy_borg
start_port_forward
validate_http

log "KinD Go validation passed"
