import base64
import gzip
import json
import os
import socket
import subprocess
import sys
import threading
import time
from contextlib import ExitStack, contextmanager
from dataclasses import dataclass
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from typing import Any
from urllib.parse import parse_qs, urlparse

import httpx
import pytest
import yaml
from cryptography.hazmat.primitives.ciphers.aead import AESGCM

REPO_ROOT = Path(__file__).resolve().parents[2]
GO_BINARY = REPO_ROOT / "bin" / "borg-go"
SMOKE_MODELS = ["alpha", "openai/gpt-oss-20b"]
UPSTREAM_API_KEY = "sk-upstream"


@dataclass
class RunningProxy:
    name: str
    url: str
    process: subprocess.Popen
    stdout_path: Path
    stderr_path: Path

    def logs(self) -> str:
        stdout = _read_log(self.stdout_path)
        stderr = _read_log(self.stderr_path)
        return f"stdout:\n{stdout}\n\nstderr:\n{stderr}"


@dataclass
class ProxyPair:
    python: RunningProxy
    go: RunningProxy
    python_upstream: "DummyUpstream"
    go_upstream: "DummyUpstream"
    auth_key: bytes | None = None


class DummyUpstream:
    def __init__(self) -> None:
        self._records: list[dict[str, Any]] = []
        self._lock = threading.Lock()
        owner = self

        class Handler(BaseHTTPRequestHandler):
            def do_GET(self) -> None:
                parsed = urlparse(self.path)
                if parsed.path != "/v1/models":
                    self.send_error(404)
                    return

                owner._write_json(
                    self,
                    200,
                    {
                        "object": "list",
                        "data": [
                            {
                                "id": model,
                                "object": "model",
                                "created": None,
                                "owned_by": "dummy-openai",
                            }
                            for model in SMOKE_MODELS
                        ],
                    },
                )

            def do_POST(self) -> None:
                parsed = urlparse(self.path)
                if parsed.path != "/v1/chat/completions":
                    self.send_error(404)
                    return

                raw_body = self.rfile.read(
                    int(self.headers.get("Content-Length", "0") or "0")
                )
                headers = {
                    key.lower(): value for key, value in self.headers.items()
                }
                try:
                    parsed_body = json.loads(raw_body)
                except json.JSONDecodeError:
                    parsed_body = None

                record = {
                    "method": "POST",
                    "path": parsed.path,
                    "query": parsed.query,
                    "headers": headers,
                    "body_text": raw_body.decode("utf-8", errors="replace"),
                    "json": parsed_body,
                }
                owner.record(record)

                wants_stream = (
                    isinstance(parsed_body, dict)
                    and bool(parsed_body.get("stream"))
                ) or "text/event-stream" in headers.get("accept", "")
                query = parse_qs(parsed.query)
                response_connection = "response_connection" in query

                if wants_stream:
                    owner._write_stream(self, response_connection=response_connection)
                    return

                response = {
                    "upstream": "ok",
                    "method": "POST",
                    "path": parsed.path,
                    "query": parsed.query,
                    "auth": headers.get("authorization"),
                    "content_type": headers.get("content-type"),
                    "body": parsed_body,
                    "headers": {
                        "accept_encoding": headers.get("accept-encoding"),
                        "connection": headers.get("connection"),
                        "x_keep": headers.get("x-keep"),
                        "x_smoke_hop": headers.get("x-smoke-hop"),
                    },
                }
                owner._write_json(
                    self,
                    201,
                    response,
                    gzip_response="gzip" in query,
                    response_connection=response_connection,
                )

            def log_message(self, format: str, *args: object) -> None:
                return

        self.server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        self.thread = threading.Thread(
            target=self.server.serve_forever,
            name="dummy-openai-smoke",
            daemon=True,
        )
        self.thread.start()

    @property
    def url(self) -> str:
        host, port = self.server.server_address
        return f"http://{host}:{port}"

    def close(self) -> None:
        self.server.shutdown()
        self.server.server_close()
        self.thread.join(timeout=5)

    def record(self, record: dict[str, Any]) -> None:
        with self._lock:
            self._records.append(record)

    def clear_records(self) -> None:
        with self._lock:
            self._records.clear()

    def last_record(self) -> dict[str, Any]:
        with self._lock:
            if not self._records:
                raise AssertionError("dummy upstream did not record a request")
            return self._records[-1]

    def _write_json(
        self,
        handler: BaseHTTPRequestHandler,
        status: int,
        payload: dict[str, Any],
        *,
        gzip_response: bool = False,
        response_connection: bool = False,
    ) -> None:
        raw = json.dumps(payload, separators=(",", ":")).encode()
        handler.send_response(status)
        handler.send_header("Content-Type", "application/json")
        if response_connection:
            handler.send_header("Connection", "X-Upstream-Hop")
            handler.send_header("X-Upstream-Hop", "remove-me")
            handler.send_header("X-Response-Keep", "keep-me")
        if gzip_response:
            raw = gzip.compress(raw)
            handler.send_header("Content-Encoding", "gzip")
        handler.send_header("Content-Length", str(len(raw)))
        handler.end_headers()
        handler.wfile.write(raw)

    def _write_stream(
        self,
        handler: BaseHTTPRequestHandler,
        *,
        response_connection: bool = False,
    ) -> None:
        handler.send_response(200)
        handler.send_header("Content-Type", "text/event-stream")
        if response_connection:
            handler.send_header("Connection", "X-Upstream-Hop")
            handler.send_header("X-Upstream-Hop", "remove-me")
            handler.send_header("X-Response-Keep", "keep-me")
        handler.end_headers()
        chunks = [
            b'data: {"id":"smoke","choices":[{"delta":{"content":"Hi"}}]}\n\n',
            b'data: {"id":"smoke","choices":[{"delta":{"content":"!"}}]}\n\n',
            b"data: [DONE]\n\n",
        ]
        for chunk in chunks:
            handler.wfile.write(chunk)
            handler.wfile.flush()


