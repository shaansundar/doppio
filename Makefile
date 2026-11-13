GO       ?= go
export GOTOOLCHAIN := local
VERSION  ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo 0.1.0-dev)
LDFLAGS  := -X main.version=$(VERSION)
WORKER   := worker

.PHONY: all build proto test test-go test-py test-it lint bench figures worker-setup setup-moge clean

all: build

build:
	$(GO) build -ldflags "$(LDFLAGS)" -o bin/doppio ./cmd/doppio

## proto: regenerate Go and Python gRPC code from proto/
proto:
	PATH="$$HOME/go/bin:$$PATH" buf lint
	PATH="$$HOME/go/bin:$$PATH" buf generate
	cd $(WORKER) && uv run python -m grpc_tools.protoc -I ../proto \
		--python_out=src/doppio_worker/gen --grpc_python_out=src/doppio_worker/gen \
		--pyi_out=src/doppio_worker/gen ../proto/doppio/worker/v1/worker.proto

test: test-go test-py

test-go:
	$(GO) vet ./...
	$(GO) test -race -cover ./...

test-py:
	cd $(WORKER) && uv run pytest -q -m "not model"

## test-it: integration tests against the real Python worker
test-it:
	DOPPIO_WORKER_IT=1 $(GO) test -race -run TestRealWorker -v ./internal/worker/

## bench: per-stage time / CPU / peak RSS (see docs/benchmarks.md)
bench:
	cd $(WORKER) && uv run python -m doppio_worker.bench --runs 7 --json ../bench.json

## figures: regenerate docs/assets from the synthetic scene
figures:
	cd $(WORKER) && uv run python ../scripts/make_figures.py

lint:
	@test -z "$$(gofmt -l .)" || (gofmt -l . && exit 1)
	$(GO) vet ./...

## worker-setup: core worker env (PyPI packages only)
worker-setup:
	cd $(WORKER) && uv sync

## setup-moge: add the MoGe geometry backend (GitHub-pinned deps)
setup-moge:
	cd $(WORKER) && uv sync --extra geometry-moge

clean:
	rm -rf bin
