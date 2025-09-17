# tests/test_proxy_service_instances.py
import pytest
from fastapi import HTTPException

from borg.proxy import ProxyService, RoundRobinSet


@pytest.fixture
def service() -> ProxyService:
    return ProxyService()


async def _listed_models(svc: ProxyService) -> set[str]:
    listing = await svc.list_models()
    return {row["id"] for row in listing["data"]}


# --- Desired behavior: adding endpoints creates buckets and lists models ---
@pytest.mark.asyncio
async def test_add_instance_creates_and_lists(service: ProxyService):
    await service.add_instance("http://e1:8000", "k1", ["m1", "m2"])
    await service.add_instance("http://e2:8000", "k2", ["m1"])  # m1 now has two endpoints

    assert "m1" in service._instances and isinstance(service._instances["m1"], RoundRobinSet)
    assert "m2" in service._instances and isinstance(service._instances["m2"], RoundRobinSet)
    assert await _listed_models(service) == {"m1", "m2"}


# --- Desired behavior: removing one endpoint keeps the model until empty ---
@pytest.mark.asyncio
async def test_remove_one_endpoint_keeps_model_until_empty(service: ProxyService):
    await service.add_instance("http://e1:8000", "k1", ["m1"])
    await service.add_instance("http://e2:8000", "k2", ["m1"])

    await service.remove_instance("http://e1:8000", models=["m1"])

    # Still listed because m1 has at least one endpoint
    assert await _listed_models(service) == {"m1"}

    # pick_endpoint still works and returns one of the remaining endpoints
    endpoint, _ = await service.pick_endpoint("m1")
    assert endpoint in {"http://e1:8000", "http://e2:8000"}


# --- Desired behavior: removing the last endpoint fully deletes the model ---
@pytest.mark.asyncio
async def test_remove_last_endpoint_deletes_model_and_listing(service: ProxyService):
    await service.add_instance("http://e1:8000", "k1", ["m1", "m2"])

    # Remove the only endpoint for m1
    await service.remove_instance("http://e1:8000", models=["m1"])

    assert await _listed_models(service) == {"m2"}

    # Public API: pick_endpoint should behave as if model doesn't exist
    with pytest.raises(KeyError):
        await service.pick_endpoint("m1")

    # Internal helper: pick_endpoint should raise a 404 HTTPException
    with pytest.raises(HTTPException) as ei:
        await service._choose("m1")
    
    assert ei.value.status_code == 404


# --- Desired behavior: global remove (models=None) removes endpoint everywhere and cleans up empties ---
@pytest.mark.asyncio
async def test_remove_instance_models_none_removes_everywhere_and_cleans_up(service: ProxyService):
    await service.add_instance("http://e1:8000", "k1", ["m1", "m2", "m3"])
    await service.add_instance("http://e2:8000", "k2", ["m2"])  # m2 has two endpoints

    # Remove e1 globally
    await service.remove_instance("http://e1:8000", models=None)

    # m1, m3 should be removed (now empty); m2 should remain with e2
    assert "m1" not in service._instances
    assert "m3" not in service._instances
    assert "m2" in service._instances

    assert await _listed_models(service) == {"m2"}

    # Sanity: m2 is routable
    ep, meta = await service._choose("m2")
    assert ep == "http://e2:8000"
    assert meta["apikey"] == "k2"


# --- Desired behavior: removing a non-existent endpoint is a no-op (and doesn't delete non-empty models) ---
@pytest.mark.asyncio
async def test_remove_nonexistent_endpoint_is_noop(service: ProxyService):
    await service.add_instance("http://e1:8000", "k1", ["m1"])
    await service.remove_instance("http://not-present:8000", models=["m1"])

    # Still listed and still routable
    assert await _listed_models(service) == {"m1"}
    ep, _ = await service._choose("m1")
    assert ep == "http://e1:8000"


# --- Desired behavior: list_models returns sorted names (alphabetical) of non-empty models ---
@pytest.mark.asyncio
async def test_list_models_is_sorted_and_filters_empty(service: ProxyService):
    await service.add_instance("http://e:8000", "k", ["zulu", "alpha", "bravo"])

    # Remove the only endpoint from 'bravo' to make it empty and expect it to disappear
    await service.remove_instance("http://e:8000", models=["bravo"])

    listing = await service.list_models()
    names = [row["id"] for row in listing["data"]]
    assert names == ["alpha", "zulu"]  # sorted ascending, 'bravo' filtered out


# --- Desired behavior: pick_endpoint raises KeyError when model unknown ---
@pytest.mark.asyncio
async def test_pick_endpoint_unknown_model_raises_keyerror(service: ProxyService):
    with pytest.raises(KeyError):
        await service.pick_endpoint("does-not-exist")
