#!/usr/bin/env bash
set -euo pipefail

chart_dir="${1:-charts/borg}"
work_dir="$(mktemp -d)"
trap 'rm -rf "$work_dir"' EXIT

render() {
  local name="$1"
  shift
  helm template "$name" "$chart_dir" "$@" | sed 's/\r$//' > "${work_dir}/${name}.yaml"
}

assert_contains() {
  local file="$1"
  local pattern="$2"
  if ! grep -Eq "$pattern" "$file"; then
    echo "Expected ${file} to contain pattern: ${pattern}" >&2
    exit 1
  fi
}

assert_not_contains() {
  local file="$1"
  local pattern="$2"
  if grep -Eq "$pattern" "$file"; then
    echo "Expected ${file} not to contain pattern: ${pattern}" >&2
    exit 1
  fi
}

assert_count() {
  local file="$1"
  local pattern="$2"
  local expected="$3"
  local actual
  actual="$(grep -Ec "$pattern" "$file" || true)"
  if [[ "$actual" -ne "$expected" ]]; then
    echo "Expected ${file} to contain ${expected} matches for pattern ${pattern}, got ${actual}" >&2
    exit 1
  fi
}

annotation_value() {
  local file="$1"
  local annotation="$2"
  awk -v annotation="$annotation" '$1 == annotation ":" { print $2; exit }' "$file"
}

assert_annotation_same() {
  local first_file="$1"
  local second_file="$2"
  local annotation="$3"
  local first_value second_value
  first_value="$(annotation_value "$first_file" "$annotation")"
  second_value="$(annotation_value "$second_file" "$annotation")"
  if [[ -z "$first_value" || "$first_value" != "$second_value" ]]; then
    echo "Expected ${annotation} to remain stable, got ${first_value:-<missing>} and ${second_value:-<missing>}" >&2
    exit 1
  fi
}

assert_annotation_changed() {
  local first_file="$1"
  local second_file="$2"
  local annotation="$3"
  local first_value second_value
  first_value="$(annotation_value "$first_file" "$annotation")"
  second_value="$(annotation_value "$second_file" "$annotation")"
  if [[ -z "$first_value" || -z "$second_value" || "$first_value" == "$second_value" ]]; then
    echo "Expected ${annotation} to change, got ${first_value:-<missing>} and ${second_value:-<missing>}" >&2
    exit 1
  fi
}

helm lint --strict "$chart_dir"

render borg-default
assert_not_contains "${work_dir}/borg-default.yaml" '^kind: Ingress$'
assert_not_contains "${work_dir}/borg-default.yaml" '^kind: Certificate$'
assert_count "${work_dir}/borg-default.yaml" '^        checksum\.borg\.undy\.io/(auth|backend-credentials|config): [a-f0-9]{64}$' 3
assert_not_contains "${work_dir}/borg-default.yaml" '^stringData:$'
assert_contains "${work_dir}/borg-default.yaml" '^kind: ClusterRole$'
assert_contains "${work_dir}/borg-default.yaml" '^kind: ClusterRoleBinding$'
assert_contains "${work_dir}/borg-default.yaml" 'resources: \["pods"\]'
assert_contains "${work_dir}/borg-default.yaml" 'verbs: \["get", "list"\]'
assert_not_contains "${work_dir}/borg-default.yaml" '"watch"'
assert_contains "${work_dir}/borg-default.yaml" '^      instances:$'
assert_contains "${work_dir}/borg-default.yaml" '^        \[\]$'
assert_contains "${work_dir}/borg-default.yaml" '^      max_request_body_bytes: 67108864$'
assert_contains "${work_dir}/borg-default.yaml" '^        response_header_timeout_seconds: 300$'
assert_contains "${work_dir}/borg-default.yaml" '^      k8s_service_discover:$'

if helm template borg-negative-request-limit "$chart_dir" \
  --set config.max_request_body_bytes=-1 \
  > "${work_dir}/borg-negative-request-limit.yaml" 2> "${work_dir}/borg-negative-request-limit.err"; then
  echo "Expected a negative max_request_body_bytes value to fail schema validation" >&2
  exit 1
