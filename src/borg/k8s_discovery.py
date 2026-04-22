import logging
from collections.abc import AsyncGenerator
from dataclasses import dataclass
from typing import Any

import aiohttp
from kubernetes import client, config

logger = logging.getLogger(__name__)


@dataclass
class Endpoint:
    endpoint: str
    models: list[str]
    apikey: str


class K8SDiscoveryService:
    def __init__(
        self,
        selectors: list[dict[str, Any]],
    ) -> None:
        self._selectors = selectors
        self._epmap: dict[str, set[str]] = {}

        # Load Kubernetes configuration
        try:
            config.load_incluster_config()
        except config.ConfigException:
            # Fallback to kube config if not in cluster
            try:
                config.load_kube_config()
            except Exception:
                logger.exception("Failed to load Kubernetes config")
                raise

        self.k8s_core_v1 = client.CoreV1Api()

    @staticmethod
    async def _enum_models(
        endpoint: str,
        api_key: str = "EMPTY",
        timeout: int = 30,
        models_ep: str = "/v1/models",
    ) -> list[str]:
        """
        Query OpenAI-compatible endpoint for available models.

        Args:
            endpoint: The models endpoint URL
            api_key: API key for authentication
            timeout: Request timeout in seconds

        Returns:
            Dictionary containing models response
        """
        headers = {
            "Authorization": f"Bearer {api_key}",
            "Content-Type": "application/json",
        }

        async with aiohttp.ClientSession(
            timeout=aiohttp.ClientTimeout(total=timeout)
        ) as session:
            try:
                async with session.get(
                    f"{endpoint}{models_ep}", headers=headers
                ) as response:
                    response.raise_for_status()  # Raise exception for bad status codes
                    data = await response.json()
                    return list([e["id"] for e in data["data"]])
            except aiohttp.ClientError:
                logger.exception("Request failed while enumerating models")
                raise
            except Exception:
                logger.exception("Unexpected error while enumerating models")
                raise

    async def _discover(
        self,
        namespace: str,
        selector: str,
        modelkey: str | None,
        automodel: bool = True,
    ) -> AsyncGenerator[Endpoint, None]:
        """
        Discover pods in the Kubernetes cluster
        """
        try:
            pods = self.k8s_core_v1.list_namespaced_pod(
                namespace=namespace, label_selector=selector
            )
        except Exception:
            logger.exception("Error discovering vLLM instances")
            raise

        for pod in pods.items:
            # Check pod status
            if pod.status.phase != "Running":
                continue

            pod_ip = pod.status.pod_ip
            annotations = pod.metadata.annotations

            if not annotations:
                continue

            models = None
            if modelkey is not None:
                models = annotations.get(modelkey, "").split(",")
                models = list(filter(None, models))

            protocol = annotations.get("borg/protocol", "http")
            apibase = annotations.get("borg/apibase", "")
            apiport = annotations.get("borg/apiport", "8000")

            endpoint = f"{protocol}://{pod_ip}:{apiport}{apibase}"

            if not models and automodel:
                logger.info(f"Querying {endpoint} for models")
                models = await K8SDiscoveryService._enum_models(endpoint)

            if not models:
                continue

            yield Endpoint(endpoint=endpoint, models=models, apikey="EMPTY")

    async def discover(self, automodel: bool = True) -> AsyncGenerator[Endpoint, None]:
        for selector in self._selectors:
            async for ep in self._discover(
                selector.get("namespace", "default"),
                selector.get("selector", ""),
                selector.get("modelkey"),
                automodel=automodel,
            ):
                yield ep

    @staticmethod
    def _epdiff(a: dict[str, set[str]], b: dict[str, set[str]]) -> dict[str, list[str]]:
        new: dict[str, list[str]] = {}

        for m in a.keys():
            bset = b.get(m, set())
            for ep in a[m]:
                if ep not in bset:
                    if ep not in new:
                        new[ep] = list()
                    new[ep].append(m)

        return new

    async def update(self, proxy: Any) -> None:
        logger.info("Checking for new pods...")

        epmap: dict[str, set[str]] = {}

        try:
            async for ep in self.discover():
                for model in ep.models:
                    if model not in epmap:
                        epmap[model] = set()
                    epmap[model].add(ep.endpoint)
        except Exception:
            logger.warning(
                "Discovery refresh failed; preserving previous endpoint snapshot"
            )
            return

        add = K8SDiscoveryService._epdiff(epmap, self._epmap)
        rmv = K8SDiscoveryService._epdiff(self._epmap, epmap)

        for endpoint in rmv:
            await proxy.remove_instance(endpoint, models=rmv[endpoint])

        for endpoint in add:
            await proxy.add_instance(endpoint, "EMPTY", models=add[endpoint])

        self._epmap = epmap
