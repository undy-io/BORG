import base64
import importlib.util
from pathlib import Path
from types import SimpleNamespace


def _load_genkey_module():
    module_path = Path(__file__).parent.parent / "genkey.py"
    spec = importlib.util.spec_from_file_location("borg_genkey", module_path)
    module = importlib.util.module_from_spec(spec)
    assert spec is not None and spec.loader is not None
    spec.loader.exec_module(module)
    return module


class _FakeCoreV1Api:
    def __init__(self, secret):
        self._secret = secret

    def read_namespaced_secret(self, name, namespace):  # noqa: ANN001
        return self._secret


def test_get_key_supports_text_auth_key_secrets():
    genkey = _load_genkey_module()
    raw_key = b"\x01" * 32
    printable_auth_key = base64.urlsafe_b64encode(raw_key).decode()
    secret = SimpleNamespace(
        data={
            "BORG_AUTH_KEY": base64.b64encode(printable_auth_key.encode()).decode(),
        }
    )

    key = genkey._get_key(
        _FakeCoreV1Api(secret),
        namespace="default",
        release="borg",
        key_name="BORG_AUTH_KEY",
    )

    assert key == raw_key


def test_get_key_supports_legacy_raw_key_secrets():
    genkey = _load_genkey_module()
    raw_key = b"\x02" * 32
    secret = SimpleNamespace(
        data={
            "BORG_AUTH_KEY": base64.b64encode(raw_key).decode(),
        }
    )

    key = genkey._get_key(
        _FakeCoreV1Api(secret),
        namespace="default",
        release="borg",
        key_name="BORG_AUTH_KEY",
    )

    assert key == raw_key