fi
assert_contains "${work_dir}/borg-negative-request-limit.err" 'max_request_body_bytes'

render borg-checksum-stability
cp "${work_dir}/borg-checksum-stability.yaml" "${work_dir}/borg-checksum-stability-first.yaml"
render borg-checksum-stability
assert_annotation_same "${work_dir}/borg-checksum-stability-first.yaml" "${work_dir}/borg-checksum-stability.yaml" 'checksum.borg.undy.io/config'
assert_annotation_same "${work_dir}/borg-checksum-stability-first.yaml" "${work_dir}/borg-checksum-stability.yaml" 'checksum.borg.undy.io/auth'
assert_annotation_same "${work_dir}/borg-checksum-stability-first.yaml" "${work_dir}/borg-checksum-stability.yaml" 'checksum.borg.undy.io/backend-credentials'

render borg-config-checksum
cp "${work_dir}/borg-config-checksum.yaml" "${work_dir}/borg-config-checksum-first.yaml"
render borg-config-checksum \
  --set config.upstream.response_header_timeout_seconds=301
assert_annotation_changed "${work_dir}/borg-config-checksum-first.yaml" "${work_dir}/borg-config-checksum.yaml" 'checksum.borg.undy.io/config'
assert_annotation_same "${work_dir}/borg-config-checksum-first.yaml" "${work_dir}/borg-config-checksum.yaml" 'checksum.borg.undy.io/auth'
assert_annotation_same "${work_dir}/borg-config-checksum-first.yaml" "${work_dir}/borg-config-checksum.yaml" 'checksum.borg.undy.io/backend-credentials'

render borg-auth-checksum \
  --set-string authKeySecret.value=BwcHBwcHBwcHBwcHBwcHBwcHBwcHBwcHBwcHBwcHBwc=
cp "${work_dir}/borg-auth-checksum.yaml" "${work_dir}/borg-auth-checksum-first.yaml"
render borg-auth-checksum \
  --set-string authKeySecret.value=CQkJCQkJCQkJCQkJCQkJCQkJCQkJCQkJCQkJCQkJCQk=
assert_annotation_same "${work_dir}/borg-auth-checksum-first.yaml" "${work_dir}/borg-auth-checksum.yaml" 'checksum.borg.undy.io/config'
assert_annotation_changed "${work_dir}/borg-auth-checksum-first.yaml" "${work_dir}/borg-auth-checksum.yaml" 'checksum.borg.undy.io/auth'
assert_annotation_same "${work_dir}/borg-auth-checksum-first.yaml" "${work_dir}/borg-auth-checksum.yaml" 'checksum.borg.undy.io/backend-credentials'

render borg-backend-checksum \
  --set config.instances[0].endpoint=http://model-api:8000 \
  --set config.instances[0].models[0]=model \
  --set config.instances[0].apikeyEnv=MODEL_API_KEY \
  --set-string config.instances[0].apikey=sk-one
cp "${work_dir}/borg-backend-checksum.yaml" "${work_dir}/borg-backend-checksum-first.yaml"
render borg-backend-checksum \
  --set config.instances[0].endpoint=http://model-api:8000 \
  --set config.instances[0].models[0]=model \
  --set config.instances[0].apikeyEnv=MODEL_API_KEY \
  --set-string config.instances[0].apikey=sk-two
assert_annotation_same "${work_dir}/borg-backend-checksum-first.yaml" "${work_dir}/borg-backend-checksum.yaml" 'checksum.borg.undy.io/config'
assert_annotation_same "${work_dir}/borg-backend-checksum-first.yaml" "${work_dir}/borg-backend-checksum.yaml" 'checksum.borg.undy.io/auth'
assert_annotation_changed "${work_dir}/borg-backend-checksum-first.yaml" "${work_dir}/borg-backend-checksum.yaml" 'checksum.borg.undy.io/backend-credentials'

render borg-pod-annotations \
  --set-string 'podAnnotations.example\.com/reloader=enabled' \
  --set-string 'podAnnotations.checksum\.borg\.undy\.io/config=user-value'
