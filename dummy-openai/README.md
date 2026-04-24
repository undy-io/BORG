
Dummy OpenAI backend
====================

This helper service is used for local BORG testing with KinD and Helm. It gives BORG an OpenAI-compatible backend to discover while the main service is being developed or migrated.

Build and load the image:

```bash
podman build -t localhost/dummy-openai:dev .
rm -f dummy.tar
podman save --format docker-archive -o dummy.tar localhost/dummy-openai:dev
kind load image-archive dummy.tar --name borg-dev
```

Deploy it:

```bash
helm upgrade --install dummy-openai ./charts/dummy-openai \
  --set image.repository=localhost/dummy-openai \
  --set image.tag=dev
```
