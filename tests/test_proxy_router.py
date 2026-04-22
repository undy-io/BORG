# -*- coding: utf-8 -*-
import base64
import importlib
import os
from collections.abc import AsyncIterator
from contextlib import asynccontextmanager
from types import SimpleNamespace
from typing import Any

import httpx
import pytest
import pytest_asyncio
import yaml
from cryptography.hazmat.primitives.ciphers.aead import AESGCM
from fastapi import FastAPI, Request
from httpx import ASGITransport, AsyncClient
from starlette.responses import JSONResponse, StreamingResponse

pytestmark = pytest.mark.asyncio


# ---------------------------------------------------------------------------
# Dynamic import of your app module
# ---------------------------------------------------------------------------
# Point this to the module that defines: `app`, `configure(config_path)`, and
# `create_app(config_path)`.
# You can override with: export BORG_MAIN_MODULE="your_package.main"
APP_MODULE_PATH = "borg.main"
app_module = importlib.import_module(APP_MODULE_PATH)
create_app = getattr(app_module, "create_app")
proxy_module = importlib.import_module("borg.proxy")


# ---------------------------------------------------------------------------
# Helpers / fixtures
# ---------------------------------------------------------------------------


@pytest.fixture
def fake_upstream_app() -> FastAPI:
    """
    A small ASGI app that mimics an OpenAI-compatible upstream.
    It echoes the received JSON and the Authorization header.
    For streaming requests, it emits two SSE chunks and a [DONE].
    """
    app = FastAPI()

    @app.post("/v1/chat/completions")
    async def chat(req: Request):
        body = await req.json()
        accept = req.headers.get("accept", "")

        # Streaming if either Accept contains text/event-stream or body.stream == True
        if "text/event-stream" in accept or body.get("stream"):

            async def gen():
                yield b'data: {"id":"mock-1","object":"chat.completion.chunk","choices":[{"delta":{"content":"Hi"}}]}\n\n'
                yield b'data: {"id":"mock-1","object":"chat.completion.chunk","choices":[{"delta":{"content":"!"}}]}\n\n'
                yield b"data: [DONE]\n\n"

            return StreamingResponse(gen(), media_type="text/event-stream")

        # Non-stream JSON echo
        return JSONResponse(
            {
                "upstream": "ok",
                "received": body,
                "auth": req.headers.get("authorization"),
                "content_type": req.headers.get("content-type"),
            }
        )

    return app


@pytest.fixture
def patch_httpx_to_upstream(monkeypatch, fake_upstream_app):
    """
    Patch httpx.AsyncClient used inside the ProxyService so any outgoing HTTP
    goes to our fake_upstream_app instead of the network.
    """

    def factory(*args, **kwargs) -> AsyncClient:  # noqa: ANN001
        # Route all requests (any host) to the ASGI app.
        transport = ASGITransport(app=fake_upstream_app)
        # Use a base_url that matches your configured endpoint host (e.g., "http://upstream").
        return AsyncClient(transport=transport, base_url="http://upstream")

    fake_httpx = SimpleNamespace(
        AsyncClient=factory,
        StreamClosed=httpx.StreamClosed,
    )
    monkeypatch.setattr(proxy_module, "httpx", fake_httpx)


def _write_config(
    tmp_path,
    *,
    filename: str = "config.yaml",
    instances: list[dict[str, Any]] | None = None,
    update_interval: int = -1,
    auth_key_b64: str | None = None,
    auth_prefix: str | None = None,
    k8s_discover: list[dict[str, Any]] | None = None,
) -> str:
    borg: dict[str, Any] = {
        "instances": instances
        or [
            {
                "endpoint": "http://upstream",
                "apikey": "sk-test",
                "models": ["openai/gpt-oss-20b"],
            }
        ],
        "update_interval": update_interval,
    }
    if auth_key_b64 is not None:
        borg["auth_key"] = auth_key_b64
    if auth_prefix is not None:
        borg["auth_prefix"] = auth_prefix
    if k8s_discover is not None:
        borg["k8s_discover"] = k8s_discover

    path = tmp_path / filename
    path.write_text(yaml.safe_dump({"borg": borg}), encoding="utf-8")
    return str(path)


