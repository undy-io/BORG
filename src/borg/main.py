'''Entry‑point for the **vLLM load‑balancing router**.

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
          selector: borg/expose=vllm
          modelkey: borg/models
'''
from __future__ import annotations

import argparse
import asyncio
import base64
import json
import yaml
import os

import uvicorn
import logging

from pathlib import Path
from pydantic import BaseModel
from typing import Any, Dict, List

from fastapi import Depends, FastAPI, Request, Response, status

from .proxy import ProxyService  # local module containing the class built earlier

logging.basicConfig(
    level=logging.INFO,
    format='%(asctime)s [%(levelname)s] %(name)s: %(message)s'
)

logger = logging.getLogger(__name__)

###############################################################################
# Configuration helpers
###############################################################################

def _parse_args() -> argparse.Namespace:  # executed only when run as a script
    parser = argparse.ArgumentParser(
        description='OpenAI‑compatible load‑balancing router over vLLM backends.',
    )
    parser.add_argument(
        '--config',
        '-c',
        default=os.getenv('PROXY_CONFIG', 'config.yaml'),
        help='Path to YAML/JSON file containing config.',
    )

    parser.add_argument('--host', default='0.0.0.0', help='Bind address (default: 0.0.0.0)')
    parser.add_argument('--port', type=int, default=int(os.getenv('PORT', 8000)))
    parser.add_argument('--reload', action='store_true', help='Enable uvicorn reload (dev only)')

    return parser.parse_args()


###############################################################################
# Application & global objects
###############################################################################

proxy = ProxyService()
app = FastAPI(title='BORG proxy router', version='1.0.0')

services = []

# Periodic service updates
async def periodic_update(update_interval):
    '''
    Periodically discover vLLM instances
    '''
    while True:
        for svc in services:
            try:
                await svc.update(proxy)
            except Exception as e:
                logger.exception(f'Exception during service update {e}')

        await asyncio.sleep(update_interval)

@app.on_event('startup')
async def _load_config() -> None:  # noqa: D401
    app.state.config_path = getattr(app.state, "config_path", None) \
                            or os.getenv("PROXY_CONFIG", "config.yaml")

    cfg_path: Path = Path(app.state.config_path)  # type: ignore[attr-defined]

    if not cfg_path.exists():
        raise FileNotFoundError(f'Config not found: {cfg_path}')

    with cfg_path.open('r', encoding='utf-8') as fh:
        if cfg_path.suffix == '.json':
            cfg = json.load(fh)['borg']
        else:
            cfg = yaml.safe_load(fh)['borg']
    
    # ─── auth key ───
    auth_key = os.getenv('AUTH_KEY', cfg.get('auth_key', 'EMPTY'))
    app.state.auth_key = base64.urlsafe_b64decode(auth_key) \
        if auth_key != 'EMPTY' else None
        
    if app.state.auth_key:
        if len(app.state.auth_key) != 32:
            raise RuntimeError('auth_key must be 32‑byte AES‑256 key (base64‑url encoded)')
        proxy.set_auth_key(app.state.auth_key)

    app.state.auth_prefix = cfg.get('auth_prefix', None)
    if app.state.auth_prefix:
        proxy.set_auth_prefix(app.state.auth_prefix)

    # ─── backends ───
    instances: List[Dict[str, Any]] = cfg.get('instances', []) or list()
    for inst in instances:
        await proxy.add_instance(
            endpoint=inst['endpoint'],
            apikey=inst['apikey'],
            models=inst['models'],
        )
    
    update_interval = cfg.get('update_interval', -1)

    if update_interval > 0:
        k8s_discover = cfg.get('k8s_discover', None)
        if k8s_discover:
            try:
                from .k8s_discovery import K8SDiscoveryService
                services.append(K8SDiscoveryService(cfg['k8s_discover']))
            except Exception as e:
                logger.exception('Failed to load k8s discovery service', e)

        if services:
            asyncio.create_task(periodic_update(update_interval))

    logger.info('Loaded %d backend instances and auth key', len(instances))

###############################################################################
# Routes
###############################################################################

@app.get('/')
async def _root() -> Dict[str, str]:
    return {'status': 'ok', 'detail': 'Proxy router is running'}

class ModelInfo(BaseModel):
    id: str
    object: str
    created: int | None
    owned_by: str

class ModelListResponse(BaseModel):
    object: str
    data: List[ModelInfo]

@app.get('/v1/models', response_model=ModelListResponse)
async def list_models():
    '''Return the union of every model currently registered.'''
    return await proxy.list_models()

@app.post('/v1/{remainder:path}')
async def openai_proxy(
    remainder: str,
    request: Request,
    username: str = Depends(lambda req: proxy.require_auth(req)),
    ) -> Response:  # noqa: D401
    return await proxy.proxy(remainder, request)


###############################################################################
# Configure the app
###############################################################################

def configure(config_path: str):
    app.state.config_path = config_path
    return app

