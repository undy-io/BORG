# -*- coding: utf-8 -*-
import base64
import importlib
import json
import os
from typing import Dict, Any

import pytest
import yaml
from cryptography.hazmat.primitives.ciphers.aead import AESGCM
from fastapi import FastAPI, Request
from fastapi.testclient import TestClient
import httpx
from httpx import ASGITransport, AsyncClient
from starlette.responses import JSONResponse, StreamingResponse


# ---------------------------------------------------------------------------
# Dynamic import of your app module
# ---------------------------------------------------------------------------
# Point this to the module that defines: `app`, `configure(config_path)`, and `proxy`
# You can override with: export BORG_MAIN_MODULE="your_package.main"
APP_MODULE_PATH = 'borg.main'
app_module = importlib.import_module(APP_MODULE_PATH)
configure = getattr(app_module, "configure")
proxy = getattr(app_module, "proxy")


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
                yield b'data: [DONE]\n\n'
            return StreamingResponse(gen(), media_type="text/event-stream")

        # Non-stream JSON echo
        return JSONResponse({
            "upstream": "ok",
            "received": body,
            "auth": req.headers.get("authorization"),
            "content_type": req.headers.get("content-type"),
        })

    return app


@pytest.fixture
def patch_httpx_to_upstream(monkeypatch, fake_upstream_app):
    """
    Patch httpx.AsyncClient used inside the ProxyService so any outgoing HTTP
    goes to our fake_upstream_app instead of the network.
    """
    def factory(*args, **kwargs) -> AsyncClient:  # noqa: ANN001
        # Route all requests (any host) to the ASGI app
        transport = ASGITransport(app=fake_upstream_app)
        # Use a base_url that matches your configured endpoint host (e.g., "http://upstream")
        return AsyncClient(transport=transport, base_url="http://upstream")
    monkeypatch.setattr(httpx, "AsyncClient", factory)


def _write_config(
    tmp_path,
    *,
    instances: list[dict[str, Any]] | None = None,
    update_interval: int = -1,
    auth_key_b64: str | None = None,
    auth_prefix: str | None = None,
) -> str:
    borg: Dict[str, Any] = {
        "instances": instances or [{
            "endpoint": "http://upstream",
            "apikey": "sk-test",
            "models": ["openai/gpt-oss-20b"],
        }],
        "update_interval": update_interval,
    }
    if auth_key_b64 is not None:
        borg["auth_key"] = auth_key_b64
    if auth_prefix is not None:
        borg["auth_prefix"] = auth_prefix

    path = tmp_path / "config.yaml"
    path.write_text(yaml.safe_dump({"borg": borg}), encoding="utf-8")
    return str(path)


@pytest.fixture
def client_noauth(tmp_path, monkeypatch, patch_httpx_to_upstream):
    """
    App client with auth DISABLED (auth_key is EMPTY). Good for baseline tests.
    """
    # Ensure no stray AUTH_KEY in env affects the run
    monkeypatch.delenv("AUTH_KEY", raising=False)

    # Reset global state for clean runs
    proxy._instances.clear()
    if hasattr(app_module, "services"):
        app_module.services.clear()

    cfg = _write_config(tmp_path)  # no auth by default
    app = configure(cfg)
    with TestClient(app) as client:
        yield client


@pytest.fixture
def client_with_auth(tmp_path, monkeypatch, patch_httpx_to_upstream):
    """
    App client with auth ENABLED; creates a 32-byte key and sets AUTH_KEY env var.
    """
    # 32-byte AES key (all zeros for reproducible tests)
    key_bytes = b"\x00" * 32
    key_b64 = base64.urlsafe_b64encode(key_bytes).decode()

    # Reset global state
    proxy._instances.clear()
    if hasattr(app_module, "services"):
        app_module.services.clear()

    # Make sure the app reads our key from env (overrides config)
    monkeypatch.setenv("AUTH_KEY", key_b64)

    cfg = _write_config(tmp_path, auth_prefix="BORG:")  # prefix will be set
    app = configure(cfg)

    # Return both the client and the key so tests can mint tokens
    with TestClient(app) as client:
        yield client, key_bytes


# ---------------------------------------------------------------------------
# Tests
# ---------------------------------------------------------------------------

def test_root_ok(client_noauth: TestClient):
    r = client_noauth.get("/")
    assert r.status_code == 200
    data = r.json()
    assert data["status"] == "ok"
    assert "Proxy router is running" in data["detail"]


