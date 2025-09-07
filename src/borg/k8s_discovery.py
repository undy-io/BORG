
import asyncio
import aiohttp
import kubernetes
from kubernetes import client, config
from typing import Generator, Dict, List, Any

from dataclasses import dataclass

import logging
logger = logging.getLogger(__name__)

@dataclass
class Endpoint:
    endpoint: str
    models: List[str]
    apikey: str

class K8SDiscoveryService:
    def __init__(
        self,
        selectors: list
    ):
        self._selectors = selectors
        self._epmap = dict()

        # Load Kubernetes configuration
        try:
            config.load_incluster_config()
        except config.ConfigException:
            # Fallback to kube config if not in cluster
            try:
                config.load_kube_config()
            except Exception as e:
                logger.error(f"Failed to load Kubernetes config: {e}")
                raise

        self.k8s_core_v1 = client.CoreV1Api()

    @staticmethod
    async def _enum_models(
        endpoint: str,
        api_key: str = "EMPTY",
        timeout: int = 30
    ) -> List[str]:
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
            "Content-Type": "application/json"
        }
        
        async with aiohttp.ClientSession(
            timeout=aiohttp.ClientTimeout(total=timeout)) as session:
            try:
                async with session.get(f'{endpoint}models', headers=headers) as response:
                    response.raise_for_status()  # Raise exception for bad status codes
                    data = await response.json()
                    return list([e['id'] for e in data['data']])
            except aiohttp.ClientError as e:
                print(f"Request failed: {e}")
                raise
            except Exception as e:
                print(f"Unexpected error: {e}")
                raise
    
    async def _discover(
        self,
        namespace:str,
        selector:str,
        modelkey:str|None,
        automodel=True,
    ) -> Generator[Endpoint, None, None]:
        """
        Discover pods in the Kubernetes cluster
        """
        try:
            # Find pods with the vllm label
            pods = self.k8s_core_v1.list_namespaced_pod(
                namespace=namespace,
                label_selector=selector
            )
            
            for pod in pods.items:
                # Check pod status
                if pod.status.phase != 'Running':
                    continue

                pod_ip = pod.status.pod_ip
                labels = pod.metadata.labels
                annotations = pod.metadata.annotations

                if not annotations:
                    continue

                models = None
                if modelkey is not None:
                    models = annotations.get(modelkey, '').split(',')
                    models = list(filter(None, models))
                
                protocol = annotations.get('borg/protocol', 'http')
                apibase = annotations.get('borg/apibase', '/v1')
                apiport = annotations.get('borg/apiport', '8000')

                endpoint = f'{protocol}://{pod_ip}:{apiport}{apibase}/'
                
                if not models and automodel:
                    logger.info(f'Querying {endpoint} for models')
                    models = await K8SDiscoveryService._enum_models(
                        endpoint
                    )
                
                if not models:
                    continue
                
                yield Endpoint(
                    endpoint=endpoint,
                    models=models,
                    apikey='EMPTY'
                )
        except Exception as e:
            logger.error(f"Error discovering vLLM instances: {e}")

    async def discover(self, automodel=True):
        for selector in self._selectors:
            async for ep in self._discover(
                selector.get('namespace'),
                selector.get('selector'),
                selector.get('modelkey', None),
                automodel=automodel):
                yield ep

    @staticmethod
    def _epdiff(a, b) -> Dict[str, List[str]]:
        new = dict()

        for m in a.keys():
            bset = b.get(m, set())
            for ep in a[m]:
                if not ep in bset:
                    if not ep in new:
                        new[ep] = list()
                    new[ep].append(m)
        
        return new

    async def update(self, proxy):
        logger.info(f'Checking for new pods...')
        
        epmap = dict()

        async for ep in self.discover():
            for m in ep.models:
                if not m in epmap: #Something is wrong here, m is a dict not a string?
                    epmap[m] = set()
                epmap[m].add(ep.endpoint)
        
        add = K8SDiscoveryService._epdiff(epmap, self._epmap)
        rmv = K8SDiscoveryService._epdiff(self._epmap, epmap)

        for ep in rmv:
            await proxy.remove_instance(ep, models=rmv[ep])

        for ep in add:
            await proxy.add_instance(ep, 'EMPTY', models=add[ep])
        
        self._epmap = epmap
