# syntax=docker/dockerfile:1.7

FROM python:3.12-slim AS builder

COPY --from=ghcr.io/astral-sh/uv:0.11.7 /uv /uvx /bin/

ENV UV_LINK_MODE=copy \
    UV_PYTHON_DOWNLOADS=0

WORKDIR /app

COPY pyproject.toml uv.lock README.md LICENSE ./

RUN --mount=type=cache,target=/root/.cache/uv \
    uv sync --frozen --no-install-project --no-dev --no-editable

COPY src/ ./src/
COPY config.example.yaml ./config.example.yaml
COPY entrypoint.sh /entrypoint.sh

RUN chmod +x /entrypoint.sh

RUN --mount=type=cache,target=/root/.cache/uv \
    uv sync --frozen --no-dev --no-editable

FROM python:3.12-slim

WORKDIR /app

COPY --from=builder /app/.venv /app/.venv
COPY --from=builder /app/config.example.yaml /app/config.yaml
COPY --from=builder /entrypoint.sh /entrypoint.sh

ENV PATH="/app/.venv/bin:$PATH"

EXPOSE 8000

CMD ["/entrypoint.sh"]
