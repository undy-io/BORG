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

helm lint "$chart_dir"

render borg-default
assert_not_contains "${work_dir}/borg-default.yaml" '^kind: Ingress$'
assert_not_contains "${work_dir}/borg-default.yaml" '^kind: Certificate$'
assert_not_contains "${work_dir}/borg-default.yaml" '^  annotations:$'
assert_not_contains "${work_dir}/borg-default.yaml" '^stringData:$'
assert_contains "${work_dir}/borg-default.yaml" '^      instances:$'
assert_contains "${work_dir}/borg-default.yaml" '^        \[\]$'

render borg-ingress \
  --api-versions cert-manager.io/v1 \
  --set ingress.enabled=true
assert_contains "${work_dir}/borg-ingress.yaml" '^kind: Ingress$'
assert_contains "${work_dir}/borg-ingress.yaml" '^kind: Certificate$'
assert_contains "${work_dir}/borg-ingress.yaml" '^  tls:$'
assert_contains "${work_dir}/borg-ingress.yaml" '^    - secretName: borg-ingress-tls$'

render borg-existing-secret \
  --api-versions cert-manager.io/v1 \
  --set ingress.enabled=true \
  --set ingress.tls.existingSecret=my-existing-tls
assert_contains "${work_dir}/borg-existing-secret.yaml" '^kind: Ingress$'
assert_not_contains "${work_dir}/borg-existing-secret.yaml" '^kind: Certificate$'
assert_contains "${work_dir}/borg-existing-secret.yaml" '^    - secretName: my-existing-tls$'

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

if helm template borg-invalid-tls "$chart_dir" --set server.tls.enabled=true > "${work_dir}/borg-invalid-tls.yaml" 2> "${work_dir}/borg-invalid-tls.err"; then
  echo "Expected server.tls.enabled=true without certificate.enabled or server.tls.secretName to fail" >&2
  exit 1
fi
assert_contains "${work_dir}/borg-invalid-tls.err" 'server.tls.secretName must be set'