@pytest.fixture(scope="module")
def proxy_pair(tmp_path_factory: pytest.TempPathFactory) -> ProxyPair:
    with _start_proxy_pair(tmp_path_factory, auth_key=None) as pair:
        yield pair


@pytest.fixture(scope="module")
def auth_proxy_pair(tmp_path_factory: pytest.TempPathFactory) -> ProxyPair:
    with _start_proxy_pair(tmp_path_factory, auth_key=b"\x07" * 32) as pair:
        yield pair


def test_root_health_matches(proxy_pair: ProxyPair) -> None:
    python_response, go_response = _request_both(proxy_pair, "GET", "/")
    assert python_response.status_code == go_response.status_code == 200
    assert python_response.json() == go_response.json()


def test_models_match(proxy_pair: ProxyPair) -> None:
    python_response, go_response = _request_both(proxy_pair, "GET", "/v1/models")
    assert python_response.status_code == go_response.status_code == 200
    assert python_response.json() == go_response.json()


@pytest.mark.parametrize(
    ("body", "detail"),
    [
        (b"{not-json", "Body must be valid JSON"),
        (b"[]", "Body must be valid JSON"),
        (b'{"messages":[]}', "Missing 'model' in request body"),
    ],
)
def test_request_validation_matches(
    proxy_pair: ProxyPair,
    body: bytes,
    detail: str,
) -> None:
    headers = {"Content-Type": "application/json"}
    python_response, go_response = _request_both(
        proxy_pair,
        "POST",
        "/v1/chat/completions",
        content=body,
        headers=headers,
    )
    assert python_response.status_code == go_response.status_code == 400
    assert python_response.json()["detail"] == go_response.json()["detail"] == detail


def test_unknown_model_matches(proxy_pair: ProxyPair) -> None:
    payload = {"model": "missing", "messages": []}
    python_response, go_response = _request_both(
        proxy_pair,
        "POST",
        "/v1/chat/completions",
        json=payload,
    )
    assert python_response.status_code == go_response.status_code == 404
    assert "Unknown model" in python_response.json()["detail"]
    assert "Unknown model" in go_response.json()["detail"]