@asynccontextmanager
async def _client_for_config(config_path: str) -> AsyncIterator[AsyncClient]:
    app = create_app(config_path)
    async with app.router.lifespan_context(app):
        transport = ASGITransport(app=app)
        async with AsyncClient(
            transport=transport, base_url="http://testserver"
        ) as client:
            yield client


@pytest_asyncio.fixture
async def client_noauth(tmp_path, monkeypatch, patch_httpx_to_upstream):
    """
    App client with auth DISABLED (auth_key is EMPTY). Good for baseline tests.
    """
    # Ensure no stray AUTH_KEY in env affects the run.
    monkeypatch.delenv("AUTH_KEY", raising=False)

    cfg = _write_config(tmp_path)  # no auth by default
    async with _client_for_config(cfg) as client:
        yield client


@pytest_asyncio.fixture
async def client_with_auth(tmp_path, monkeypatch, patch_httpx_to_upstream):
    """
    App client with auth ENABLED; creates a 32-byte key and sets AUTH_KEY env var.
    """
    # 32-byte AES key (all zeros for reproducible tests).
    key_bytes = b"\x00" * 32
    key_b64 = base64.urlsafe_b64encode(key_bytes).decode()

    # Make sure the app reads our key from env (overrides config).
    monkeypatch.setenv("AUTH_KEY", key_b64)

    cfg = _write_config(tmp_path, auth_prefix="BORG:")  # prefix will be set
    async with _client_for_config(cfg) as client:
        yield client, key_bytes


# ---------------------------------------------------------------------------
# Tests
# ---------------------------------------------------------------------------


async def test_root_ok(client_noauth: AsyncClient):
    response = await client_noauth.get("/")
    assert response.status_code == 200
    data = response.json()
    assert data["status"] == "ok"
    assert "Proxy router is running" in data["detail"]


async def test_list_models(client_noauth: AsyncClient):
    response = await client_noauth.get("/v1/models")
    assert response.status_code == 200
    data = response.json()
    assert data["object"] == "list"
    ids = [model["id"] for model in data["data"]]
    assert "openai/gpt-oss-20b" in ids


async def test_proxy_forwards_and_swaps_auth_header(client_noauth: AsyncClient):
    payload = {
        "model": "openai/gpt-oss-20b",
        "messages": [
            {"role": "system", "content": "You are a helpful assistant."},
            {"role": "user", "content": "Who won the world series in 2020?"},
        ],
    }
    response = await client_noauth.post("/v1/chat/completions", json=payload)
    assert response.status_code == 200
    data = response.json()
    # The proxy should replace the inbound Authorization with backend apikey.
    assert data["auth"] == "Bearer sk-test"
    assert data["received"] == payload
    assert data["content_type"] == "application/json"


async def test_proxy_streaming_via_stream_flag(client_noauth: AsyncClient):
    payload = {
        "model": "openai/gpt-oss-20b",
        "stream": True,  # should force streaming path
        "messages": [{"role": "user", "content": "Hello"}],
    }
    async with client_noauth.stream(
        "POST", "/v1/chat/completions", json=payload
    ) as response:
        assert response.status_code == 200
        text = "".join([chunk.decode() async for chunk in response.aiter_raw()])
        assert "[DONE]" in text


async def test_proxy_streaming_via_accept_header(client_noauth: AsyncClient):
    payload = {
        "model": "openai/gpt-oss-20b",
        "messages": [{"role": "user", "content": "Hello"}],
    }
    headers = {"Accept": "text/event-stream; charset=utf-8"}
    async with client_noauth.stream(
        "POST", "/v1/chat/completions", json=payload, headers=headers
    ) as response:
        assert response.status_code == 200
        text = "".join([chunk.decode() async for chunk in response.aiter_raw()])
        assert "[DONE]" in text


