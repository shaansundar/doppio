"""Stage benchmarks: wall time, CPU time and peak RSS per stage and size.

    uv run python -m doppio_worker.bench                 # ingest + maps (+ geometry if MoGe is installed)
    uv run python -m doppio_worker.bench --image my.jpg --runs 10 --json bench.json

Each measurement runs in a fresh subprocess so peak RSS reflects that stage
alone (plus interpreter and library baseline, reported separately).
"""

from __future__ import annotations

import argparse
import json
import os
import platform
import resource
import statistics
import subprocess
import sys
import tempfile
import time
from pathlib import Path
from typing import Any

DEFAULT_SIZES = (1024, 2048, 3072)
MOGE_MODELS = ("moge2-vits-normal", "moge2-vitb-normal", "moge2-vitl-normal")
PRESET_FOR_SIZE = {1024: "fast", 2048: "standard", 3072: "full"}


def _peak_rss_mb() -> float:
    peak = resource.getrusage(resource.RUSAGE_SELF).ru_maxrss
    # macOS reports bytes, Linux kilobytes.
    return peak / (1 << 20) if sys.platform == "darwin" else peak / 1024


def _cpu_seconds() -> float:
    r = resource.getrusage(resource.RUSAGE_SELF)
    return r.ru_utime + r.ru_stime


def _registry_model(model_id: str) -> dict[str, Any]:
    import yaml  # dev dependency

    path = Path(__file__).resolve().parents[3] / "internal" / "models" / "registry.yaml"
    for entry in yaml.safe_load(path.read_text())["models"]:
        if entry["id"] == model_id:
            return entry
    raise SystemExit(f"model {model_id} not in {path}")


def _synthetic_photo(path: Path, width: int = 4032, height: int = 3024) -> None:
    from PIL import Image

    from doppio_worker import synthetic

    scene = synthetic.room(width, height)
    exif = Image.Exif()
    exif.get_ifd(0x8769)[41989] = 26  # typical phone main camera, 26 mm equiv.
    Image.fromarray(scene.image).save(path, format="JPEG", quality=92, exif=exif.tobytes())


