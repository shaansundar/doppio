"""End-to-end gRPC tests over a real Unix socket with fake stages."""

import asyncio
import json
import os
import tempfile
from pathlib import Path

import grpc
import pytest

from doppio_worker.gen import worker_pb2, worker_pb2_grpc
from doppio_worker.model_manager import ModelManager
from doppio_worker.server import serve
from doppio_worker.stages.base import ArtifactOut, StageDependencyError, StageInputError, StageOutput


def fake_ok(ctx):
    ctx.report(0.5, "halfway")
    (ctx.out_dir / "x.txt").write_text(json.dumps(dict(ctx.params)))
    return StageOutput([ArtifactOut("x", "x.txt", "text/plain")], {"echo": ctx.params.get("value")})


def fake_bad_input(ctx):
    raise StageInputError("no good")


def fake_crash(ctx):
    raise RuntimeError("boom")


def fake_missing_dep(ctx):
    raise StageDependencyError("backend not installed")


STAGES = {"ok": fake_ok, "bad": fake_bad_input, "crash": fake_crash, "dep": fake_missing_dep}


async def _with_server(fn):
    # Short socket path: macOS limits AF_UNIX paths to ~104 bytes.
    sock_dir = tempfile.mkdtemp(prefix="dw", dir="/tmp")
    sock = Path(sock_dir) / "w.sock"
    stop = asyncio.Event()
    task = asyncio.create_task(serve(sock, STAGES, ModelManager("cpu"), "cpu", stop))
    for _ in range(100):
        if sock.exists():
            break
        await asyncio.sleep(0.02)
    try:
        async with grpc.aio.insecure_channel(f"unix:{sock}") as channel:
            await fn(worker_pb2_grpc.WorkerServiceStub(channel))
    finally:
        stop.set()
        await task
        os.rmdir(sock_dir)


def test_health_lists_stages():
    async def check(stub):
        resp = await stub.Health(worker_pb2.HealthRequest())
        assert resp.device == "cpu"
        assert list(resp.stages) == ["bad", "crash", "dep", "ok"]

    asyncio.run(_with_server(check))


def test_run_stage_streams_progress_then_result(tmp_path):
    async def check(stub):
        req = worker_pb2.RunStageRequest(stage="ok", params_json='{"value": 7}', out_dir=str(tmp_path))
        events = [e async for e in stub.RunStage(req)]
        assert events[0].WhichOneof("event") == "progress"
        assert events[0].progress.message == "halfway"
        result = events[-1].result
        assert json.loads(result.metadata_json) == {"echo": 7}
        assert result.artifacts[0].rel_path == "x.txt"
        assert (tmp_path / "x.txt").exists()

    asyncio.run(_with_server(check))


@pytest.mark.parametrize(
    ("stage", "params", "out_dir", "code"),
    [
        ("missing", "{}", None, grpc.StatusCode.INVALID_ARGUMENT),
        ("ok", "[1]", None, grpc.StatusCode.INVALID_ARGUMENT),
        ("ok", "{", None, grpc.StatusCode.INVALID_ARGUMENT),
        ("ok", "{}", "relative/dir", grpc.StatusCode.INVALID_ARGUMENT),
        ("bad", "{}", None, grpc.StatusCode.INVALID_ARGUMENT),
        ("crash", "{}", None, grpc.StatusCode.INTERNAL),
        ("dep", "{}", None, grpc.StatusCode.FAILED_PRECONDITION),
    ],
)
def test_run_stage_error_mapping(tmp_path, stage, params, out_dir, code):
    async def check(stub):
        req = worker_pb2.RunStageRequest(stage=stage, params_json=params, out_dir=out_dir or str(tmp_path))
        with pytest.raises(grpc.aio.AioRpcError) as err:
            _ = [e async for e in stub.RunStage(req)]
        assert err.value.code() == code

    asyncio.run(_with_server(check))