assert_contains "${work_dir}/borg-pod-annotations.yaml" '^        example.com/reloader: enabled$'
assert_not_contains "${work_dir}/borg-pod-annotations.yaml" 'checksum\.borg\.undy\.io/config: user-value'
assert_contains "${work_dir}/borg-pod-annotations.yaml" '^        checksum\.borg\.undy\.io/config: [a-f0-9]{64}$'

render borg-ingress \
  --api-versions cert-manager.io/v1 \
  --set ingress.enabled=true
assert_contains "${work_dir}/borg-ingress.yaml" '^kind: Ingress$'
assert_contains "${work_dir}/borg-ingress.yaml" '^kind: Certificate$'
assert_contains "${work_dir}/borg-ingress.yaml" '^    nginx.ingress.kubernetes.io/force-ssl-redirect: "true"$'
assert_contains "${work_dir}/borg-ingress.yaml" '^  tls:$'
assert_contains "${work_dir}/borg-ingress.yaml" '^    - secretName: borg-ingress-tls$'

render borg-ingress-annotation-precedence \
  --set ingress.enabled=true \
  --set-string 'ingress.issuer.annotations.example\.com/value=legacy' \
  --set-string 'ingress.annotations.example\.com/value=current'
assert_contains "${work_dir}/borg-ingress-annotation-precedence.yaml" '^    example.com/value: current$'
assert_not_contains "${work_dir}/borg-ingress-annotation-precedence.yaml" 'example.com/value: legacy'

render borg-existing-secret \
  --api-versions cert-manager.io/v1 \
  --set ingress.enabled=true \
  --set ingress.tls.existingSecret=my-existing-tls
assert_contains "${work_dir}/borg-existing-secret.yaml" '^kind: Ingress$'
assert_not_contains "${work_dir}/borg-existing-secret.yaml" '^kind: Certificate$'
assert_contains "${work_dir}/borg-existing-secret.yaml" '^    - secretName: my-existing-tls$'

render borg-existing-auth-secret \
  --set-string authKeySecret.name= \
  --set authKeySecret.existingSecret=my-existing-auth
assert_not_contains "${work_dir}/borg-existing-auth-secret.yaml" '^kind: Secret$'
assert_contains "${work_dir}/borg-existing-auth-secret.yaml" '^                  name: my-existing-auth$'
assert_contains "${work_dir}/borg-existing-auth-secret.yaml" '^  auth-secret-name: "my-existing-auth"$'

if helm template borg-empty-auth-secret-name "$chart_dir" \
  --set-string authKeySecret.name= \
  > "${work_dir}/borg-empty-auth-secret-name.yaml" 2> "${work_dir}/borg-empty-auth-secret-name.err"; then
  echo "Expected empty authKeySecret.name without an existing Secret to fail validation" >&2
  exit 1
fi
assert_contains "${work_dir}/borg-empty-auth-secret-name.err" 'authKeySecret'

render borg-explicit-auth-secret \
  --set-string authKeySecret.value=BwcHBwcHBwcHBwcHBwcHBwcHBwcHBwcHBwcHBwcHBwc=
assert_contains "${work_dir}/borg-explicit-auth-secret.yaml" '^  "BORG_AUTH_KEY": [A-Za-z0-9+/=]+$'

render borg-unpadded-auth-secret \
  --set-string authKeySecret.value=BwcHBwcHBwcHBwcHBwcHBwcHBwcHBwcHBwcHBwcHBwc
assert_contains "${work_dir}/borg-unpadded-auth-secret.yaml" '^  "BORG_AUTH_KEY": [A-Za-z0-9+/=]+$'

render borg-empty-auth-secret \
  --set-string authKeySecret.value=EMPTY
assert_contains "${work_dir}/borg-empty-auth-secret.yaml" '^  "BORG_AUTH_KEY": RU1QVFk=$'

if helm template borg-invalid-auth-short "$chart_dir" \
  --set-string authKeySecret.value=test-auth-key \
  > "${work_dir}/borg-invalid-auth-short.yaml" 2> "${work_dir}/borg-invalid-auth-short.err"; then
  echo "Expected a short authKeySecret.value to fail validation" >&2
  exit 1
