import asyncio
import json
import os
from itertools import cycle
from typing import Dict, List, Optional

import httpx
from pydantic import BaseModel

from cryptography.hazmat.primitives.ciphers.aead import AESGCM
from fastapi import Depends, FastAPI, HTTPException, Request, Response
from starlette.responses import StreamingResponse
from fastapi.responses import JSONResponse

from typing import Any, Tuple, Dict, List

import logging

logger = logging.getLogger(__name__)

class RoundRobinSet:
    def __init__(self):
        self._data: Dict[str, Dict[str, Any]] = {}
        self._cycler = None            # will hold cycle(self._data.items())
    
    def _reset_cycle(self) -> None:
        """Re-create the cycling iterator whenever membership changes."""
        # cycle() needs a *snapshot* of the current keys, so we pass a
        # view (self._data.items()) each time we change the dict
        self._cycler = cycle(self._data.items()) if self._data else None

    def add(self, endpoint: str, **attrs: Any) -> None:
        """Add/replace an endpoint with optional metadata in **attrs."""
        self._data[endpoint] = attrs
        self._reset_cycle()

    def rmv(self, endpoint: str) -> None:
        """Remove an endpoint; ignore if it isn’t present."""
        self._data.pop(endpoint, None)
        self._reset_cycle()

    def next(self) -> Tuple[str, Dict[str, Any]]:
        """
        Return the next (endpoint, attrs) pair in round-robin order.
        Raises RuntimeError if the set is empty.
        """
        if not self._cycler:
            raise RuntimeError("RoundRobinSet is empty")
        return next(self._cycler)

