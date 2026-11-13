# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the project uses
[Semantic Versioning](https://semver.org/).

## [Unreleased]

### Added
- `doppio` CLI: `analyze`, `serve`, `stop`, `doctor`, `setup`, `models list`, `version`.
- Local REST API (`/v1`) with bearer-token auth, loopback-only binding, Host-header
  checks, RFC 9457 errors, async jobs with `?wait=`, and pack/artifact download.
- Python inference worker over gRPC on a Unix socket: `ingest`, `geometry`
  (MoGe-2 ViT-S/B/L, metric depth and normals) and `maps` stages.
- Spatial Context Pack v1 (`scene.json`, `manifest.json`, maps, arrays) with
  JSON Schemas, plus a synthetic example.
- Content-addressed stage cache with Merkle keys, in-flight deduplication and
  LRU pruning.
- Model registry with pinned revisions and a license policy (commercial-only
  by default; `--allow-noncommercial` to opt in).
- Benchmark harness (`python -m doppio_worker.bench`) and synthetic test scenes.
- Prompt and workflow templates, including an install-with-AI prompt.
- LRU pruning of assembled packs (`--packs-max-gb`, default 5) and of uploaded inputs.
- `doppio analyze` exits with code 2 for input errors (unreadable image, bad option, license refusal).

### Performance
- Maps encoders use float32, one channel at a time. Peak RSS at 3072 px dropped
  from 3.5 GB to 470 MB.