def test_non_streaming_forwarding_matches(proxy_pair: ProxyPair) -> None:
    payload = {
        "model": "openai/gpt-oss-20b",
        "messages": [{"role": "user", "content": "hello"}],
    }
    headers = {
        "Authorization": "Bearer inbound",
        "Content-Type": "application/json",
        "X-Keep": "keep-me",
    }
    python_response, go_response = _request_both(
        proxy_pair,
        "POST",
        "/v1/chat/completions?trace=1",
        json=payload,
        headers=headers,
    )
    assert python_response.status_code == go_response.status_code == 201

    python_body = python_response.json()
    go_body = go_response.json()
    for key in ("method", "path", "query", "auth", "content_type", "body"):
        assert python_body[key] == go_body[key]
    assert go_body["auth"] == f"Bearer {UPSTREAM_API_KEY}"
    assert go_body["path"] == "/v1/chat/completions"
    assert go_body["query"] == "trace=1"
    assert go_body["body"] == payload
    assert go_body["headers"]["x_keep"] == "keep-me"


def test_streaming_via_body_flag_matches(proxy_pair: ProxyPair) -> None:
    payload = {
        "model": "openai/gpt-oss-20b",
        "stream": True,
        "messages": [{"role": "user", "content": "hello"}],
    }
    python_status, python_text = _stream_response(
        proxy_pair.python.url,
        payload,
        headers=None,
    )
    go_status, go_text = _stream_response(proxy_pair.go.url, payload, headers=None)
    assert python_status == go_status == 200
    assert python_text == go_text
    assert "[DONE]" in go_text


def test_streaming_via_accept_header_matches(proxy_pair: ProxyPair) -> None:
    payload = {
        "model": "openai/gpt-oss-20b",
        "messages": [{"role": "user", "content": "hello"}],
    }
    headers = {"Accept": "text/event-stream; charset=utf-8"}
    python_status, python_text = _stream_response(
        proxy_pair.python.url,
        payload,
        headers=headers,
    )
    go_status, go_text = _stream_response(proxy_pair.go.url, payload, headers=headers)
    assert python_status == go_status == 200
    assert python_text == go_text
    assert "[DONE]" in go_text


def test_auth_behavior_matches(auth_proxy_pair: ProxyPair) -> None:
    assert auth_proxy_pair.auth_key is not None
    payload = {
        "model": "openai/gpt-oss-20b",
        "messages": [{"role": "user", "content": "hello"}],
    }

    missing_python, missing_go = _request_both(
        auth_proxy_pair,
        "POST",
        "/v1/chat/completions",
        json=payload,
    )
    assert missing_python.status_code == missing_go.status_code == 401
    assert missing_python.json() == missing_go.json() == {"detail": "Missing API key"}

    token = _mint_auth_token(auth_proxy_pair.auth_key, "PROXY:", "alice")
    headers = {"Authorization": f"Bearer {token}"}
    valid_python, valid_go = _request_both(
        auth_proxy_pair,
        "POST",
        "/v1/chat/completions",
        json=payload,
        headers=headers,
    )
    assert valid_python.status_code == valid_go.status_code == 201
    assert valid_python.json()["auth"] == valid_go.json()["auth"]
    assert valid_go.json()["auth"] == f"Bearer {UPSTREAM_API_KEY}"


def test_go_non_streaming_compression_delta(proxy_pair: ProxyPair) -> None:
    proxy_pair.go_upstream.clear_records()
    response = httpx.post(
        f"{proxy_pair.go.url}/v1/chat/completions?gzip=1",
        json={
            "model": "openai/gpt-oss-20b",
            "messages": [{"role": "user", "content": "hello"}],
        },
        headers={"Accept-Encoding": "gzip, br"},
        timeout=5,
    )
    assert response.status_code == 201
    assert response.headers.get("Content-Encoding") is None
    assert response.json()["upstream"] == "ok"
    assert proxy_pair.go_upstream.last_record()["headers"]["accept-encoding"] == "gzip"


