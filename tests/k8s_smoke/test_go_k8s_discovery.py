import json
import os
import socket
import subprocess
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

REPO_ROOT = Path(__file__).resolve().parents[2]
GO_BINARY = REPO_ROOT / "bin" / "borg-go"


@dataclass
class RunningProxy:
    url: str
    process: subprocess.Popen
    stdout_path: Path
    stderr_path: Path

    def logs(self) -> str:
        stdout = _read_log(self.stdout_path)
        stderr = _read_log(self.stderr_path)
        return f"stdout:\n{stdout}\n\nstderr:\n{stderr}"


class DummyUpstream:
    def __init__(self, models: list[str]) -> None:
        self.models = models
        self._records: list[dict[str, Any]] = []
        self._model_requests: list[dict[str, Any]] = []
        self._lock = threading.Lock()
        owner = self

        class Handler(BaseHTTPRequestHandler):
            def do_GET(self) -> None:
                parsed = urlparse(self.path)
                if not parsed.path.endswith("/v1/models"):
                    self.send_error(404)
                    return

                headers = {key.lower(): value for key, value in self.headers.items()}
                owner.record_model_request({"path": parsed.path, "headers": headers})
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
                                "owned_by": "k8s-smoke",
                            }
                            for model in owner.models
                        ],
                    },
                )

            def do_POST(self) -> None:
                parsed = urlparse(self.path)
                if not parsed.path.endswith("/v1/chat/completions"):
                    self.send_error(404)
                    return

                raw_body = self.rfile.read(
                    int(self.headers.get("Content-Length", "0") or "0")
                )
                headers = {key.lower(): value for key, value in self.headers.items()}
                try:
                    parsed_body = json.loads(raw_body)
                except json.JSONDecodeError:
                    parsed_body = None

                record = {
                    "method": "POST",
                    "path": parsed.path,
                    "query": parsed.query,
                    "headers": headers,
                    "body": parsed_body,
                }
                owner.record(record)
                owner._write_json(
                    self,
                    200,
                    {
                        "upstream": "ok",
                        "path": parsed.path,
                        "auth": headers.get("authorization"),
                        "body": parsed_body,
                    },
                )

            def log_message(self, format: str, *args: object) -> None:
                return

        self.server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        self.thread = threading.Thread(
            target=self.server.serve_forever,
            name="dummy-openai-k8s-smoke",
            daemon=True,
        )
        self.thread.start()

    @property
    def host(self) -> str:
        return str(self.server.server_address[0])

    @property
    def port(self) -> str:
        return str(self.server.server_address[1])

    def close(self) -> None:
        self.server.shutdown()
        self.server.server_close()
        self.thread.join(timeout=5)

    def record(self, record: dict[str, Any]) -> None:
        with self._lock:
            self._records.append(record)

    def record_model_request(self, record: dict[str, Any]) -> None:
        with self._lock:
            self._model_requests.append(record)

    def last_record(self) -> dict[str, Any]:
        with self._lock:
            if not self._records:
                raise AssertionError("dummy upstream did not record a POST")
            return self._records[-1]

    def model_requests(self) -> list[dict[str, Any]]:
        with self._lock:
            return [dict(record) for record in self._model_requests]

    def _write_json(
        self,
        handler: BaseHTTPRequestHandler,
        status: int,
        payload: dict[str, Any],
    ) -> None:
        raw = json.dumps(payload, separators=(",", ":")).encode()
        handler.send_response(status)
        handler.send_header("Content-Type", "application/json")
        handler.send_header("Content-Length", str(len(raw)))
        handler.end_headers()
        handler.wfile.write(raw)


