# Contributing to doppio

Thanks for helping! Bug reports, benchmarks on new hardware, prompt templates
and code are all welcome.

## Ground rules

- Be kind; see the [Code of Conduct](CODE_OF_CONDUCT.md).
- Report security issues privately; see [SECURITY.md](SECURITY.md).
- For anything larger than a bug fix, open an issue first so we can agree on the approach.
- Models: only add models whose weights license is verified from the upstream
  model card or LICENSE file. Record it in `internal/models/registry.yaml`
  with a pinned revision. Non-commercial models must set `commercial: false`.

## Development setup

```sh
git clone https://github.com/shaansundar/doppio.git && cd doppio
make build          # bin/doppio
make worker-setup   # Python worker env (uv, PyPI only)
make setup-moge     # optional: MoGe-2 geometry backend (pinned GitHub deps)
make test           # gofmt/vet + go test -race -cover + pytest
make test-it        # Go <-> real Python worker integration
```

Requirements: Go 1.25+, [uv](https://docs.astral.sh/uv/), and `buf` + `protoc`
only if you change `proto/`.

## Workflow

1. Write a failing test first (table-driven in Go, pytest in Python), then make it pass.
2. Keep Go formatted (`gofmt`), wrap errors with context (`fmt.Errorf("...: %w", err)`),
   accept interfaces and return structs, and keep interfaces small.
3. Keep coverage at 80 % or higher for the packages you touch (`go test -cover ./...`).
4. If you change `proto/`, run `make proto` and commit the generated Go and Python code.
5. If you change output formats, update `schema/scp/v1/`, `docs/conventions.md`
   and `examples/`. `go test ./internal/scp` validates the examples.
6. For performance work, include before and after numbers from
   `python -m doppio_worker.bench` (see [docs/benchmarks.md](docs/benchmarks.md)).
7. Add a line under "Unreleased" in [CHANGELOG.md](CHANGELOG.md).

## Commit messages

Use [Conventional Commits](https://www.conventionalcommits.org/):
`feat:`, `fix:`, `refactor:`, `docs:`, `test:`, `chore:`, `perf:`, `ci:`.

```
feat: add GeoCalib calibration stage

Estimates roll/pitch and focal length with uncertainty and fuses them with
the EXIF prior.
```

## Adding a pipeline stage

1. Python: add `worker/src/doppio_worker/stages/<stage>.py` with
   `run(ctx) -> StageOutput`, register it in `stages/__init__.py`, and add tests.
   Use `StageInputError` for bad input and `StageDependencyError` for missing
   optional backends.
2. Go: add the stage to the plan in `internal/pipeline/plan.go` (bindings,
   params, version), map its artifacts in `internal/pipeline/pack.go`, and
   extend the fake runner in `pipeline_test.go`.
3. Bump the stage `version` whenever its outputs change meaning; that
   invalidates its cache entries and everything downstream.

## Maintainers: repository topics

```sh
gh repo edit shaansundar/doppio --add-topic 3d,depth-estimation,monocular-depth,surface-normals,camera-calibration,controlnet,image-editing,generative-ai,stable-diffusion,flux,comfyui,computer-vision,apple-silicon,local-first,golang,pytorch
```

Upload `docs/assets/cover.png` as the social preview (Settings → General).