async def test_unknown_model_returns_404(client_noauth: AsyncClient):
    payload = {
        "model": "totally-unknown-model",
        "messages": [{"role": "user", "content": "Hi"}],
    }
    response = await client_noauth.post("/v1/chat/completions", json=payload)
    assert response.status_code == 404
    # ProxyService should raise an HTTPException with detail mentioning the model.
    assert "Unknown model" in response.text


async def test_proxy_rejects_invalid_json_body(client_noauth: AsyncClient):
    response = await client_noauth.post(
        "/v1/chat/completions",
        content=b"{not-json",
        headers={"Content-Type": "application/json"},
    )
    assert response.status_code == 400
    assert response.json()["detail"] == "Body must be valid JSON"


@pytest.mark.parametrize("body", [b"[]", b"null", b'"text"'])
async def test_proxy_rejects_non_object_json_body(
    client_noauth: AsyncClient, body: bytes
):
    response = await client_noauth.post(
        "/v1/chat/completions",
        content=body,
        headers={"Content-Type": "application/json"},
    )
    assert response.status_code == 400
    assert response.json()["detail"] == "Body must be valid JSON"


async def test_proxy_rejects_missing_model_in_body(client_noauth: AsyncClient):
    response = await client_noauth.post(
        "/v1/chat/completions",
        json={"messages": [{"role": "user", "content": "Hi"}]},
    )
    assert response.status_code == 400
    assert response.json()["detail"] == "Missing 'model' in request body"


def _mint_auth_token(key_bytes: bytes, prefix: str, username: str) -> str:
    """
    Make a URL-safe base64 token matching ProxyService._decrypt_token() expectations:
    token = base64url( nonce(12) || AESGCM(nonce, plaintext, None) )
    where plaintext = f"{prefix}{username}".encode()
    """
    nonce = os.urandom(12)
    plaintext = f"{prefix}{username}".encode()
    ct = AESGCM(key_bytes).encrypt(nonce, plaintext, None)
    return base64.urlsafe_b64encode(nonce + ct).decode()


async def test_auth_enforced_when_configured(client_with_auth):
    client, key_bytes = client_with_auth

    # No Authorization -> 401.
    payload = {
        "model": "openai/gpt-oss-20b",
        "messages": [{"role": "user", "content": "Hi"}],
    }
    response_no_auth = await client.post("/v1/chat/completions", json=payload)
    assert response_no_auth.status_code == 401
    assert response_no_auth.json()["detail"] == "Missing API key"

    # Non-bearer Authorization -> 401.
    response_non_bearer_auth = await client.post(
        "/v1/chat/completions",
        json=payload,
        headers={"Authorization": "Token not-a-bearer-token"},
    )
    assert response_non_bearer_auth.status_code == 401
    assert response_non_bearer_auth.json()["detail"] == "Missing API key"

    # Malformed bearer token -> 401.
    response_bad_bearer = await client.post(
        "/v1/chat/completions",
        json=payload,
        headers={"Authorization": "Bearer not-a-valid-token"},
    )
    assert response_bad_bearer.status_code == 401
    assert response_bad_bearer.json()["detail"] == "Invalid API key"

    # Valid token -> 200.
    token = _mint_auth_token(key_bytes, "BORG:", "alice")
    response_ok = await client.post(
        "/v1/chat/completions",
        json=payload,
        headers={"Authorization": f"Bearer {token}"},
    )
    assert response_ok.status_code == 200
    data = response_ok.json()
    assert data["auth"] == "Bearer sk-test"


