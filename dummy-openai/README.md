
# Dummy OpenAI Backend

This tiny Go helper service is used for local BORG testing with KinD and Helm. It gives BORG an OpenAI-compatible backend to discover during validation.

It implements `GET /v1/models` plus deterministic `POST /v1/chat/completions` responses. When the request body contains `"stream": true`, it returns SSE chunks ending with `data: [DONE]`.

Run it locally:

```bash
go run ./
```

Build and load the image:

```bash
docker build -t dummy-openai:kind .
kind load docker-image dummy-openai:kind --name borg
```

Deploy it:

```bash
helm upgrade --install dummy-openai ./charts/dummy-openai \
  --namespace vllm-services \
  --create-namespace \
  --set image.repository=dummy-openai \
  --set image.tag=kind \
  --set image.pullPolicy=IfNotPresent
```

The repeatable Go validation path is `scripts/validate-kind-go.sh` from the repository root.