def _synthetic_arrays(directory: Path, size: int) -> dict[str, str]:
    import numpy as np

    from doppio_worker import synthetic

    scene = synthetic.room(size, size * 3 // 4)
    paths = {}
    for name, arr in (("depth", scene.depth), ("normal", scene.normal), ("mask", scene.mask)):
        paths[name] = str(directory / f"{name}.npy")
        np.save(paths[name], arr)
    return paths


# --------------------------------------------------------------------------- child


def _child(spec: dict[str, Any]) -> dict[str, Any]:
    from doppio_worker.device import select_device
    from doppio_worker.stages import geometry, ingest, maps
    from doppio_worker.stages.base import ModelRef, StageContext

    stage, size, runs = spec["stage"], int(spec["size"]), int(spec["runs"])
    device = select_device(spec.get("device") or None) if stage == "geometry" else "cpu"
    work = Path(tempfile.mkdtemp(prefix="doppio-bench-"))
    baseline = _peak_rss_mb()
    result: dict[str, Any] = {"stage": stage, "size": size, "runs": runs, "device": device}

    inputs: dict[str, Path] = {}
    params: dict[str, Any] = {}
    model = None
    models = None
    if stage == "ingest":
        fn = ingest.run
        inputs["source"] = Path(spec["image"])
        params["max_side"] = size
    elif stage == "maps":
        fn = maps.run
        inputs.update({name: Path(path) for name, path in spec["inputs"].items()})
    elif stage == "geometry":
        try:
            import moge  # noqa: F401
        except ImportError:
            return {**result, "skipped": "MoGe not installed (run `doppio setup`)"}
        from PIL import Image

        from doppio_worker.model_manager import ModelManager

        fn = geometry.run
        entry = _registry_model(spec["model"])
        model = ModelRef(id=entry["id"], family=entry["family"], hf_repo=entry["hf_repo"],
                         revision=entry["revision"], memory_gb=entry["memory_gb"])
        models = ModelManager(device)
        with Image.open(spec["image"]) as img:
            img = img.convert("RGB")
            img.thumbnail((size, size))
            inputs["image"] = work / "image.png"
            img.save(inputs["image"])
        params["resolution_level"] = 5 if size <= 1024 else 9
        result["model"] = spec["model"]
        started = time.perf_counter()
        models.get(model, geometry._load_moge2)
        result["load_s"] = round(time.perf_counter() - started, 3)
    else:
        raise SystemExit(f"unknown stage {stage}")

    def once(i: int) -> tuple[float, float, dict[str, Any]]:
        out = work / f"out-{i}"
        out.mkdir()
        ctx = StageContext(inputs=inputs, params=params, out_dir=out, device=device, models=models, model=model)
        c0, t0 = _cpu_seconds(), time.perf_counter()
        output = fn(ctx)
        return time.perf_counter() - t0, _cpu_seconds() - c0, output.metadata

    once(-1)  # warm-up (caches, MPS kernel compilation)
    walls, cpus = [], []
    meta: dict[str, Any] = {}
    for i in range(runs):
        wall, cpu, meta = once(i)
        walls.append(wall)
        cpus.append(cpu)
    walls.sort()
    result.update(
        width=meta.get("width"), height=meta.get("height"),
        wall_median_s=round(statistics.median(walls), 4),
        wall_p90_s=round(walls[min(len(walls) - 1, int(0.9 * len(walls)))], 4),
        cpu_median_s=round(statistics.median(cpus), 4),
        baseline_rss_mb=round(baseline, 1),
        peak_rss_mb=round(_peak_rss_mb(), 1),
    )
    if stage == "maps":
        result["width"], result["height"] = size, size * 3 // 4
    return result


# --------------------------------------------------------------------------- parent


def _run_child(spec: dict[str, Any]) -> dict[str, Any]:
    proc = subprocess.run(
        [sys.executable, "-m", "doppio_worker.bench", "--child", json.dumps(spec)],
        capture_output=True, text=True, env={**os.environ, "PYTORCH_ENABLE_MPS_FALLBACK": "1"},
    )
    if proc.returncode != 0:
        return {**spec, "error": proc.stderr.strip().splitlines()[-1] if proc.stderr else "failed"}
    return json.loads(proc.stdout.strip().splitlines()[-1])


def _host() -> dict[str, Any]:
    info: dict[str, Any] = {"platform": platform.platform(), "python": platform.python_version(),
                            "cpu_count": os.cpu_count()}
    if sys.platform == "darwin":
        for key, name in (("machdep.cpu.brand_string", "cpu"), ("hw.memsize", "memory_bytes")):
            out = subprocess.run(["sysctl", "-n", key], capture_output=True, text=True).stdout.strip()
            info[name] = int(out) if name == "memory_bytes" and out.isdigit() else out
    try:
        import torch

        info["torch"] = torch.__version__
    except ImportError:
        pass
    return info


def _markdown(rows: list[dict[str, Any]]) -> str:
    lines = [
        "| Stage | Preset size | Output | Model | Device | Median | p90 | CPU time | Peak RSS (baseline) |",
        "|---|---|---|---|---|---|---|---|---|",
    ]
    for r in rows:
        if "skipped" in r or "error" in r:
            lines.append(f"| {r['stage']} | {r['size']} | | {r.get('model', '')} | | "
                         f"_{r.get('skipped') or r.get('error')}_ | | | |")
            continue
        out = f"{r['width']}x{r['height']}" if r.get("width") else ""
        load = f" (+{r['load_s']} s load)" if "load_s" in r else ""
        lines.append(
            f"| {r['stage']} | {r['size']} ({PRESET_FOR_SIZE.get(r['size'], '-')}) | {out} | {r.get('model', '-')} | "
            f"{r['device']} | {r['wall_median_s'] * 1000:.0f} ms{load} | {r['wall_p90_s'] * 1000:.0f} ms | "
            f"{r['cpu_median_s'] * 1000:.0f} ms | {r['peak_rss_mb']:.0f} MB ({r['baseline_rss_mb']:.0f} MB) |"
        )
    return "\n".join(lines)


def main(argv: list[str] | None = None) -> None:
    parser = argparse.ArgumentParser(prog="doppio-bench", description=__doc__,
                                     formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--child", help=argparse.SUPPRESS)
    parser.add_argument("--image", type=Path, help="input photo (default: synthetic 12 MP JPEG)")
    parser.add_argument("--runs", type=int, default=5)
    parser.add_argument("--sizes", default=",".join(map(str, DEFAULT_SIZES)))
    parser.add_argument("--stages", default="ingest,maps,geometry")
    parser.add_argument("--models", default=",".join(MOGE_MODELS))
    parser.add_argument("--device", default="", help="geometry device: cuda, mps or cpu (default auto)")
    parser.add_argument("--json", type=Path, help="write raw results here")
    args = parser.parse_args(argv)

    if args.child:
        print(json.dumps(_child(json.loads(args.child))))
        return

    image = args.image
    tmp = None
    if image is None:
        tmp = tempfile.mkdtemp(prefix="doppio-bench-img-")
        image = Path(tmp) / "synthetic-12mp.jpg"
        _synthetic_photo(image)
    sizes = [int(s) for s in args.sizes.split(",") if s]
    stages = [s for s in args.stages.split(",") if s]
    rows: list[dict[str, Any]] = []
    for stage in stages:
        for size in sizes:
            specs = [{"stage": stage, "size": size, "runs": args.runs, "image": str(image), "device": args.device}]
            if stage == "maps":
                # Build inputs here so the child's peak RSS measures the stage only.
                specs[0]["inputs"] = _synthetic_arrays(Path(tempfile.mkdtemp(prefix="doppio-bench-maps-")), size)
            if stage == "geometry":
                specs = [{**specs[0], "model": m} for m in args.models.split(",") if m]
            for spec in specs:
                print(f"bench {stage} {size} {spec.get('model', '')}".rstrip(), file=sys.stderr)
                rows.append(_run_child(spec))
                if rows[-1].get("skipped"):
                    break
            if rows and rows[-1].get("skipped"):
                break

    report = {"host": _host(), "image": str(image) if args.image else "synthetic 4032x3024 JPEG", "results": rows}
    if args.json:
        args.json.write_text(json.dumps(report, indent=2) + "\n")
    print(_markdown(rows))


if __name__ == "__main__":
    main()
