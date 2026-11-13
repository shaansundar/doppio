# 0001 — Phase 0 findings: Mocha Pro, model licenses, MoGe pin

Date: 2026-11-13. Status: accepted.

## Mocha Pro is a reference, not a dependency

We inspected Mocha Pro 2026.5.1 (`/Applications/BorisFX/Mocha Pro 2026.5.app`)
using symbol tables, strings and its shipped Python package:

- The 3D camera solve is a bundled SynthEyes engine (`libcam3d` → `libsyntheyes`;
  `Cam3D::SyntheyesSolver::solveAllFrames`). It needs a multi-frame clip with
  parallax.
- The bundled ML models (`Resources/ML/models`) cover point tracking
  (CoTracker3), optical flow (RAFT), retiming (RIFE), segmentation, matting and
  face/plate detection. There is **no monocular depth or normal model**.
- The Python API (`mocha.project`, `mocha.exporters`) exports existing solves
  (USD/FBX/Alembic/.sni); no call that runs a solve was found.

**Decision:** doppio is standalone. It uses no Mocha code, binaries or bundled
models, and does not extract them. We re-implement the useful ideas (planar
surfaces with homographies, camera export, layered depth) with open models.

## Model licenses (checked against HF `cardData.license` and GitHub LICENSE)

| Model | License | Commercial |
|---|---|---|
| MoGe-2 ViT-S/B/L normal (weights) | MIT | yes |
| MoGe code | MIT (+ Apache-2.0 DINOv2 parts) | yes |
| Florence-2 base/large | MIT | yes |
| SAM 2.1 hiera small/base+/large | Apache-2.0 | yes |
| GeoCalib (repo + v1.0 release weights) | Apache-2.0 | yes |
| DA3 SMALL/BASE/METRIC-LARGE/MONO-LARGE | Apache-2.0 | yes |
| MapAnything (`facebook/map-anything-apache`) | Apache-2.0 | yes |
| VGGT-1B-Commercial | custom AUP (no military use), gated | yes, with terms |
| MapAnything default (`facebook/map-anything`) | CC-BY-NC-4.0 | **no** |
| DA3-LARGE / GIANT (any-view) | CC-BY-NC-4.0 | **no** |
| VGGT-1B (original) | CC-BY-NC-4.0 | **no** |
| Apple Depth Pro | apple-amlr (research) | **no** |
| SAM 3 | custom SAM License, gated | treated as **no** until reviewed |

Revisions are pinned in `internal/models/registry.yaml`. Re-verify licenses
when bumping a revision.

## MoGe pin

`microsoft/MoGe` main is now MoGe-3 (v3.0.0, Aug 2026). It depends on the
CUDA/Triton `flex-gemm`, and its uv config targets only Linux and Windows, so
it is not viable on Apple Silicon. We pin MoGe **2.0.0** at commit
`07444410f1e33f402353b99d6ccd26bd31e469e8` (2025-11-02). MoGe-3 weights are
MIT too; revisit them for the CUDA path later.

MoGe is an optional worker extra (`geometry-moge`) because it pulls two
GitHub-only dependencies (`EasternJournalist/utils3d`, `EasternJournalist/pipeline`)
at pinned commits. Installing it needs explicit approval: `doppio setup` or
`make setup-moge`.

## Still open

- MPS latency and memory benchmarks for MoGe-2 S/B/L on the M4 with 16 GB.
  These need MoGe installed. Preset latency targets stay unverified until then.
- The label→ADE20K mapping for segmentation maps (Phase 3).
