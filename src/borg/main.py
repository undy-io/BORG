"""Entry‑point for the **vLLM load‑balancing router**.

Run with:

    python -m mymodule --config config.yaml --host 0.0.0.0 --port 8000

`config.yaml` example::
    borg:
        auth_key: "saMmbffqDb0ZeqJM4abNz4gKV4PFzqz2gmeoGNiRo3I=",    # base64-url 32-byte AES-256 key
        auth_prefix: "BORG:"
        instances:
        - endpoint: "http://10.0.0.5:8000"
            apikey: "sk‑examplekey123"
            models: ["gpt-3.5-turbo", "gpt-3.5-turbo-0125"]

        - endpoint: "http://10.0.0.6:8000"
            apikey: "sk‑anotherkey456"
            models: ["gpt-4o", "gpt-4o-mini"]
        discover:
        - namespace: vllm-servers
          selector: borg/expose=yes
          modelkey: borg/models
"""

from __future__ import annotations

import argparse
import asyncio
import base64
import json
import logging
import os
from contextlib import asynccontextmanager
from pathlib import Path
from typing import Any, Dict, List, Mapping

import yaml  # type: ignore[import-untyped]
from fastapi import FastAPI, Request, Response
from pydantic import BaseModel

from .proxy import ProxyService  # local module containing the class built earlier

API_KEY_ENV = "API_KEY"
AUTH_KEY_ENV = "AUTH_KEY"
LEGACY_AUTH_KEY_ENV = "BORG_AUTH_KEY"
DEFAULT_KEY_VAL = "EMPTY"

logging.basicConfig(
    level=logging.INFO, format="%(asctime)s [%(levelname)s] %(name)s: %(message)s"
)

logger = logging.getLogger(__name__)

###############################################################################
# Configuration helpers
###############################################################################


def _parse_args() -> argparse.Namespace:  # executed only when run as a script
    parser = argparse.ArgumentParser(
        description="OpenAI‑compatible load‑balancing router over vLLM backends.",
    )
    parser.add_argument(
        "--config",
        "-c",
        default=os.getenv("PROXY_CONFIG", "config.yaml"),
        help="Path to YAML/JSON file containing config.",
    )

    parser.add_argument(
        "--host", default="0.0.0.0", help="Bind address (default: 0.0.0.0)"
    )
    parser.add_argument("--port", type=int, default=int(os.getenv("PORT", 8000)))
    parser.add_argument(
        "--reload", action="store_true", help="Enable uvicorn reload (dev only)"
    )

    return parser.parse_args()


def _get_apikey(inst: Mapping[str, str], default: str = DEFAULT_KEY_VAL) -> str:
    env_var = inst.get("apikeyEnv")
    if env_var:
        return os.getenv(env_var, default)
    return inst.get("apikey", default)


class ModelInfo(BaseModel):
    id: str
    object: str
    created: int | None
    owned_by: str


class ModelListResponse(BaseModel):
    object: str
    data: List[ModelInfo]


def create_app(config_path: str | None = None) -> FastAPI:
    """Create an isolated application instance.

    The exported module-level ``app`` below remains the production runtime
    singleton. Tests and local callers should prefer ``create_app()`` so each
    app gets its own proxy and discovery-service state.
    """

    proxy = ProxyService()
    services: list[Any] = []

    async def periodic_update(update_interval: int) -> None:
        """Periodically discover and refresh vLLM instances."""
        while True:
            for svc in services:
                try:
                    await svc.update(proxy)
                except Exception:
                    logger.exception("Service update failed")

            await asyncio.sleep(update_interval)

    @asynccontextmanager
    async def lifespan(app: FastAPI):
        app.state.config_path = getattr(app.state, "config_path", None) or os.getenv(
            "PROXY_CONFIG", "config.yaml"
        )

        cfg_path = Path(app.state.config_path)

        if not cfg_path.exists():
            raise FileNotFoundError(f"Config not found: {cfg_path}")

        with cfg_path.open("r", encoding="utf-8") as fh:
            cfg_data = (
                yaml.safe_load(fh) if cfg_path.suffix != ".json" else json.load(fh)
            ) or {}

        cfg = cfg_data.get("borg", {})

        # ─── auth key ───
        auth_key = (
            os.getenv(AUTH_KEY_ENV)
            or os.getenv(LEGACY_AUTH_KEY_ENV)
            or cfg.get("auth_key", "EMPTY")
        )
        app.state.auth_key = (
            base64.urlsafe_b64decode(auth_key) if auth_key != "EMPTY" else None
        )

        if app.state.auth_key:
            if len(app.state.auth_key) != 32:
                raise RuntimeError(
                    "auth_key must be 32‑byte AES‑256 key (base64‑url encoded)"
                )
            proxy.set_auth_key(app.state.auth_key)

        app.state.auth_prefix = cfg.get("auth_prefix", None)
        if app.state.auth_prefix:
            proxy.set_auth_prefix(app.state.auth_prefix)

        # ─── backends ───
        instances: List[Dict[str, Any]] = cfg.get("instances", []) or []
        apikey_default = os.getenv(API_KEY_ENV, DEFAULT_KEY_VAL)
        for inst in instances:
            await proxy.add_instance(
                endpoint=inst["endpoint"],
                apikey=_get_apikey(inst, default=apikey_default),
                models=inst["models"],
            )

        # ─── Background Task ───
        update_task = None
        update_interval = cfg.get("update_interval", -1)

        if update_interval > 0:
            k8s_discover = cfg.get("k8s_discover", None)
            if k8s_discover:
                try:
                    from .k8s_discovery import K8SDiscoveryService

                    services.append(K8SDiscoveryService(cfg["k8s_discover"]))
                except Exception:
                    logger.exception("Failed to load k8s discovery service")

            if services:
                update_task = asyncio.create_task(periodic_update(update_interval))

        logger.info("Loaded %d backend instances and auth key", len(instances))

        logger.info("Application startup complete.")
        yield

        if update_task:
            update_task.cancel()
            logger.info("Background update task cancelled.")

        logger.info("Application shutdown complete.")

    app = FastAPI(title="BORG proxy router", version="1.0.0", lifespan=lifespan)
    app.state.config_path = config_path or os.getenv("PROXY_CONFIG", "config.yaml")
    app.state.proxy = proxy
    app.state.services = services

    @app.get("/")
    async def _root() -> Dict[str, str]:
        return {"status": "ok", "detail": "Proxy router is running"}

    @app.get("/v1/models", response_model=ModelListResponse)
    async def list_models():
        """Return the union of every model currently registered."""
        return await proxy.list_models()

    @app.post("/v1/{remainder:path}")
    async def openai_proxy(
        remainder: str,
        request: Request,
    ) -> Response:  # noqa: D401
        proxy.require_auth(request)
        return await proxy.proxy(remainder, request)

    return app


def configure(config_path: str) -> FastAPI:
    return create_app(config_path)


# Runtime singleton for ASGI servers. Tests should prefer ``create_app()``.
app = create_app()
proxy = app.state.proxy
services = app.state.services
