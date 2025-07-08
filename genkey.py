"""Utility script to **mint client API keys** for the vLLM proxy.

Example::

    python -m mymodule.generate_key user123 \
           --config instances.json > api_key.txt

The script reads the *auth_key* from the same JSON config the proxy uses,
encrypts ``PROXY:<username>`` with AES‑256‑GCM, prefixes the nonce, and outputs
a URL‑safe base64 token ready to be used as an `Authorization: Bearer` header.
"""
from __future__ import annotations

import argparse
import base64
import json
import secrets
from pathlib import Path

from cryptography.hazmat.primitives.ciphers.aead import AESGCM

NONCE_LEN = 12  # AES‑GCM standard


def _load_key(cfg_path: Path) -> bytes:
    cfg = json.loads(cfg_path.read_text(encoding="utf‑8"))
    try:
        key = base64.urlsafe_b64decode(cfg["auth_key"])
    except (KeyError, ValueError) as exc:  # noqa: BLE001
        raise SystemExit("[generate_key] 'auth_key' missing or invalid in config") from exc
    if len(key) != 32:
        raise SystemExit("[generate_key] auth_key must be 32‑byte AES‑256 key")
    return key


def _make_token(username: str, key: bytes) -> str:
    payload = f"PROXY:{username}".encode()
    nonce = secrets.token_bytes(NONCE_LEN)
    ct_tag = AESGCM(key).encrypt(nonce, payload, None)  # ← returns ciphertext||tag
    token_bytes = nonce + ct_tag
    return base64.urlsafe_b64encode(token_bytes).decode()


def main() -> None:  # noqa: D401
    ap = argparse.ArgumentParser(
        description="Generate a base64 API key for the vLLM proxy",
    )
    ap.add_argument("username", help="Client username to embed in the token")
    ap.add_argument(
        "--config",
        "-c",
        default="instances.json",
        help="Path to the same JSON config used by the proxy server",
    )
    args = ap.parse_args()

    key = _load_key(Path(args.config))
    print(_make_token(args.username, key))


if __name__ == "__main__":
    main()