def test_go_streaming_identity_delta(proxy_pair: ProxyPair) -> None:
    proxy_pair.go_upstream.clear_records()
    status, text = _stream_response(
        proxy_pair.go.url,
        {
            "model": "openai/gpt-oss-20b",
            "stream": True,
            "messages": [{"role": "user", "content": "hello"}],
        },
        headers={"Accept-Encoding": "gzip"},
    )
    assert status == 200
    assert "[DONE]" in text
    assert (
        proxy_pair.go_upstream.last_record()["headers"]["accept-encoding"]
        == "identity"
    )


def test_go_request_connection_extensions_are_stripped(
    proxy_pair: ProxyPair,
) -> None:
    proxy_pair.go_upstream.clear_records()
    response = httpx.post(
        f"{proxy_pair.go.url}/v1/chat/completions",
        json={
            "model": "openai/gpt-oss-20b",
            "messages": [{"role": "user", "content": "hello"}],
        },
        headers={
            "Connection": "X-Smoke-Hop",
            "X-Smoke-Hop": "remove-me",
            "X-Keep": "keep-me",
        },
        timeout=5,
    )
    assert response.status_code == 201
    record_headers = proxy_pair.go_upstream.last_record()["headers"]
    assert "x-smoke-hop" not in record_headers
    assert "connection" not in record_headers
    assert record_headers["x-keep"] == "keep-me"


def test_go_response_connection_extensions_are_stripped(
    proxy_pair: ProxyPair,
) -> None:
    response = httpx.post(
        f"{proxy_pair.go.url}/v1/chat/completions?response_connection=1",
        json={
            "model": "openai/gpt-oss-20b",
            "messages": [{"role": "user", "content": "hello"}],
        },
        timeout=5,
    )
    assert response.status_code == 201
    assert "connection" not in response.headers
    assert "x-upstream-hop" not in response.headers
    assert response.headers["x-response-keep"] == "keep-me"


@contextmanager
def _start_proxy_pair(
    tmp_path_factory: pytest.TempPathFactory,
    *,
    auth_key: bytes | None,
):
    if not GO_BINARY.exists():
        pytest.skip("bin/borg-go missing; run `go build -o bin/borg-go ./cmd/borg`")

    tmp_path = tmp_path_factory.mktemp("local-smoke")
    with ExitStack() as stack:
        python_upstream = stack.enter_context(_dummy_upstream())
        go_upstream = stack.enter_context(_dummy_upstream())

        python_config = _write_config(tmp_path, "python.yaml", python_upstream, auth_key)
        go_config = _write_config(tmp_path, "go.yaml", go_upstream, auth_key)

        python_proxy = stack.enter_context(
            _run_proxy(
                name="python",
                tmp_path=tmp_path,
                command=[
                    sys.executable,
                    "-m",
                    "borg",
                    "--config",
                    str(python_config),
                    "--host",
                    "127.0.0.1",
                    "--port",
                ],
                python_path=True,
            )
        )
        go_proxy = stack.enter_context(
            _run_proxy(
                name="go",
                tmp_path=tmp_path,
                command=[
                    str(GO_BINARY),
                    "--config",
                    str(go_config),
                    "--host",
                    "127.0.0.1",
                    "--port",
                ],
                python_path=False,
            )
        )
        yield ProxyPair(
            python=python_proxy,
            go=go_proxy,
            python_upstream=python_upstream,
            go_upstream=go_upstream,
            auth_key=auth_key,
        )


def _dummy_upstream():
    class DummyUpstreamContext:
        def __enter__(self) -> DummyUpstream:
            self.upstream = DummyUpstream()
            return self.upstream

        def __exit__(self, *args: object) -> None:
            self.upstream.close()

    return DummyUpstreamContext()


def _run_proxy(
    *,
    name: str,
    tmp_path: Path,
    command: list[str],
    python_path: bool,
):
    class ProxyContext:
        def __enter__(self) -> RunningProxy:
            self.port = _free_port()
            stdout_path = tmp_path / f"{name}.stdout.log"
            stderr_path = tmp_path / f"{name}.stderr.log"
            full_command = [*command, str(self.port)]
            env = _proxy_env(python_path=python_path)

            with stdout_path.open("w") as stdout, stderr_path.open("w") as stderr:
                self.process = subprocess.Popen(
                    full_command,
                    cwd=REPO_ROOT,
                    env=env,
                    stdout=stdout,
                    stderr=stderr,
                    text=True,
                )

            proxy = RunningProxy(
                name=name,
                url=f"http://127.0.0.1:{self.port}",
                process=self.process,
                stdout_path=stdout_path,
                stderr_path=stderr_path,
            )
            _wait_until_ready(proxy)
            return proxy

        def __exit__(self, *args: object) -> None:
            _terminate_process(self.process)

    return ProxyContext()