class ProxyService:
    def __init__(
        self,
        namespace: str = 'default',
        auth_key: bytes | None = None,
        auth_prefix: str | None = 'Proxy:',
        api_prefix: str = "Bearer "):
        """
        Initialize the vLLM Proxy Service
        
        :param namespace: Kubernetes namespace to search for vLLM instances
        """
        self._instances: Dict[str, Dict] = {}
        self._lock = asyncio.Lock()
        self.api_prefix = api_prefix
        self.auth_key = auth_key
        self.auth_prefix = auth_prefix
    
    def set_auth_key(self, auth_key: bytes) -> None:
        """Set the authentication key for token decryption."""
        self.auth_key = auth_key

    def set_auth_prefix(self, auth_prefix: bytes) -> None:
        """Set the authentication key for token decryption."""
        self.auth_prefix = auth_prefix
    
    def _decrypt_token(self, token_b64: str, key: bytes) -> str:
        """Decode *token_b64* (URL‑safe base64) and decrypt with **key**.

        Expected layout: ``nonce || ciphertext || tag`` where *nonce* is
        ``NONCE_LEN`` bytes.  Returns the UTF‑8 plaintext string or raises
        :class:`fastapi.HTTPException` *401* on error.
        """
        try:
            buf = base64.urlsafe_b64decode(token_b64.encode())
            if len(buf) <= self.NONCE_LEN:
                raise ValueError("token too short")
            nonce, ct_tag = buf[:self.NONCE_LEN], buf[self.NONCE_LEN:]
            plaintext = AESGCM(key).decrypt(nonce, ct_tag, None)
            return plaintext.decode()
        except Exception:  # noqa: BLE001 ‑‑ treat any failure as auth failure
            raise HTTPException(status.HTTP_401_UNAUTHORIZED, "Invalid API key") from None

    def require_auth(self, request: Request) -> str:  # noqa: D401
        """FastAPI dependency that validates the *Authorization* header.

        On success it writes ``request.state.username`` and returns it; otherwise
        raises *401 Unauthorized*.
        """
        if not self.auth_key:
            request.state.username = 'ANONYMOUS'
            return request.state.username

        auth = request.headers.get("authorization")

        if not auth or not auth.startswith(self.api_prefix):
            raise HTTPException(status.HTTP_401_UNAUTHORIZED, "Missing API key")

        if self.auth_key is None:
            raise HTTPException(status.HTTP_500_INTERNAL_SERVER_ERROR, "Auth key not configured")

        plaintext = self._decrypt_token(auth[len(self.api_prefix) :], self.auth_key)

        if not plaintext.startswith(self.auth_prefix):
            raise HTTPException(status.HTTP_401_UNAUTHORIZED, "Invalid API key")

        username = plaintext[len(self.auth_prefix):]
        request.state.username = username
        return username
    
    async def add_instance(
        self,
        endpoint: str,
        apikey: str,
        models: List[str],
    ) -> None:
        """
        Register *endpoint* for every model in *models*.
        Assumes the same API key applies to all.
        """
        logger.info(f"Adding endpoint {endpoint} to models {','.join(models)}")
        async with self._lock:               # writer section
            for model in models:
                bucket = self._instances.setdefault(model, RoundRobinSet())
                bucket.add(endpoint, apikey=apikey)
    
    async def remove_instance(
        self,
        endpoint: str,
        models: List[str] | None = None) -> None:
        """Remove *endpoint* from every model group that contains it."""
        logger.info(f"Removing endpoint {endpoint} from models {','.join(models)}")
        async with self._lock:
            if models is None:
                models = self._instances.keys()
            
            for model in models:
                await sself._instances[model].remove(endpoint)
        
    async def pick_endpoint(self, model: str) -> str:
        """
        Return the next endpoint for *model* in round-robin order.
        Raises KeyError if the model is unknown.
        """
        # quick read outside the big lock ↓
        bucket = None
        async with self._lock:               # reader section
            bucket = self._instances.get(model)

        if bucket is None:
            raise KeyError(f"No instances for model {model!r}")

        return bucket.next()
    
    async def _choose(self, model: str) -> Tuple[str, Dict[str, Any]]:
        """Internal helper: get (endpoint, attrs) or raise 404."""
        async with self._lock:
            bucket = self._instances.get(model)
        if bucket is None:
            raise HTTPException(404, f"Unknown model: {model!r}")
        return bucket.next()              # (endpoint, {"apikey": …})

    async def list_models(self):
        """
        List available models
        """
        async with self._lock:
            return {
                "object": "list",
                "data": [
                    {
                        "id": model,
                        "object": "model",
                        "created": None,  # Could add timestamp if needed
                        "owned_by": "vllm-proxy"
                    } for model in sorted(self._instances.keys())
                ]
            }
    
    async def proxy_request(self, model: str, request: Request) -> Response:
        """
        Forward a **regular** (non-streaming) OpenAI-compatible request
        to the next backend instance and return the backend’s response
        as-is (status code, body, most headers).
        """
        endpoint, meta = await self._choose(model)

        # Build the upstream URL:  e.g. http://10.0.0.5:8000/v1/chat/completions
        upstream_url = f"{endpoint}{request.url.path}"

        # Copy headers except hop-by-hop ones FastAPI will set for us
        excluded = {"host", "content-length", "connection", "keep-alive",
                    "proxy-authenticate", "proxy-authorization", "te",
                    "trailers", "transfer-encoding", "upgrade"}
        forward_headers = {
            k: v
            for k, v in request.headers.items()
            if k.lower() not in excluded
        }
        forward_headers["authorization"] = f"Bearer {meta['apikey']}"

        # Grab the body (it will already be bytes for JSON requests)
        body = await request.body()

        async with httpx.AsyncClient(timeout=30.0) as client:
            r = await client.request(
                request.method,
                upstream_url,
                headers=forward_headers,
                content=body,
                params=request.query_params,
            )

        # Pass most headers back unchanged
        response_headers = {
            k: v for k, v in r.headers.items()
            if k.lower() not in {"content-encoding",
                                 "transfer-encoding",
                                 "connection"}
        }
        return Response(
            content=r.content,
            status_code=r.status_code,
            headers=response_headers,
            media_type=r.headers.get("content-type"),
        )

    async def proxy_request_stream(self, model: str, request: Request) -> Response:
        """
        Same idea as `proxy_request`, but keeps the HTTP/1.1 stream open
        so tokens/chunks pass through in real time (SSE or chunked JSON).
        """
        endpoint, meta = await self._choose(model)
        upstream_url = f"{endpoint}{request.url.path}"

        excluded = {"host", "content-length", "connection", "keep-alive",
                    "proxy-authenticate", "proxy-authorization", "te",
                    "trailers", "transfer-encoding", "upgrade"}
        forward_headers = {
            k: v
            for k, v in request.headers.items()
            if k.lower() not in excluded
        }
        forward_headers["authorization"] = f"Bearer {meta['apikey']}"

        # NOTE: `httpx` lets us stream both the request *and* the response
        async with httpx.AsyncClient(timeout=None) as client:
            async with client.stream(
                request.method,
                upstream_url,
                headers=forward_headers,
                content=request.stream(),        # raw incoming body ↗
                params=request.query_params,
            ) as upstream:

                async def aiter():
                    async for chunk in upstream.aiter_bytes():
                        yield chunk

                return StreamingResponse(
                    aiter(),
                    status_code=upstream.status_code,
                    media_type=upstream.headers.get("content-type"),
                    headers={
                        k: v for k, v in upstream.headers.items()
                        if k.lower() not in {"content-encoding",
                                             "transfer-encoding",
                                             "connection"}
                    },
                )
    
    async def proxy(
        self,
        remainder: str,
        request: Request
    ) -> Response:  # noqa: D401
        """Catch‑all router for POST endpoints.

        * Parses the JSON body to determine the target *model*.
        * Decides whether to stream or not based on the `stream` field or
          `text/event-stream` Accept header.
        * Delegates to :pymeth:`ProxyService.proxy_request` or
          :pymeth:`ProxyService.proxy_request_stream`.
        """
        try:
            body: Dict[str, Any] = await request.json()
        except json.JSONDecodeError as exc:  # pragma: no cover — FastAPI already validates
            raise HTTPException(status.HTTP_400_BAD_REQUEST, "Body must be valid JSON") from exc

        model = body.get("model")
        if not model:
            raise HTTPException(status.HTTP_400_BAD_REQUEST, "Missing 'model' in request body")

        wants_stream = body.get("stream") or request.headers.get("accept") == "text/event-stream"

        if wants_stream:
            return await self.proxy_request_stream(model, request)

        return await self.proxy_request(model, request)