fi
assert_contains "${work_dir}/borg-invalid-auth-short.err" 'authKeySecret.value'

if helm template borg-invalid-auth-alphabet "$chart_dir" \
  --set-string authKeySecret.value='BwcHBwcHBwcHBwcHBwcHBwcHBwcHBwcHBwcHBwcHBwc+' \
  > "${work_dir}/borg-invalid-auth-alphabet.yaml" 2> "${work_dir}/borg-invalid-auth-alphabet.err"; then
  echo "Expected a non-base64url authKeySecret.value to fail validation" >&2
  exit 1
fi
assert_contains "${work_dir}/borg-invalid-auth-alphabet.err" 'authKeySecret.value'

if helm template borg-invalid-auth-env "$chart_dir" \
  --set-string authKeySecret.key=9AUTH_KEY \
  > "${work_dir}/borg-invalid-auth-env.yaml" 2> "${work_dir}/borg-invalid-auth-env.err"; then
  echo "Expected a non-portable authKeySecret.key to fail validation" >&2
  exit 1
fi
assert_contains "${work_dir}/borg-invalid-auth-env.err" 'authKeySecret.key'

if helm template borg-reserved-auth-env "$chart_dir" \
  --set-string authKeySecret.key=PORT \
  > "${work_dir}/borg-reserved-auth-env.yaml" 2> "${work_dir}/borg-reserved-auth-env.err"; then
  echo "Expected a reserved authKeySecret.key to fail validation" >&2
  exit 1
fi
assert_contains "${work_dir}/borg-reserved-auth-env.err" 'reserved by BORG'

render borg-auth-create-disabled \
  --set authKeySecret.create=false
assert_not_contains "${work_dir}/borg-auth-create-disabled.yaml" '^kind: Secret$'
assert_contains "${work_dir}/borg-auth-create-disabled.yaml" '^                  name: borg-auth$'

render borg-namespace-rbac \
  --set rbac.clusterScoped=false \
  --set rbac.namespaces[0]=vllm-services \
  --set serviceAccount.name=custom-sa
assert_contains "${work_dir}/borg-namespace-rbac.yaml" '^kind: Role$'
assert_contains "${work_dir}/borg-namespace-rbac.yaml" '^kind: RoleBinding$'
assert_not_contains "${work_dir}/borg-namespace-rbac.yaml" '^kind: ClusterRole$'
assert_contains "${work_dir}/borg-namespace-rbac.yaml" '^  namespace: vllm-services$'
assert_contains "${work_dir}/borg-namespace-rbac.yaml" '^      serviceAccountName: custom-sa$'
assert_contains "${work_dir}/borg-namespace-rbac.yaml" '^    name: custom-sa$'

render borg-existing-service-account \
  --set serviceAccount.create=false \
  --set serviceAccount.name=external-sa
assert_not_contains "${work_dir}/borg-existing-service-account.yaml" '^kind: ServiceAccount$'
assert_contains "${work_dir}/borg-existing-service-account.yaml" '^      serviceAccountName: external-sa$'
assert_contains "${work_dir}/borg-existing-service-account.yaml" '^    name: external-sa$'

render borg-default-service-account \
  --set serviceAccount.create=false
assert_not_contains "${work_dir}/borg-default-service-account.yaml" '^kind: ServiceAccount$'
assert_contains "${work_dir}/borg-default-service-account.yaml" '^      serviceAccountName: default$'

render borg-service-discovery \
  --set config.k8s_service_discover[0].id=llmd-router \
  --set config.k8s_service_discover[0].namespace=models \
  --set config.k8s_service_discover[0].service_name=qwen-inference-scheduler \
  --set config.k8s_service_discover[0].port_name=http \
  --set config.k8s_service_discover[0].models[0]=Qwen/Qwen3-32B
assert_contains "${work_dir}/borg-service-discovery.yaml" 'resources: \["pods","services"\]'
assert_contains "${work_dir}/borg-service-discovery.yaml" '^      k8s_service_discover:$'
assert_contains "${work_dir}/borg-service-discovery.yaml" '^        - id: llmd-router$'
assert_contains "${work_dir}/borg-service-discovery.yaml" '^          port_name: http$'