def _write_config(
    tmp_path: Path,
    filename: str,
    upstream: DummyUpstream,
    auth_key: bytes | None,
) -> Path:
    auth_key_text = "EMPTY"
    if auth_key is not None:
        auth_key_text = base64.urlsafe_b64encode(auth_key).decode()

    config = {
        "borg": {
            "auth_key": auth_key_text,
            "auth_prefix": "PROXY:",
            "update_interval": -1,
            "instances": [
                {
                    "endpoint": upstream.url,
                    "apikey": UPSTREAM_API_KEY,
                    "models": SMOKE_MODELS,
                }
            ],
            "k8s_discover": [],
        }
    }
    path = tmp_path / filename
    path.write_text(yaml.safe_dump(config), encoding="utf-8")
    return path


def _proxy_env(*, python_path: bool) -> dict[str, str]:
    env = os.environ.copy()
    for key in ("AUTH_KEY", "BORG_AUTH_KEY", "PROXY_CONFIG", "PORT"):
        env.pop(key, None)
    env["PYTHONUNBUFFERED"] = "1"
    if python_path:
        current = env.get("PYTHONPATH")
        src_path = str(REPO_ROOT / "src")
        env["PYTHONPATH"] = src_path if not current else f"{src_path}{os.pathsep}{current}"
    return env


def _wait_until_ready(proxy: RunningProxy) -> None:
    deadline = time.monotonic() + 10
    while time.monotonic() < deadline:
        if proxy.process.poll() is not None:
            raise AssertionError(
                f"{proxy.name} proxy exited before readiness\n{proxy.logs()}"
            )
        try:
            response = httpx.get(f"{proxy.url}/", timeout=0.25)
            if response.status_code == 200:
                return
        except httpx.HTTPError:
            pass
        time.sleep(0.05)

    _terminate_process(proxy.process)
    raise AssertionError(f"{proxy.name} proxy did not become ready\n{proxy.logs()}")


def _terminate_process(process: subprocess.Popen) -> None:
    if process.poll() is not None:
        return
    process.terminate()
    try:
        process.wait(timeout=5)
    except subprocess.TimeoutExpired:
        process.kill()
        process.wait(timeout=5)


def _free_port() -> int:
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as sock:
        sock.bind(("127.0.0.1", 0))
        return int(sock.getsockname()[1])


def _read_log(path: Path) -> str:
    if not path.exists():
        return ""
    return path.read_text(encoding="utf-8", errors="replace")[-4000:]


def _request_both(
    pair: ProxyPair,
    method: str,
    path: str,
    **kwargs: Any,
) -> tuple[httpx.Response, httpx.Response]:
    with httpx.Client(timeout=5) as client:
        python_response = client.request(method, f"{pair.python.url}{path}", **kwargs)
        go_response = client.request(method, f"{pair.go.url}{path}", **kwargs)
    return python_response, go_response


def _stream_response(
    base_url: str,
    payload: dict[str, Any],
    *,
    headers: dict[str, str] | None,
) -> tuple[int, str]:
    with httpx.Client(timeout=5) as client:
        with client.stream(
            "POST",
            f"{base_url}/v1/chat/completions",
            json=payload,
            headers=headers,
        ) as response:
            text = "".join(response.iter_text())
            return response.status_code, text


def _mint_auth_token(key: bytes, prefix: str, username: str) -> str:
    nonce = os.urandom(12)
    plaintext = f"{prefix}{username}".encode()
    ciphertext = AESGCM(key).encrypt(nonce, plaintext, None)
    return base64.urlsafe_b64encode(nonce + ciphertext).decode()
