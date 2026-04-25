.PHONY: build test test-unit test-integration test-all lint bench bench-quality bench-baseline docker docker-test clean proto-gen

# --- Build ---
build:
	go build -o bin/reverb ./cmd/reverb

# --- Production Docker image ---
docker:
	docker build -t reverb:latest .

# --- Unit tests (no external deps, runs locally) ---
test-unit:
	go test -race -count=1 -timeout 120s ./pkg/... ./internal/...

# --- Integration tests (starts reverb in Docker, runs tests against it) ---
test-integration: docker
	@echo "Starting reverb server in Docker..."
	@docker rm -f reverb-integration 2>/dev/null || true
	@docker run --rm -d --name reverb-integration -p 8082:8080 reverb:latest
	@echo "Waiting for server to be healthy..."
	@for i in 1 2 3 4 5 6 7 8 9 10; do \
		if curl -sf http://localhost:8082/healthz > /dev/null 2>&1; then break; fi; \
		sleep 1; \
	done
	REVERB_TEST_HTTP_ADDR=http://localhost:8082 \
	go test -count=1 -timeout 60s -tags integration ./test/integration/...; \
	EXIT=$$?; \
	docker stop reverb-integration; \
	exit $$EXIT

# --- All tests inside containers (zero host deps beyond Docker) ---
test-all:
	cd test && docker compose up --build --abort-on-container-exit test-runner

# --- Full containerized test (alias) ---
docker-test: test-all

# --- Convenience alias ---
test: test-unit

# --- Linting ---
lint:
	golangci-lint run ./...

# --- Benchmarks ---
bench:
	go test -bench=. -benchmem -benchtime=3s -run='^$$' ./...

# --- Quality benchmarks (correctness evals + latency) ---
bench-quality:
	go test -v -count=1 -timeout 300s -run '^TestEval_' ./benchmark/...
	go test -bench=. -benchmem -benchtime=3s -run='^$$' ./benchmark/...

# --- Published latency baselines (the exact numbers in BENCHMARKS.md) ---
# Stderr is silenced so per-Store INFO logs don't interleave with the
# benchmark output. The eval suite (bench-quality) covers Store + logs.
bench-baseline:
	@go test -bench='BenchmarkLookup_(ExactHit|SemanticHit|Miss)(_ScaledIndex)?$$' \
		-benchmem -benchtime=2s -run='^$$' ./benchmark/... 2>/dev/null \
		| grep -E '^(Benchmark|goos|goarch|pkg|cpu|PASS|ok|FAIL)'

# --- Coverage ---
coverage:
	go test -coverprofile=coverage.out -covermode=atomic ./pkg/... ./internal/...
	go tool cover -func=coverage.out
	go tool cover -html=coverage.out -o coverage.html

# --- Proto generation ---
proto-gen:
	protoc \
		--go_out=. --go_opt=paths=source_relative \
		--go-grpc_out=. --go-grpc_opt=paths=source_relative \
		pkg/server/proto/reverb.proto

# --- Cleanup ---
clean:
	cd test && docker compose down -v 2>/dev/null || true
	docker rm -f reverb-integration 2>/dev/null || true
	rm -rf bin/ coverage.out coverage.html data/
