# Benchmarks

All numbers are measured with the harness in this repo. Rows without numbers
are **not measured yet**: they are listed so you can see what is missing,
never estimated.

## Reference machine

| | |
|---|---|
| Machine | Apple M4 (10 cores), 16 GB unified memory |
| OS | macOS 26.5.2 (arm64) |
| Software | Go 1.25.6, Python 3.12.14, torch 2.15.0, NumPy 2.5.4, OpenCV 5.0.0, Pillow 12.3.0 |
| Date | 2026-11-13 |

## Per-stage cost

`uv run python -m doppio_worker.bench --runs 7` (from `worker/`). Each row
runs in a fresh process: one warm-up, then 7 timed runs. The input is a
synthetic 4032×3024 (12 MP) JPEG with EXIF. Peak RSS includes the interpreter
and library baseline, shown in parentheses.

| Stage | Preset (long side) | Output | Device | Median | p90 | CPU time | Peak RSS (baseline) |
|---|---|---|---|---|---|---|---|
| ingest (decode 12 MP JPEG, orient, sRGB, resize) | fast (1024) | 1024×768 | CPU | 65 ms | 67 ms | 65 ms | 215 MB (57 MB) |
| ingest | standard (2048) | 2048×1536 | CPU | 94 ms | 98 ms | 94 ms | 236 MB (57 MB) |
| ingest | full (3072) | 3072×2304 | CPU | 128 ms | 148 ms | 128 ms | 263 MB (57 MB) |
| maps (depth 16/8-bit, normals, mask PNGs) | fast (1024) | 1024×768 | CPU | 20 ms | 20 ms | 21 ms | 108 MB (57 MB) |
| maps | standard (2048) | 2048×1536 | CPU | 76 ms | 77 ms | 78 ms | 243 MB (57 MB) |
| maps | full (3072) | 3072×2304 | CPU | 173 ms | 198 ms | 178 ms | 470 MB (57 MB) |
| geometry, MoGe-2 ViT-S | fast (1024) | | MPS | _not measured_ | | | |
| geometry, MoGe-2 ViT-B | – | | MPS | _not measured_ | | | |
| geometry, MoGe-2 ViT-L | standard / full | | MPS | _not measured_ | | | |

Geometry rows need the MoGe backend (`doppio setup`). Once it is installed,
the same command fills them in, including model load time.

## Runtime overhead

Measured with `ps -o rss,%cpu` on the reference machine, with no jobs running.

| | Value |
|---|---|
| Daemon (`doppio serve`) idle | 18 MB RSS, 0.0 % CPU |
| Daemon after starting the worker | 22 MB RSS |
| Worker (`doppio-worker`, torch imported, no model loaded) idle | 241 MB RSS, 0.0 % CPU |
| Worker cold start (spawn → healthy) | 3.1 s |
| API round trip, `GET /v1/models` via curl (includes curl start-up) | 7.3 ms |
| `bin/doppio` binary | 20 MB |
| Worker virtualenv (`worker/.venv`, without MoGe) | 0.9 GB |
| Default disk caps | 20 GB stage cache and uploads (`--cache-max-gb`), 5 GB packs (`--packs-max-gb`) |

## Model footprint

These are download sizes from the registry. Resident memory is the planning
estimate used by the worker's eviction budget (`--max-resident-gb`, default 6),
not a measurement.

| Model | Download | Resident estimate | Used by preset |
|---|---|---|---|
| moge2-vits-normal | 134 MB | 0.5 GB | fast |
| moge2-vitb-normal | 400 MB | 1.0 GB | override |
| moge2-vitl-normal | 1.2 GB | 2.0 GB | standard, full |

## Changes measured with this harness

- Maps encoders now use float32 and process one channel at a time. Peak RSS
  for the maps stage at 3072 px dropped from 3.5 GB to 470 MB, and median time
  went from 239 ms to 173 ms.

## Reproduce

```sh
cd worker
uv run python -m doppio_worker.bench --runs 7 --json ../bench.json   # all stages
uv run python -m doppio_worker.bench --image ~/Pictures/room.jpg      # your own photo
uv run python -m doppio_worker.bench --stages geometry --models moge2-vits-normal --device mps
```

Please include `bench.json` (it records host details) when you report numbers
for other hardware.
