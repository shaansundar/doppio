"""gRPC servicer exposing pipeline stages to the Go orchestrator."""

from __future__ import annotations

import asyncio
import json
import logging
import threading
import time
from collections.abc import AsyncIterator, Mapping
from pathlib import Path
from typing import Any

import grpc

from doppio_worker import __version__
from doppio_worker.device import torch_version
from doppio_worker.gen import worker_pb2, worker_pb2_grpc
from doppio_worker.model_manager import ModelManager
from doppio_worker.stages.base import (
    ModelRef,
    StageCancelled,
    StageContext,
    StageDependencyError,
    StageFn,
    StageInputError,
    StageOutput,
)

log = logging.getLogger(__name__)


def _model_from_proto(spec: worker_pb2.ModelSpec) -> ModelRef | None:
    if not spec.id:
        return None
    return ModelRef(
        id=spec.id,
        family=spec.family,
        hf_repo=spec.hf_repo,
        revision=spec.revision,
        filename=spec.filename,
        memory_gb=spec.memory_gb,
    )


def _result_proto(output: StageOutput, elapsed: float) -> worker_pb2.StageResult:
    return worker_pb2.StageResult(
        artifacts=[
            worker_pb2.Artifact(
                name=a.name,
                rel_path=a.rel_path,
                media_type=a.media_type,
                shape=list(a.shape),
                dtype=a.dtype,
            )
            for a in output.artifacts
        ],
        metadata_json=json.dumps(output.metadata, allow_nan=False, default=float),
        elapsed_seconds=elapsed,
    )


class WorkerServicer(worker_pb2_grpc.WorkerServiceServicer):
    def __init__(self, stages: Mapping[str, StageFn], models: ModelManager, device: str) -> None:
        self._stages = dict(stages)
        self._models = models
        self._device = device

    async def Health(self, request: worker_pb2.HealthRequest, context: grpc.aio.ServicerContext) -> worker_pb2.HealthResponse:
        return worker_pb2.HealthResponse(
            worker_version=__version__,
            device=self._device,
            torch_version=torch_version(),
            stages=sorted(self._stages),
        )

    async def ListResident(self, request: worker_pb2.ListResidentRequest, context: grpc.aio.ServicerContext) -> worker_pb2.ListResidentResponse:
        return worker_pb2.ListResidentResponse(
            models=[
                worker_pb2.ResidentModel(model_id=mid, memory_gb=gb, last_used_unix=used)
                for mid, gb, used in self._models.resident()
            ]
        )

    async def UnloadModel(self, request: worker_pb2.UnloadModelRequest, context: grpc.aio.ServicerContext) -> worker_pb2.UnloadModelResponse:
        unloaded = await asyncio.to_thread(self._models.unload, request.model_id)
        return worker_pb2.UnloadModelResponse(unloaded=unloaded)

    async def RunStage(
        self, request: worker_pb2.RunStageRequest, context: grpc.aio.ServicerContext
    ) -> AsyncIterator[worker_pb2.RunStageResponse]:
        stage_fn = self._stages.get(request.stage)
        if stage_fn is None:
            await context.abort(grpc.StatusCode.INVALID_ARGUMENT, f"unknown stage {request.stage!r}")
        out_dir = Path(request.out_dir)
        if not out_dir.is_absolute() or not out_dir.is_dir():
            await context.abort(grpc.StatusCode.INVALID_ARGUMENT, "out_dir must be an existing absolute directory")
        try:
            params: Any = json.loads(request.params_json or "{}")
        except json.JSONDecodeError as exc:
            await context.abort(grpc.StatusCode.INVALID_ARGUMENT, f"params_json is not valid JSON: {exc}")
        if not isinstance(params, dict):
            await context.abort(grpc.StatusCode.INVALID_ARGUMENT, "params_json must be a JSON object")

        loop = asyncio.get_running_loop()
        events: asyncio.Queue[tuple[str, Any]] = asyncio.Queue()
        cancelled = threading.Event()
        context.add_done_callback(lambda _ctx: cancelled.set())

        def report(fraction: float, message: str) -> None:
            loop.call_soon_threadsafe(events.put_nowait, ("progress", (fraction, message)))

        stage_ctx = StageContext(
            inputs={name: Path(path) for name, path in request.inputs.items()},
            params=params,
            out_dir=out_dir,
            device=self._device,
            models=self._models,
            model=_model_from_proto(request.model),
            report=report,
            is_cancelled=cancelled.is_set,
        )

        def work() -> None:
            started = time.perf_counter()
            try:
                output = stage_fn(stage_ctx)
                loop.call_soon_threadsafe(events.put_nowait, ("done", (output, time.perf_counter() - started)))
            except BaseException as exc:  # noqa: BLE001 - forwarded to the event loop
                loop.call_soon_threadsafe(events.put_nowait, ("error", exc))

        worker = loop.run_in_executor(None, work)
        try:
            while True:
                kind, payload = await events.get()
                if kind == "progress":
                    fraction, message = payload
                    yield worker_pb2.RunStageResponse(
                        progress=worker_pb2.StageProgress(fraction=float(fraction), message=str(message))
                    )
                elif kind == "done":
                    output, elapsed = payload
                    yield worker_pb2.RunStageResponse(result=_result_proto(output, elapsed))
                    return
                else:
                    raise payload
        except StageCancelled:
            await context.abort(grpc.StatusCode.CANCELLED, "stage cancelled")
        except StageInputError as exc:
            await context.abort(grpc.StatusCode.INVALID_ARGUMENT, str(exc))
        except StageDependencyError as exc:
            await context.abort(grpc.StatusCode.FAILED_PRECONDITION, str(exc))
        except asyncio.CancelledError:
            cancelled.set()
            raise
        except Exception as exc:  # noqa: BLE001 - map to INTERNAL with context
            log.exception("stage %s failed", request.stage)
            await context.abort(grpc.StatusCode.INTERNAL, f"stage {request.stage} failed: {exc}")
        finally:
            # Leaving early (client cancel, abort) signals the stage thread to stop
            # at its next cancellation check; it is not awaited.
            if not worker.done():
                cancelled.set()


async def serve(socket_path: Path, stages: Mapping[str, StageFn], models: ModelManager, device: str, stop: asyncio.Event) -> None:
    if socket_path.exists():
        socket_path.unlink()
    server = grpc.aio.server()
    worker_pb2_grpc.add_WorkerServiceServicer_to_server(WorkerServicer(stages, models, device), server)
    server.add_insecure_port(f"unix:{socket_path}")
    await server.start()
    socket_path.chmod(0o600)
    log.info("doppio-worker %s listening on %s (device=%s)", __version__, socket_path, device)
    await stop.wait()
    await server.stop(grace=2.0)
    if socket_path.exists():
        socket_path.unlink()
