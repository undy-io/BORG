
podman build -t localhost/dummy-openai:dev .
rm -f dummy.tar
podman save --format docker-archive -o dummy.tar localhost/dummy-openai:dev
kind load image-archive dummy.tar --name borg-dev

helm upgrade --install dummy-openai ./charts/dummy-openai \
  --set image.repository=localhost/dummy-openai \
  --set image.tag=dev