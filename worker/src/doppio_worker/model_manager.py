"""Lazy model loading with a resident-memory budget and LRU eviction."""

from __future__ import annotations

import gc
import logging
import sys
import threading
import time
from collections import OrderedDict
from collections.abc import Callable
from dataclasses import dataclass
from typing import Any

from doppio_worker.stages.base import ModelRef

log = logging.getLogger(__name__)

Loader = Callable[[ModelRef, str], Any]


@dataclass
class _Entry:
    spec: ModelRef
    model: Any
    last_used: float


class ModelManager:
    """Keeps loaded models under ``max_resident_gb``, evicting least recently used.

    ``gpu_lock`` serializes accelerator work; MPS in particular handles
    concurrent kernels from several threads poorly.
    """

    def __init__(self, device: str, max_resident_gb: float = 6.0, clock: Callable[[], float] = time.time) -> None:
        if max_resident_gb <= 0:
            raise ValueError("max_resident_gb must be positive")
        self.device = device
        self.max_resident_gb = max_resident_gb
        self.gpu_lock = threading.Lock()
        self._clock = clock
        self._lock = threading.Lock()
        self._entries: OrderedDict[str, _Entry] = OrderedDict()

    def get(self, spec: ModelRef, loader: Loader) -> Any:
        with self._lock:
            entry = self._entries.get(spec.id)
            if entry is not None:
                self._entries.move_to_end(spec.id)
                entry.last_used = self._clock()
                return entry.model
            self._evict_for(spec.memory_gb)
            log.info("loading model %s (%s@%s) on %s", spec.id, spec.hf_repo, spec.revision or "default", self.device)
            model = loader(spec, self.device)
            self._entries[spec.id] = _Entry(spec, model, self._clock())
            return model

    def unload(self, model_id: str) -> bool:
        with self._lock:
            entry = self._entries.pop(model_id, None)
            if entry is None:
                return False
            del entry
            _release_accelerator_memory()
            return True

    def resident(self) -> list[tuple[str, float, float]]:
        with self._lock:
            return [(e.spec.id, e.spec.memory_gb, e.last_used) for e in self._entries.values()]

    def resident_gb(self) -> float:
        with self._lock:
            return sum(e.spec.memory_gb for e in self._entries.values())

    def _evict_for(self, needed_gb: float) -> None:
        used = sum(e.spec.memory_gb for e in self._entries.values())
        evicted = False
        while self._entries and used + needed_gb > self.max_resident_gb:
            model_id, entry = self._entries.popitem(last=False)
            used -= entry.spec.memory_gb
            log.info("evicting model %s to fit %.2f GB", model_id, needed_gb)
            del entry
            evicted = True
        if evicted:
            _release_accelerator_memory()


def _release_accelerator_memory() -> None:
    gc.collect()
    torch = sys.modules.get("torch")
    if torch is None:
        return
    if torch.cuda.is_available():
        torch.cuda.empty_cache()
    mps = getattr(torch, "mps", None)
    if mps is not None and torch.backends.mps.is_available():
        mps.empty_cache()
