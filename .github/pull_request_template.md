## Summary

<!-- What does this change and why? Link the issue: Fixes #123 -->

## Changes

-

## Test plan

- [ ] `make test` passes (gofmt, vet, `go test -race -cover`, pytest)
- [ ] `make test-it` passes (if worker or proto changed)
- [ ] New or changed behaviour has tests; touched packages stay at 80 % or higher coverage
- [ ] Benchmarks attached (if performance-related): before/after from `python -m doppio_worker.bench`

## Checklist

- [ ] Output format changes update `schema/`, `docs/conventions.md` and `examples/`, and bump the stage version
- [ ] New models have a verified weights license and a pinned revision in `internal/models/registry.yaml`
- [ ] `CHANGELOG.md` updated under "Unreleased"
- [ ] Docs and README updated if user-facing