render borg-service-only-discovery \
  --set-json 'config.k8s_discover=[]' \
  --set config.k8s_service_discover[0].id=llmd-router \
  --set config.k8s_service_discover[0].namespace=models \
  --set config.k8s_service_discover[0].service_name=qwen-inference-scheduler \
  --set config.k8s_service_discover[0].port_name=http \
  --set config.k8s_service_discover[0].models[0]=Qwen/Qwen3-32B
assert_contains "${work_dir}/borg-service-only-discovery.yaml" 'resources: \["services"\]'
assert_not_contains "${work_dir}/borg-service-only-discovery.yaml" 'resources: \["pods"'

render borg-generated-api-keys \
  --set config.instances[0].endpoint=http://model-api:8000 \
  --set config.instances[0].models[0]=shared-model \
  --set config.instances[0].apikeyEnv=SHARED_API_KEY \
  --set-string config.instances[0].apikey=sk-shared \
  --set config.k8s_service_discover[0].id=llmd-router \
  --set config.k8s_service_discover[0].service_name=qwen-inference-scheduler \
  --set config.k8s_service_discover[0].port_name=http \
  --set config.k8s_service_discover[0].models[0]=Qwen/Qwen3-32B \
  --set config.k8s_service_discover[0].apikeyEnv=SHARED_API_KEY \
  --set-string config.k8s_service_discover[0].apikey=sk-shared
assert_contains "${work_dir}/borg-generated-api-keys.yaml" '^  name: borg-apikeys$'
assert_contains "${work_dir}/borg-generated-api-keys.yaml" '^  "SHARED_API_KEY": "sk-shared"$'
assert_count "${work_dir}/borg-generated-api-keys.yaml" '^            - name: "SHARED_API_KEY"$' 1
assert_count "${work_dir}/borg-generated-api-keys.yaml" 'apikeyEnv: SHARED_API_KEY$' 2
assert_not_contains "${work_dir}/borg-generated-api-keys.yaml" '^          apikey:'

render borg-existing-api-keys \
  --set-string apikeySecrets.name= \
  --set apikeySecrets.existingSecret=central-backend-keys \
  --set config.k8s_service_discover[0].id=llmd-router \
  --set config.k8s_service_discover[0].service_name=qwen-inference-scheduler \
  --set config.k8s_service_discover[0].port_name=http \
  --set config.k8s_service_discover[0].models[0]=Qwen/Qwen3-32B \
  --set config.k8s_service_discover[0].apikeyEnv=LLMD_API_KEY
assert_not_contains "${work_dir}/borg-existing-api-keys.yaml" '^  name: borg-apikeys$'
assert_contains "${work_dir}/borg-existing-api-keys.yaml" '^                  name: central-backend-keys$'
assert_contains "${work_dir}/borg-existing-api-keys.yaml" '^                  key: "LLMD_API_KEY"$'

if helm template borg-empty-api-key-secret-name "$chart_dir" \
  --set-string apikeySecrets.name= \
  > "${work_dir}/borg-empty-api-key-secret-name.yaml" 2> "${work_dir}/borg-empty-api-key-secret-name.err"; then
  echo "Expected empty apikeySecrets.name without an existing Secret to fail validation" >&2
  exit 1
fi
assert_contains "${work_dir}/borg-empty-api-key-secret-name.err" 'apikeySecrets'

render borg-api-key-create-disabled \
  --set apikeySecrets.create=false \
  --set apikeySecrets.name=external-backend-keys \
  --set config.instances[0].endpoint=http://model-api:8000 \
  --set config.instances[0].models[0]=model \
  --set config.instances[0].apikeyEnv=MODEL_API_KEY
assert_not_contains "${work_dir}/borg-api-key-create-disabled.yaml" '^  name: external-backend-keys$'
assert_contains "${work_dir}/borg-api-key-create-disabled.yaml" '^                  name: external-backend-keys$'

