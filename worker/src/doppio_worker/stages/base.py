"""Stage contract shared by all pipeline stages."""

from __future__ import annotations

from collections.abc import Callable, Mapping
from dataclasses import dataclass, field
from pathlib import Path
from typing import TYPE_CHECKING, Any

if TYPE_CHECKING:
    from doppio_worker.model_manager import ModelManager


class StageError(Exception):
    """Base class for stage failures reported to the orchestrator."""


class StageInputError(StageError):
    """The request was malformed: missing inputs, bad params, unreadable image."""


class StageCancelled(StageError):
    """The orchestrator cancelled the stage."""


class StageDependencyError(StageError):
    """An optional backend the stage needs is not installed."""


@dataclass(frozen=True)
class ModelRef:
    """Model resolved by the Go registry. The worker trusts it verbatim."""

    id: str
    family: str
    hf_repo: str
    revision: str = ""
    filename: str = ""
    memory_gb: float = 0.0


@dataclass(frozen=True)
class ArtifactOut:
    name: str
    rel_path: str
    media_type: str
    shape: tuple[int, ...] = ()
    dtype: str = ""


@dataclass
class StageOutput:
    artifacts: list[ArtifactOut] = field(default_factory=list)
    metadata: dict[str, Any] = field(default_factory=dict)


@dataclass
class StageContext:
    inputs: Mapping[str, Path]
    params: Mapping[str, Any]
    out_dir: Path
    device: str
    models: ModelManager | None = None
    model: ModelRef | None = None
    report: Callable[[float, str], None] = lambda _fraction, _message: None
    is_cancelled: Callable[[], bool] = lambda: False

    def check_cancelled(self) -> None:
        if self.is_cancelled():
            raise StageCancelled("stage cancelled by orchestrator")

    def input_path(self, name: str) -> Path:
        path = self.inputs.get(name)
        if path is None:
            raise StageInputError(f"missing required input {name!r}")
        if not path.is_file():
            raise StageInputError(f"input {name!r} does not exist: {path}")
        return path

    def optional_input(self, name: str) -> Path | None:
        path = self.inputs.get(name)
        if path is None:
            return None
        if not path.is_file():
            raise StageInputError(f"input {name!r} does not exist: {path}")
        return path

    def require_model(self) -> ModelRef:
        if self.model is None:
            raise StageInputError("stage requires a model but none was provided")
        if self.models is None:
            raise StageError("stage requires a model manager")
        return self.model

    def param(self, name: str, default: Any = None) -> Any:
        return self.params.get(name, default)


StageFn = Callable[[StageContext], StageOutput]
