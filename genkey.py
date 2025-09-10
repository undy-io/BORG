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
import yaml
import secrets
import sys
from pathlib import Path
from typing import Optional

from cryptography.hazmat.primitives.ciphers.aead import AESGCM
from kubernetes import client, config

NONCE_LEN = 12  # AES‑GCM standard

def _init_k8s() -> client.CoreV1Api:
    """Load the local kubeconfig and return a CoreV1Api handle."""
    try:
        config.load_kube_config()        # honours $KUBECONFIG / ~/.kube/config
    except Exception as exc:             # noqa: BLE001
        raise SystemExit(f"[generate_key] cannot load kubeconfig: {exc}") from exc
    return client.CoreV1Api()

def _get_config_info(
    v1: client.CoreV1Api,
    namespace: str,
    release: str,
    configmap_suffix: str,
) -> tuple[Optional[str], Optional[str]]:
    """
    Return (auth_key_name, auth_prefix) from <release><configmap_suffix>.

    If the ConfigMap or either field is absent, returns (None, None).
    """
    name = f"{release}{configmap_suffix}"
    try:
        cm = v1.read_namespaced_config_map(name, namespace)
    except client.exceptions.ApiException:
        # Not fatal – the operator may not have deployed the ConfigMap yet.
        return None, None

    raw_yaml = cm.data.get("config.yaml") if cm.data else None
    if not raw_yaml:
        return None, None

    try:
        cfg = yaml.safe_load(raw_yaml) or {}
    except yaml.YAMLError as exc:       # noqa: BLE001
        print(f"[generate_key] WARNING: cannot parse {name}/config.yaml: {exc}", file=sys.stderr)
        return None, None

    borg = cfg.get("borg", {})
    return borg.get("auth_key_from_env"), borg.get("auth_prefix")

def _get_key(
    v1: client.CoreV1Api,
    namespace: str,
    release: str,
    key_name: Optional[str] = None,
    secret_suffix: str = "-auth"
) -> bytes:
    """
    Load <key_name> from the Secret called "<release><secret_suffix>" in *namespace*.

    If *key_name* is omitted, the first key in the Secret's data is used.
    """

    secret_name = f"{release}{secret_suffix}"
    try:
        secret = v1.read_namespaced_secret(secret_name, namespace)
    except client.exceptions.ApiException as exc:
        raise SystemExit(
            f"[generate_key] cannot read Secret '{secret_name}' in namespace "
            f"'{namespace}': {exc.reason}",
        ) from exc

    # Pick the data key
    if not secret.data:
        raise SystemExit(f"[generate_key] Secret '{secret_name}' has no data fields")

    if key_name:
        if key_name not in secret.data:
            raise SystemExit(
                f"[generate_key] key '{key_name}' not found in Secret '{secret_name}'",
            )
        b64 = secret.data[key_name]
    else:
        # Take the first key (deterministic order in Python ≥3.7)
        key_name, b64 = next(iter(secret.data.items()))
        print(f"[generate_key] using key '{key_name}' from Secret '{secret_name}'", file=sys.stderr)
    
    try:
        key = base64.b64decode(b64)
    except Exception as exc:               # noqa: BLE001
        raise SystemExit(
            f"[generate_key] failed to base64-decode Secret data '{key_name}': {exc}",
        ) from exc

    if len(key) != 32:
        raise SystemExit(
            "[generate_key] extracted key is not 32 bytes "
            f"(got {len(key)}); ensure it is AES-256",
        )
    return key

def _load_key(cfg_path: Path) -> bytes:
    cfg = json.loads(cfg_path.read_text(encoding="utf‑8"))
    try:
        key = base64.urlsafe_b64decode(cfg["auth_key"])
        prefix = base64.urlsafe_b64decode(cfg["auth_prefix"])
    except (KeyError, ValueError) as exc:  # noqa: BLE001
        raise SystemExit("[generate_key] 'auth_key' missing or invalid in config") from exc
    if len(key) != 32:
        raise SystemExit("[generate_key] auth_key must be 32‑byte AES‑256 key")
    return key

def _make_token(username: str, key: bytes, prefix: str) -> str:
    payload = f"{prefix}{username}".encode()
    nonce = secrets.token_bytes(NONCE_LEN)
    ct_tag = AESGCM(key).encrypt(nonce, payload, None)
    return base64.urlsafe_b64encode(nonce + ct_tag).decode()

def main() -> None:  # noqa: D401
    ap = argparse.ArgumentParser(
        description="Generate a base64 API token for the vLLM proxy",
    )
    ap.add_argument("username", help="Client username to embed in the token")

    # k8s location
    ap.add_argument("-n", "--namespace", required=True, help="Kubernetes namespace")
    ap.add_argument("-r", "--release", required=True, help="Helm release name")

    # optional overrides
    ap.add_argument("--key-name", "-k", help="Secret data key (overrides ConfigMap)")
    ap.add_argument("--auth-prefix", help="Prefix for the payload (overrides ConfigMap)")
    ap.add_argument(
        "--secret-suffix",
        default="-auth",
        help="Suffix appended to <release> for the Secret (default: %(default)s)",
    )
    ap.add_argument(
        "--configmap-suffix",
        default="-config",
        help="Suffix appended to <release> for the ConfigMap (default: %(default)s)",
    )

    args = ap.parse_args()

    v1 = _init_k8s()

    # ── Discover defaults from the ConfigMap, if present ──
    cm_key, cm_prefix = _get_config_info(
        v1,
        namespace=args.namespace,
        release=args.release,
        configmap_suffix=args.configmap_suffix,
    )

    key_name   = args.key_name   or cm_key
    auth_prefix = args.auth_prefix or cm_prefix or "PROXY:"

    # ── Fetch the AES-256 key ──
    key = _get_key(
        v1,
        namespace=args.namespace,
        release=args.release,
        secret_suffix=args.secret_suffix,
        key_name=key_name,
    )

    # ── Mint & print the token ──
    print(_make_token(args.username, key, auth_prefix))


if __name__ == "__main__":
    main()