if helm template borg-missing-api-key "$chart_dir" \
  --set config.instances[0].endpoint=http://model-api:8000 \
  --set config.instances[0].models[0]=model \
  --set config.instances[0].apikeyEnv=MODEL_API_KEY \
  > "${work_dir}/borg-missing-api-key.yaml" 2> "${work_dir}/borg-missing-api-key.err"; then
  echo "Expected managed apikeyEnv without apikey to fail" >&2
  exit 1
fi
assert_contains "${work_dir}/borg-missing-api-key.err" 'apikey is required for MODEL_API_KEY'

if helm template borg-conflicting-api-keys "$chart_dir" \
  --set config.instances[0].endpoint=http://model-api:8000 \
  --set config.instances[0].models[0]=model \
  --set config.instances[0].apikeyEnv=SHARED_API_KEY \
  --set-string config.instances[0].apikey=sk-one \
  --set config.k8s_service_discover[0].id=llmd-router \
  --set config.k8s_service_discover[0].service_name=router \
  --set config.k8s_service_discover[0].port=8000 \
  --set config.k8s_service_discover[0].models[0]=model \
  --set config.k8s_service_discover[0].apikeyEnv=SHARED_API_KEY \
  --set-string config.k8s_service_discover[0].apikey=sk-two \
  > "${work_dir}/borg-conflicting-api-keys.yaml" 2> "${work_dir}/borg-conflicting-api-keys.err"; then
  echo "Expected conflicting apikeyEnv values to fail" >&2
  exit 1
fi
assert_contains "${work_dir}/borg-conflicting-api-keys.err" 'conflicting apikey values for apikeyEnv SHARED_API_KEY'

if helm template borg-invalid-backend-env "$chart_dir" \
  --set config.instances[0].endpoint=http://model-api:8000 \
  --set config.instances[0].models[0]=model \
  --set-string config.instances[0].apikeyEnv=BAD-NAME \
  --set-string config.instances[0].apikey=sk-model \
  > "${work_dir}/borg-invalid-backend-env.yaml" 2> "${work_dir}/borg-invalid-backend-env.err"; then
  echo "Expected a non-portable apikeyEnv to fail validation" >&2
  exit 1
fi
assert_contains "${work_dir}/borg-invalid-backend-env.err" 'apikeyEnv'

if helm template borg-reserved-backend-env "$chart_dir" \
  --set config.instances[0].endpoint=http://model-api:8000 \
  --set config.instances[0].models[0]=model \
  --set-string config.instances[0].apikeyEnv=AUTH_KEY \
  --set-string config.instances[0].apikey=sk-model \
  > "${work_dir}/borg-reserved-backend-env.yaml" 2> "${work_dir}/borg-reserved-backend-env.err"; then
  echo "Expected a reserved apikeyEnv to fail validation" >&2
  exit 1
fi
assert_contains "${work_dir}/borg-reserved-backend-env.err" 'collides with a BORG environment variable'

if helm template borg-auth-backend-env-collision "$chart_dir" \
  --set-string authKeySecret.key=CUSTOM_AUTH_KEY \
  --set config.instances[0].endpoint=http://model-api:8000 \
  --set config.instances[0].models[0]=model \
  --set-string config.instances[0].apikeyEnv=CUSTOM_AUTH_KEY \
  --set-string config.instances[0].apikey=sk-model \
  > "${work_dir}/borg-auth-backend-env-collision.yaml" 2> "${work_dir}/borg-auth-backend-env-collision.err"; then
  echo "Expected an auth/backend environment collision to fail validation" >&2
  exit 1
fi
assert_contains "${work_dir}/borg-auth-backend-env-collision.err" 'collides with a BORG environment variable'

render borg-unlimited-header-timeout \
  --set config.upstream.response_header_timeout_seconds=0
assert_contains "${work_dir}/borg-unlimited-header-timeout.yaml" '^        response_header_timeout_seconds: 0$'

if helm template borg-external-name "$chart_dir" --set service.type=ExternalName \
  > "${work_dir}/borg-external-name.yaml" 2> "${work_dir}/borg-external-name.err"; then
  echo "Expected unsupported ExternalName Service type to fail schema validation" >&2
  exit 1
