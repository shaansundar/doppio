"""doppio-worker entrypoint: ``doppio-worker --socket /path/worker.sock``."""

from __future__ import annotations

import argparse
import asyncio
import logging
import os
import signal
import sys
import threading
from pathlib import Path


def _parse_args(argv: list[str] | None) -> argparse.Namespace:
    parser = argparse.ArgumentParser(prog="doppio-worker", description=__doc__)
    parser.add_argument("--socket", required=True, type=Path, help="Unix socket path to listen on")
    parser.add_argument("--max-resident-gb", type=float, default=6.0, help="resident model memory budget")
    parser.add_argument("--device", default=None, help="force cuda, mps or cpu")
    parser.add_argument(
        "--exit-on-stdin-eof",
        action="store_true",
        help="exit when stdin closes (the parent orchestrator went away)",
    )
    parser.add_argument("--log-level", default="INFO")
    return parser.parse_args(argv)


def _watch_stdin(loop: asyncio.AbstractEventLoop, stop: asyncio.Event) -> None:
    def read_until_eof() -> None:
        try:
            while sys.stdin.buffer.read(4096):
                pass
        finally:
            loop.call_soon_threadsafe(stop.set)

    threading.Thread(target=read_until_eof, name="stdin-watch", daemon=True).start()


async def _main(args: argparse.Namespace) -> None:
    from doppio_worker.device import select_device
    from doppio_worker.model_manager import ModelManager
    from doppio_worker.server import serve
    from doppio_worker.stages import STAGES

    device = select_device(args.device)
    models = ModelManager(device=device, max_resident_gb=args.max_resident_gb)
    loop = asyncio.get_running_loop()
    stop = asyncio.Event()
    for sig in (signal.SIGINT, signal.SIGTERM):
        loop.add_signal_handler(sig, stop.set)
    if args.exit_on_stdin_eof:
        _watch_stdin(loop, stop)
    await serve(args.socket.expanduser().resolve(), STAGES, models, device, stop)


def main(argv: list[str] | None = None) -> None:
    args = _parse_args(argv)
    # Let unsupported MPS ops fall back to CPU instead of failing the stage.
    os.environ.setdefault("PYTORCH_ENABLE_MPS_FALLBACK", "1")
    logging.basicConfig(
        stream=sys.stderr,
        level=args.log_level.upper(),
        format="%(asctime)s %(levelname)s %(name)s: %(message)s",
    )
    asyncio.run(_main(args))


if __name__ == "__main__":
    main()
