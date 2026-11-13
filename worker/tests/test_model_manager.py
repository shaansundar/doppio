import pytest

from doppio_worker.model_manager import ModelManager
from doppio_worker.stages.base import ModelRef


def _spec(model_id: str, gb: float) -> ModelRef:
    return ModelRef(id=model_id, family="fake", hf_repo=f"org/{model_id}", memory_gb=gb)


class CountingLoader:
    def __init__(self):
        self.calls: list[str] = []

    def __call__(self, spec, device):
        self.calls.append(spec.id)
        return {"id": spec.id, "device": device}


def test_get_caches_loaded_models():
    loader = CountingLoader()
    mm = ModelManager("cpu", max_resident_gb=4)
    a1 = mm.get(_spec("a", 1), loader)
    a2 = mm.get(_spec("a", 1), loader)
    assert a1 is a2
    assert loader.calls == ["a"]


def test_lru_eviction_respects_budget():
    ticks = iter(range(100))
    loader = CountingLoader()
    mm = ModelManager("cpu", max_resident_gb=3, clock=lambda: next(ticks))
    mm.get(_spec("a", 1.5), loader)
    mm.get(_spec("b", 1.0), loader)
    mm.get(_spec("a", 1.5), loader)  # touch a -> b becomes LRU
    mm.get(_spec("c", 1.0), loader)  # needs room: evict b
    assert [m for m, _, _ in mm.resident()] == ["a", "c"]
    assert mm.resident_gb() == pytest.approx(2.5)


def test_model_larger_than_budget_evicts_everything_but_still_loads():
    loader = CountingLoader()
    mm = ModelManager("cpu", max_resident_gb=1)
    mm.get(_spec("small", 0.5), loader)
    mm.get(_spec("huge", 5), loader)
    assert [m for m, _, _ in mm.resident()] == ["huge"]


def test_unload():
    mm = ModelManager("cpu")
    mm.get(_spec("a", 1), CountingLoader())
    assert mm.unload("a") is True
    assert mm.unload("a") is False
    assert mm.resident() == []


def test_invalid_budget():
    with pytest.raises(ValueError):
        ModelManager("cpu", max_resident_gb=0)