async def test_default_auth_prefix_is_proxy_uppercase(
    tmp_path, monkeypatch, patch_httpx_to_upstream
):
    monkeypatch.delenv("AUTH_KEY", raising=False)
    monkeypatch.delenv("BORG_AUTH_KEY", raising=False)

    auth_key = b"\x03" * 32
    auth_key_b64 = base64.urlsafe_b64encode(auth_key).decode()
    cfg = _write_config(
        tmp_path,
        filename="default-prefix.yaml",
        auth_key_b64=auth_key_b64,
    )

    payload = {
        "model": "openai/gpt-oss-20b",
        "messages": [{"role": "user", "content": "Hello"}],
    }

    async with _client_for_config(cfg) as client:
        proxy_token = _mint_auth_token(auth_key, "PROXY:", "alice")
        proxy_response = await client.post(
            "/v1/chat/completions",
            json=payload,
            headers={"Authorization": f"Bearer {proxy_token}"},
        )
        assert proxy_response.status_code == 200

        buggy_token = _mint_auth_token(auth_key, "Proxy:", "alice")
        buggy_response = await client.post(
            "/v1/chat/completions",
            json=payload,
            headers={"Authorization": f"Bearer {buggy_token}"},
        )
        assert buggy_response.status_code == 401


async def test_factory_apps_isolate_auth_and_models(
    tmp_path, monkeypatch, patch_httpx_to_upstream
):
    monkeypatch.delenv("AUTH_KEY", raising=False)

    auth_key = b"\x01" * 32
    auth_key_b64 = base64.urlsafe_b64encode(auth_key).decode()
    auth_cfg = _write_config(
        tmp_path,
        filename="auth.yaml",
        instances=[
            {
                "endpoint": "http://upstream",
                "apikey": "sk-test",
                "models": ["isolated-auth-model"],
            }
        ],
        auth_key_b64=auth_key_b64,
        auth_prefix="BORG:",
    )
    noauth_cfg = _write_config(
        tmp_path,
        filename="noauth.yaml",
        instances=[
            {
                "endpoint": "http://upstream",
                "apikey": "sk-test",
                "models": ["isolated-noauth-model"],
            }
        ],
    )

    async with _client_for_config(auth_cfg) as auth_client:
        auth_models = await auth_client.get("/v1/models")
        assert auth_models.status_code == 200
        assert auth_models.json()["data"] == [
            {
                "id": "isolated-auth-model",
                "object": "model",
                "created": None,
                "owned_by": "vllm-proxy",
            }
        ]

        denied = await auth_client.post(
            "/v1/chat/completions",
            json={
                "model": "isolated-auth-model",
                "messages": [{"role": "user", "content": "Hello"}],
            },
        )
        assert denied.status_code == 401

        token = _mint_auth_token(auth_key, "BORG:", "alice")
        allowed = await auth_client.post(
            "/v1/chat/completions",
            json={
                "model": "isolated-auth-model",
                "messages": [{"role": "user", "content": "Hello"}],
            },
            headers={"Authorization": f"Bearer {token}"},
        )
        assert allowed.status_code == 200

    async with _client_for_config(noauth_cfg) as noauth_client:
        noauth_models = await noauth_client.get("/v1/models")
        assert noauth_models.status_code == 200
        assert noauth_models.json()["data"] == [
            {
                "id": "isolated-noauth-model",
                "object": "model",
                "created": None,
                "owned_by": "vllm-proxy",
            }
        ]

        allowed = await noauth_client.post(
            "/v1/chat/completions",
            json={
                "model": "isolated-noauth-model",
                "messages": [{"role": "user", "content": "Hello"}],
            },
        )
        assert allowed.status_code == 200


async def test_discovery_init_failure_is_logged_once(tmp_path, monkeypatch, caplog):
    monkeypatch.delenv("AUTH_KEY", raising=False)

    k8s_discovery = importlib.import_module("borg.k8s_discovery")

    def fail_k8s_init(*args, **kwargs):  # noqa: ANN001, ARG001
        raise RuntimeError("boom")

    monkeypatch.setattr(k8s_discovery, "K8SDiscoveryService", fail_k8s_init)
    app = create_app(
        _write_config(
            tmp_path,
            update_interval=30,
            k8s_discover=[{"namespace": "default", "selector": "app=test"}],
        )
    )

    caplog.clear()
    with caplog.at_level("ERROR", logger=APP_MODULE_PATH):
        async with app.router.lifespan_context(app):
            assert app.state.services == []

    records = [
        record
        for record in caplog.records
        if record.name == APP_MODULE_PATH
        and record.message == "Failed to load k8s discovery service"
    ]
    assert len(records) == 1
