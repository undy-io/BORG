
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
