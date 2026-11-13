"""Stage registry. Stages import heavy dependencies lazily inside ``run``."""

from __future__ import annotations

from doppio_worker.stages import geometry, ingest, maps
from doppio_worker.stages.base import StageFn

STAGES: dict[str, StageFn] = {
    "ingest": ingest.run,
    "geometry": geometry.run,
    "maps": maps.run,
}