class FakeKubernetesAPI:
    def __init__(self) -> None:
        self._pods: list[dict[str, Any]] = []
        self._requests: list[dict[str, str]] = []
        self._fail_lists = False
        self._failed_request_count = 0
        self._lock = threading.Lock()
        owner = self

        class Handler(BaseHTTPRequestHandler):
            def do_GET(self) -> None:
                parsed = urlparse(self.path)
                namespace = _namespace_from_pod_list_path(parsed.path)
                if namespace is None:
                    self.send_error(404)
                    return

                selector = parse_qs(parsed.query).get("labelSelector", [""])[0]
                owner.record_request(namespace, selector)
                if owner.should_fail_lists():
                    owner.record_failed_request()
                    owner._write_json(
                        self,
                        500,
                        {
                            "kind": "Status",
                            "apiVersion": "v1",
                            "status": "Failure",
                            "message": "forced fake Kubernetes failure",
                        },
                    )
                    return

                items = [
                    pod
                    for pod in owner.pods()
                    if pod["metadata"].get("namespace") == namespace
                    and _matches_selector(pod, selector)
                ]
                owner._write_json(
                    self,
                    200,
                    {
                        "kind": "PodList",
                        "apiVersion": "v1",
                        "metadata": {"resourceVersion": "1"},
                        "items": items,
                    },
                )

            def log_message(self, format: str, *args: object) -> None:
                return

        self.server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        self.thread = threading.Thread(
            target=self.server.serve_forever,
            name="fake-kubernetes-api",
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

    def set_pods(self, pods: list[dict[str, Any]]) -> None:
        with self._lock:
            self._pods = pods

    def pods(self) -> list[dict[str, Any]]:
        with self._lock:
            return list(self._pods)

    def set_fail_lists(self, fail: bool) -> None:
        with self._lock:
            self._fail_lists = fail

    def should_fail_lists(self) -> bool:
        with self._lock:
            return self._fail_lists

    def record_request(self, namespace: str, selector: str) -> None:
        with self._lock:
            self._requests.append({"namespace": namespace, "selector": selector})

    def requests(self) -> list[dict[str, str]]:
        with self._lock:
            return [dict(record) for record in self._requests]

    def record_failed_request(self) -> None:
        with self._lock:
            self._failed_request_count += 1

    def failed_request_count(self) -> int:
        with self._lock:
            return self._failed_request_count

    def _write_json(
        self,
        handler: BaseHTTPRequestHandler,
        status: int,
        payload: dict[str, Any],
    ) -> None:
        raw = json.dumps(payload, separators=(",", ":")).encode()
        handler.send_response(status)
        handler.send_header("Content-Type", "application/json")
        handler.send_header("Content-Length", str(len(raw)))
        handler.end_headers()
        handler.wfile.write(raw)


def test_annotation_discovery_and_selector_request(
    tmp_path: Path,
) -> None:
    with _k8s_smoke_context(tmp_path) as ctx:
        upstream = ctx["upstream"]
        kube = ctx["kube"]
        proxy = ctx["proxy"]
        kube.set_pods(
            [
                _pod(
                    "annotated",
                    namespace="models",
                    pod_ip=upstream.host,
                    labels={"borg/expose": "vllm"},
                    annotations={
                        "borg/models": "alpha,beta",
                        "borg/apiport": upstream.port,
                    },
                ),
                _pod(
                    "not-selected",
                    namespace="models",
                    pod_ip=upstream.host,
                    labels={"borg/expose": "other"},
                    annotations={
                        "borg/models": "hidden",
                        "borg/apiport": upstream.port,
                    },
                ),
            ]
        )

        assert _wait_for_models(proxy, include={"alpha", "beta"}, absent={"hidden"}) == {
            "alpha",
            "beta",
        }
        assert {
            "namespace": "models",
            "selector": "borg/expose=vllm",
        } in kube.requests()


def test_automodel_discovery_queries_upstream(tmp_path: Path) -> None:
    with _k8s_smoke_context(tmp_path, upstream_models=["auto-alpha", "auto-beta"]) as ctx:
        upstream = ctx["upstream"]
        kube = ctx["kube"]
        proxy = ctx["proxy"]
        kube.set_pods(
            [
                _pod(
                    "automodel",
                    namespace="models",
                    pod_ip=upstream.host,
                    labels={"borg/expose": "vllm"},
                    annotations={"borg/apiport": upstream.port},
                )
            ]
        )

        assert _wait_for_models(
            proxy,
            include={"auto-alpha", "auto-beta"},
        ) == {"auto-alpha", "auto-beta"}
        model_requests = upstream.model_requests()
        assert model_requests
        assert model_requests[-1]["path"] == "/v1/models"
        assert model_requests[-1]["headers"]["authorization"] == "Bearer EMPTY"


def test_successful_refresh_removes_missing_pods(tmp_path: Path) -> None:
    with _k8s_smoke_context(tmp_path) as ctx:
        upstream = ctx["upstream"]
        kube = ctx["kube"]
        proxy = ctx["proxy"]
        kube.set_pods(
            [
                _pod(
                    "temporary",
                    namespace="models",
                    pod_ip=upstream.host,
                    labels={"borg/expose": "vllm"},
                    annotations={
                        "borg/models": "temporary-model",
                        "borg/apiport": upstream.port,
                    },
                )
            ]
        )
        _wait_for_models(proxy, include={"temporary-model"})

        kube.set_pods([])
        assert _wait_for_models(proxy, absent={"temporary-model"}) == set()


def test_failed_refresh_preserves_last_successful_snapshot(tmp_path: Path) -> None:
    with _k8s_smoke_context(tmp_path) as ctx:
        upstream = ctx["upstream"]
        kube = ctx["kube"]
        proxy = ctx["proxy"]
        kube.set_pods(
            [
                _pod(
                    "stable",
                    namespace="models",
                    pod_ip=upstream.host,
                    labels={"borg/expose": "vllm"},
                    annotations={
                        "borg/models": "stable-model",
                        "borg/apiport": upstream.port,
                    },
                )
            ]
        )
        _wait_for_models(proxy, include={"stable-model"})

        kube.set_pods([])
        kube.set_fail_lists(True)
        _wait_for_kube_failures(kube)
        assert "stable-model" in _model_ids(proxy)

        kube.set_fail_lists(False)
        assert _wait_for_models(proxy, absent={"stable-model"}) == set()


def test_endpoint_annotation_overrides_are_used_for_forwarding(
    tmp_path: Path,
) -> None:
    with _k8s_smoke_context(tmp_path) as ctx:
        upstream = ctx["upstream"]
        kube = ctx["kube"]
        proxy = ctx["proxy"]
        kube.set_pods(
            [
                _pod(
                    "with-base",
                    namespace="models",
                    pod_ip=upstream.host,
                    labels={"borg/expose": "vllm"},
                    annotations={
                        "borg/models": "override-model",
                        "borg/protocol": "http",
                        "borg/apiport": upstream.port,
                        "borg/apibase": "/openai",
                    },
                )
            ]
        )
        _wait_for_models(proxy, include={"override-model"})

        response = httpx.post(
            f"{proxy.url}/v1/chat/completions",
            json={"model": "override-model", "messages": []},
            timeout=5,
        )
        assert response.status_code == 200
        assert response.json()["upstream"] == "ok"

        record = upstream.last_record()
        assert record["path"] == "/openai/v1/chat/completions"
        assert record["headers"]["authorization"] == "Bearer EMPTY"


@contextmanager
def _k8s_smoke_context(
    tmp_path: Path,
    *,
    upstream_models: list[str] | None = None,
):
    if not GO_BINARY.exists():
        pytest.skip("bin/borg-go missing; run `go build -o bin/borg-go ./cmd/borg`")

    with ExitStack() as stack:
        kube = stack.enter_context(_fake_kubernetes_api())
        upstream = stack.enter_context(_dummy_upstream(upstream_models or ["smoke"]))

        kubeconfig = _write_kubeconfig(tmp_path, kube)
        config = _write_borg_config(tmp_path)
        proxy = stack.enter_context(_run_go_proxy(tmp_path, config, kubeconfig))

        yield {"kube": kube, "upstream": upstream, "proxy": proxy}


def _fake_kubernetes_api():
    class FakeKubernetesContext:
        def __enter__(self) -> FakeKubernetesAPI:
            self.kube = FakeKubernetesAPI()
            return self.kube

        def __exit__(self, *args: object) -> None:
            self.kube.close()

    return FakeKubernetesContext()


def _dummy_upstream(models: list[str]):
    class DummyUpstreamContext:
        def __enter__(self) -> DummyUpstream:
            self.upstream = DummyUpstream(models)
            return self.upstream

        def __exit__(self, *args: object) -> None:
            self.upstream.close()

    return DummyUpstreamContext()


def _run_go_proxy(tmp_path: Path, config: Path, kubeconfig: Path):
    class ProxyContext:
        def __enter__(self) -> RunningProxy:
            self.port = _free_port()
            stdout_path = tmp_path / "borg-go.stdout.log"
            stderr_path = tmp_path / "borg-go.stderr.log"
            env = _go_proxy_env(kubeconfig)
            command = [
                str(GO_BINARY),
                "--config",
                str(config),
                "--host",
                "127.0.0.1",
                "--port",
                str(self.port),
            ]

            with stdout_path.open("w") as stdout, stderr_path.open("w") as stderr:
                self.process = subprocess.Popen(
                    command,
                    cwd=REPO_ROOT,
                    env=env,
                    stdout=stdout,
                    stderr=stderr,
                    text=True,
                )

            proxy = RunningProxy(
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


def _write_borg_config(tmp_path: Path) -> Path:
    config = {
        "borg": {
            "auth_key": "EMPTY",
            "auth_prefix": "PROXY:",
            "update_interval": 1,
            "instances": [],
            "k8s_discover": [
                {
                    "namespace": "models",
                    "selector": "borg/expose=vllm",
                    "modelkey": "borg/models",
                }
            ],
        }
    }
    path = tmp_path / "config.yaml"
    path.write_text(yaml.safe_dump(config), encoding="utf-8")
    return path


def _write_kubeconfig(tmp_path: Path, kube: FakeKubernetesAPI) -> Path:
    kubeconfig = {
        "apiVersion": "v1",
        "kind": "Config",
        "clusters": [
            {
                "name": "fake",
                "cluster": {
                    "server": kube.url,
                    "insecure-skip-tls-verify": True,
                },
            }
        ],
        "contexts": [
            {
                "name": "fake",
                "context": {
                    "cluster": "fake",
                    "user": "fake",
                },
            }
        ],
        "current-context": "fake",
        "users": [{"name": "fake", "user": {}}],
    }
    path = tmp_path / "kubeconfig.yaml"
    path.write_text(yaml.safe_dump(kubeconfig), encoding="utf-8")
    return path


def _go_proxy_env(kubeconfig: Path) -> dict[str, str]:
    env = os.environ.copy()
    for key in (
        "AUTH_KEY",
        "BORG_AUTH_KEY",
        "PROXY_CONFIG",
        "PORT",
        "KUBECONFIG",
        "KUBERNETES_SERVICE_HOST",
        "KUBERNETES_SERVICE_PORT",
    ):
        env.pop(key, None)
    env["KUBECONFIG"] = str(kubeconfig)
    env["NO_PROXY"] = "127.0.0.1,localhost"
    env["no_proxy"] = "127.0.0.1,localhost"
    return env


def _pod(
    name: str,
    *,
    namespace: str,
    pod_ip: str,
    labels: dict[str, str],
    annotations: dict[str, str],
    phase: str = "Running",
) -> dict[str, Any]:
    return {
        "kind": "Pod",
        "apiVersion": "v1",
        "metadata": {
            "name": name,
            "namespace": namespace,
            "labels": labels,
            "annotations": annotations,
        },
        "status": {
            "phase": phase,
            "podIP": pod_ip,
        },
    }


def _namespace_from_pod_list_path(path: str) -> str | None:
    parts = path.strip("/").split("/")
    if len(parts) == 5 and parts[:3] == ["api", "v1", "namespaces"] and parts[4] == "pods":
        return parts[3]
    return None


def _matches_selector(pod: dict[str, Any], selector: str) -> bool:
    if not selector:
        return True

    labels = pod["metadata"].get("labels") or {}
    for requirement in selector.split(","):
        requirement = requirement.strip()
        if not requirement:
            continue
        key, separator, value = requirement.partition("=")
        if separator != "=" or labels.get(key.strip()) != value.strip():
            return False
    return True


def _wait_until_ready(proxy: RunningProxy) -> None:
    deadline = time.monotonic() + 10
    while time.monotonic() < deadline:
        if proxy.process.poll() is not None:
            raise AssertionError(f"Go proxy exited before readiness\n{proxy.logs()}")
        try:
            response = httpx.get(f"{proxy.url}/", timeout=0.25)
            if response.status_code == 200:
                return
        except httpx.HTTPError:
            pass
        time.sleep(0.05)

    _terminate_process(proxy.process)
    raise AssertionError(f"Go proxy did not become ready\n{proxy.logs()}")


def _wait_for_models(
    proxy: RunningProxy,
    *,
    include: set[str] | None = None,
    absent: set[str] | None = None,
) -> set[str]:
    include = include or set()
    absent = absent or set()
    deadline = time.monotonic() + 8
    last_models: set[str] = set()

    while time.monotonic() < deadline:
        if proxy.process.poll() is not None:
            raise AssertionError(f"Go proxy exited while waiting for models\n{proxy.logs()}")

        last_models = _model_ids(proxy)
        if include.issubset(last_models) and last_models.isdisjoint(absent):
            return last_models
        time.sleep(0.1)

    raise AssertionError(
        f"timed out waiting for models include={include} absent={absent}; "
        f"last models={last_models}\n{proxy.logs()}"
    )


def _wait_for_kube_failures(kube: FakeKubernetesAPI) -> None:
    deadline = time.monotonic() + 8
    while time.monotonic() < deadline:
        if kube.failed_request_count() > 0:
            return
        time.sleep(0.1)
    raise AssertionError("timed out waiting for fake Kubernetes list failure")


def _model_ids(proxy: RunningProxy) -> set[str]:
    response = httpx.get(f"{proxy.url}/v1/models", timeout=5)
    response.raise_for_status()
    return {item["id"] for item in response.json()["data"]}


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
