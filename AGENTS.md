# AGENTS.md

Guidance for AI coding agents (Claude Code, Codex, Cursor, …) working **on**
this repository. To install doppio or use it from an agent, see
`templates/prompts/install-with-ai.md` and `templates/prompts/agent-system-prompt.md`.

## Layout

| Path | What |
|---|---|
| `cmd/doppio/` | CLI (cobra): analyze, serve, stop, doctor, setup, models |
| `internal/api/` | REST API, middleware (auth, host guard, CORS), problem+json |
| `internal/pipeline/` | Stage plan, cache-aware execution, pack assembly |
| `internal/cache/` | Content-addressed stage cache and input blobs |
| `internal/jobs/` | Async job queue (concurrency 1 = one GPU) |
| `internal/worker/` | Python worker supervisor + gRPC client |
| `internal/models/` | Embedded model registry (`registry.yaml`) and license policy |
| `internal/scp/` | Spatial Context Pack types and schema validation |
| `schema/scp/v1/` | Published JSON Schemas |
| `proto/` | Go↔Python gRPC contract; generated code in `gen/go` and `worker/src/doppio_worker/gen` |
| `worker/` | Python worker (uv project): stages, model manager, bench, synthetic scenes |
| `templates/` | Prompt and workflow templates for downstream models |

## Commands

```sh
make build        # bin/doppio
make test         # gofmt check, go vet, go test -race -cover, pytest
make test-it      # integration with the real Python worker
make proto        # after editing proto/
cd worker && uv run python -m doppio_worker.bench   # stage benchmarks
```

Go uses the local toolchain (`GOTOOLCHAIN=local`, Go 1.25). Don't bump
dependencies to versions that require a newer Go.

## Rules

- Write tests first. Go tests are table-driven and run with `-race`. Keep touched
  packages at 80 % or higher coverage.
- Don't hand-edit generated code under `gen/` or `worker/src/doppio_worker/gen/`.
- Don't add models without a verified weights license and a pinned revision in
  `internal/models/registry.yaml`.
- Large arrays never go over gRPC: stages write files into `out_dir` and return
  relative paths.
- Output conventions are a public contract (`docs/conventions.md`, `schema/`).
  Changing meaning requires a stage version bump and a changelog entry.
- Don't install the `geometry-moge` extra or download model weights without
  the user's approval. It pulls pinned GitHub dependencies and large weights.
- Commit only when asked. Use Conventional Commit messages.
