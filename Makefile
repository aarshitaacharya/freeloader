# fl-cache builds on Linux (epoll). These targets run inside a Docker container
# so they work from macOS too. The build tree lives in a named volume.
IMAGE := fl-cache-dev
VOLUME := fl-cache-build
DOCKER_RUN := docker run --rm -v $(CURDIR):/src -v $(VOLUME):/src/cache/build $(IMAGE)

.PHONY: bench bench-store bench-report cache-image cache-test cache-asan cache-tsan cache-release cache-run cache-shell

cache-image:
	docker build -q -t $(IMAGE) -f cache/Dockerfile.dev cache

cache-test: cache-image
	$(DOCKER_RUN) sh -c 'cmake --preset debug >/dev/null && cmake --build --preset debug -j"$$(nproc)" && ctest --preset debug'

cache-asan: cache-image
	$(DOCKER_RUN) sh -c 'cmake --preset asan >/dev/null && cmake --build --preset asan -j"$$(nproc)" && ctest --preset asan'

cache-tsan: cache-image
	$(DOCKER_RUN) sh -c 'cmake --preset tsan >/dev/null && cmake --build --preset tsan -j"$$(nproc)" && ctest --preset tsan'

cache-release: cache-image
	$(DOCKER_RUN) sh -c 'cmake --preset release >/dev/null && cmake --build --preset release -j"$$(nproc)"'

# Server on localhost:6400 so host redis-cli works. Ctrl-C sends SIGINT -> clean shutdown.
cache-run: cache-release
	docker run --rm -it -p 6400:6400 -v $(VOLUME):/src/cache/build $(IMAGE) \
	  /src/cache/build/release/fl-cache --port 6400

cache-shell: cache-image
	docker run --rm -it -v $(CURDIR):/src -v $(VOLUME):/src/cache/build $(IMAGE) bash

# Benchmarks (take ~15-25 minutes; run on an otherwise idle machine).
BENCH_RUN := docker run --rm -e REPS -e CLIENTS -e CONFIGS -e SECONDS_PER_RUN -v $(CURDIR):/src -v $(VOLUME):/src/cache/build -w /src/bench $(IMAGE)

bench: cache-release
	$(BENCH_RUN) ./run.sh

bench-store: cache-release
	$(BENCH_RUN) ./run_store.sh

bench-report: cache-image
	$(BENCH_RUN) python3 report.py

# ---- Gateway & stack --------------------------------------------------------
COMPOSE := docker compose $(if $(wildcard .env),--env-file .env) -f deploy/compose/docker-compose.yml
COMPOSE_MOCK := $(COMPOSE) -f deploy/compose/docker-compose.mock.yml

.PHONY: init up up-mock down logs app gateway-test gateway-integration check-hit-latency check-openai-client

init:  ## create local config and .env from the examples (never overwrites)
	@[ -f config/providers.yaml ] || cp config/providers.example.yaml config/providers.yaml
	@[ -f .env ] || cp .env.example .env
	@echo "edit .env to add provider API keys (it is gitignored)"

up: init
	$(COMPOSE) up -d --build

# Same stack, but with a local fake provider instead of Groq/Gemini/OpenRouter (no API keys needed).
up-mock:
	@[ -f .env ] || cp .env.example .env
	$(COMPOSE_MOCK) up -d --build

down:
	$(COMPOSE_MOCK) down

logs:
	$(COMPOSE) logs -f gateway

# Create an app and print its key once:  make app NAME=demo
app:
	@[ -n "$(NAME)" ] || { echo "usage: make app NAME=demo"; exit 2; }
	$(COMPOSE) run --rm --no-deps --entrypoint /usr/local/bin/mkapp \
	  -e DATABASE_URL=postgres://freeloader:$${POSTGRES_PASSWORD:-freeloader}@postgres:5432/freeloader?sslmode=disable \
	  gateway --name "$(NAME)" $(ARGS)

gateway-test:
	cd gateway && go vet ./... && go test -race -count=1 ./...

gateway-integration:
	docker build -q -t fl-cache:dev -f cache/Dockerfile .
	cd gateway && go test -tags integration -race -count=1 -timeout 300s ./internal/integration/...

# Level 4 check: cache HITs answer in < 10 ms (run `make up-mock app NAME=demo` first, pass the key).
check-hit-latency:
	@[ -n "$(KEY)" ] || { echo "usage: make check-hit-latency KEY=fl_..."; exit 2; }
	docker run --rm --network freeloader_default -e KEY=$(KEY) -v $(CURDIR)/bench:/bench:ro \
	  python:3.13-alpine python -I /bench/hit_latency.py

# Level 5 check: a streamed chat through the gateway using the official OpenAI Python client.
#   make check-openai-client KEY=fl_... [MODEL=fast]
check-openai-client:
	@[ -n "$(KEY)" ] || { echo "usage: make check-openai-client KEY=fl_... [MODEL=fast]"; exit 2; }
	docker run --rm --network freeloader_default -e KEY=$(KEY) -e MODEL=$(or $(MODEL),fast) \
	  -v $(CURDIR)/tools/checks:/checks:ro python:3.13-slim \
	  sh -c "pip install -q --disable-pip-version-check openai >/dev/null 2>&1 && python -I /checks/openai_client_check.py"