fi
assert_contains "${work_dir}/borg-external-name.err" 'service.type'

render borg-extra-rbac \
  --set-json 'rbac.extraRules=[{"apiGroups":["authorization.k8s.io"],"resources":["subjectaccessreviews"],"verbs":["create"]}]'
assert_contains "${work_dir}/borg-extra-rbac.yaml" 'resources:'
assert_contains "${work_dir}/borg-extra-rbac.yaml" '  - subjectaccessreviews'
assert_contains "${work_dir}/borg-extra-rbac.yaml" '  - create'

render borg-cilium-lb \
  --set ingress.enabled=false \
  --set service.type=LoadBalancer \
  --set-string 'service.labels.cilium\.io/lb-pool=apps' \
  --set-string 'service.annotations.lbipam\.cilium\.io/ips=192.0.2.50'
assert_contains "${work_dir}/borg-cilium-lb.yaml" '^  type: LoadBalancer$'
assert_contains "${work_dir}/borg-cilium-lb.yaml" '^    cilium.io/lb-pool: apps$'
assert_contains "${work_dir}/borg-cilium-lb.yaml" '^    lbipam.cilium.io/ips: 192.0.2.50$'
assert_not_contains "${work_dir}/borg-cilium-lb.yaml" '^kind: Ingress$'

render borg-cilium-tls \
  --set ingress.enabled=false \
  --set service.type=LoadBalancer \
  --set service.port=443 \
  --set server.tls.enabled=true \
  --set certificate.enabled=true \
  --set certificate.secretName=borg-tls \
  --set certificate.commonName=borg.example.com \
  --set certificate.dnsNames[0]=borg.example.com \
  --set certificate.issuerRef.group=ejbca-issuer.keyfactor.com \
  --set certificate.issuerRef.kind=ClusterIssuer \
  --set certificate.issuerRef.name=clusterissuer-pkirules \
  --set certificate.usages[0]='digital signature' \
  --set certificate.usages[1]='key encipherment'
assert_contains "${work_dir}/borg-cilium-tls.yaml" '^kind: Certificate$'
assert_contains "${work_dir}/borg-cilium-tls.yaml" '^  secretName: borg-tls$'
assert_contains "${work_dir}/borg-cilium-tls.yaml" '^    kind: ClusterIssuer$'
assert_contains "${work_dir}/borg-cilium-tls.yaml" '^    group: ejbca-issuer.keyfactor.com$'
assert_contains "${work_dir}/borg-cilium-tls.yaml" '^            - name: TLS_CERT_FILE$'
assert_contains "${work_dir}/borg-cilium-tls.yaml" '^            - name: TLS_KEY_FILE$'
assert_contains "${work_dir}/borg-cilium-tls.yaml" '^              scheme: HTTPS$'
assert_contains "${work_dir}/borg-cilium-tls.yaml" '^    - name: https$'
assert_contains "${work_dir}/borg-cilium-tls.yaml" '^      port: 443$'

render borg-ingress-cert-disabled \
  --api-versions cert-manager.io/v1 \
  --set ingress.enabled=true \
  --set ingress.issuer.cert.enabled=false
assert_contains "${work_dir}/borg-ingress-cert-disabled.yaml" '^kind: Ingress$'
assert_not_contains "${work_dir}/borg-ingress-cert-disabled.yaml" '^kind: Certificate$'
assert_not_contains "${work_dir}/borg-ingress-cert-disabled.yaml" '^  tls:$'
assert_not_contains "${work_dir}/borg-ingress-cert-disabled.yaml" 'force-ssl-redirect'

if helm template borg-invalid-tls "$chart_dir" --set server.tls.enabled=true > "${work_dir}/borg-invalid-tls.yaml" 2> "${work_dir}/borg-invalid-tls.err"; then
  echo "Expected server.tls.enabled=true without certificate.enabled or server.tls.secretName to fail" >&2
  exit 1
fi
assert_contains "${work_dir}/borg-invalid-tls.err" 'server.tls.secretName must be set'