def test_list_models(client_noauth: TestClient):
    r = client_noauth.get("/v1/models")
    assert r.status_code == 200
    data = r.json()
    assert data["object"] == "list"
    ids = [m["id"] for m in data["data"]]
    assert "openai/gpt-oss-20b" in ids


def test_proxy_forwards_and_swaps_auth_header(client_noauth: TestClient):
    payload = {
        "model": "openai/gpt-oss-20b",
        "messages": [
            {"role": "system", "content": "You are a helpful assistant."},
            {"role": "user", "content": "Who won the world series in 2020?"},
        ],
    }
    r = client_noauth.post("/v1/chat/completions", json=payload)
    assert r.status_code == 200
    data = r.json()
    # The proxy should replace the inbound Authorization with backend apikey
    assert data["auth"] == "Bearer sk-test"
    assert data["received"] == payload
    assert data["content_type"] == "application/json"

@pytest.mark.asyncio
async def test_proxy_streaming_via_stream_flag(client_noauth: TestClient):
    """
    This test now uses httpx.AsyncClient to handle the stream correctly.
    """
    payload = {
        "model": "openai/gpt-oss-20b",
        "stream": True,
        "messages": [{"role": "user", "content": "Hello"}],
    }
    
    # 1. Use a true async client pointing at your app
    transport = ASGITransport(app=client_noauth.app)
    async with httpx.AsyncClient(transport=transport, base_url="http://test") as client:
        # 2. Use 'async with' to make the streaming request
        async with client.stream("POST", "/v1/chat/completions", json=payload) as r:
            assert r.status_code == 200
            
            # 3. Use 'async for' to iterate over the chunks
            text_chunks = [chunk.decode() async for chunk in r.aiter_raw()]
            text = "".join(text_chunks)
            
            assert "[DONE]" in text

@pytest.mark.asyncio
async def test_proxy_streaming_via_accept_header(client_noauth: TestClient):
    """
    This test is also updated to use httpx.AsyncClient.
    """
    payload = {
        "model": "openai/gpt-oss-20b",
        "messages": [{"role": "user", "content": "Hello"}],
    }
    headers = {"Accept": "text/event-stream; charset=utf-8"}
    
    transport = ASGITransport(app=client_noauth.app)
    async with httpx.AsyncClient(transport=transport, base_url="http://test") as client:
        async with client.stream("POST", "/v1/chat/completions", json=payload, headers=headers) as r:
            assert r.status_code == 200

            text = "".join([chunk.decode() async for chunk in r.aiter_raw()])
            assert "[DONE]" in text

def test_unknown_model_returns_404(client_noauth: TestClient):
    payload = {"model": "totally-unknown-model", "messages": [{"role": "user", "content": "Hi"}]}
    r = client_noauth.post("/v1/chat/completions", json=payload)
    assert r.status_code == 404
    # ProxyService should raise an HTTPException with detail mentioning the model
    assert "Unknown model" in r.text


def _mint_auth_token(key_bytes: bytes, prefix: str, username: str) -> str:
    """
    Make a URL-safe base64 token matching ProxyService._decrypt_token() expectations:
    token = base64url( nonce(12) || AESGCM(nonce, plaintext, None) )
    where plaintext = f"{prefix}{username}".encode()
    """
    import os as _os
    nonce = _os.urandom(12)
    plaintext = f"{prefix}{username}".encode()
    ct = AESGCM(key_bytes).encrypt(nonce, plaintext, None)
    return base64.urlsafe_b64encode(nonce + ct).decode()


def test_auth_enforced_when_configured(client_with_auth):
    client, key_bytes = client_with_auth

    # No Authorization -> 401
    payload = {"model": "openai/gpt-oss-20b", "messages": [{"role": "user", "content": "Hi"}]}
    r1 = client.post("/v1/chat/completions", json=payload)
    assert r1.status_code == 401

    # Bad Authorization format -> 401
    r2 = client.post(
        "/v1/chat/completions",
        json=payload,
        headers={"Authorization": "Bearer not-a-valid-token"},
    )
    assert r2.status_code == 401

    # Valid token -> 200
    token = _mint_auth_token(key_bytes, "BORG:", "alice")
    r3 = client.post(
        "/v1/chat/completions",
        json=payload,
        headers={"Authorization": f"Bearer {token}"},
    )
    assert r3.status_code == 200
    data = r3.json()
    assert data["auth"] == "Bearer sk-test"
