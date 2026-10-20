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

.PHONY: init up up-mock down logs app gateway-test gateway-integration check-hit-latency check-openai-client ledger-replay

# Generates ADMIN_TOKEN in .env when it is missing or empty (the admin API stays off without it).
define ensure_admin_token
	@grep -q '^ADMIN_TOKEN=.' .env || { grep -v '^ADMIN_TOKEN=' .env > .env.tmp; echo "ADMIN_TOKEN=$$(openssl rand -hex 24)" >> .env.tmp; mv .env.tmp .env; echo "generated ADMIN_TOKEN in .env"; }
endef

init:  ## create local config and .env from the examples (never overwrites)
	@[ -f config/providers.yaml ] || cp config/providers.example.yaml config/providers.yaml
	@[ -f .env ] || cp .env.example .env
	$(ensure_admin_token)
	@echo "edit .env to add provider API keys (it is gitignored)"

up: init
	$(COMPOSE) up -d --build
	@echo "panel: http://127.0.0.1:$$(grep '^PANEL_PORT=' .env | cut -d= -f2 | grep . || echo 3001)  (admin token is ADMIN_TOKEN in .env)"

# Same stack, but with a local fake provider instead of Groq/Gemini/OpenRouter (no API keys needed).
up-mock:
	@[ -f .env ] || cp .env.example .env
	$(ensure_admin_token)
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
	cd ledger && go vet ./... && go test -race -count=1 ./...
	cd guard && go vet ./... && go test -race -count=1 ./...

gateway-integration:
	docker build -q -t fl-cache:dev -f cache/Dockerfile .
	cd gateway && go test -tags integration -race -count=1 -timeout 300s ./internal/integration/...
	cd ledger && go test -tags integration -race -count=1 -timeout 300s ./...
	cd guard && go test -tags integration -race -count=1 -timeout 300s ./...

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

# Rebuild usage_events and usage_daily from the llm.usage topic (TRUNCATES them first).
# Refuses to run while any ledger replica is up:  docker compose stop ledger
ledger-replay:
	$(COMPOSE) run --rm --no-deps ledger replay --confirm

# ---- Level 7: Kubernetes on kind. kind and helm are fetched into ./.bin (gitignored). ----
KIND_VERSION := v0.29.0
HELM_VERSION := v3.18.4
KUBECTL := kubectl --context kind-freeloader -n freeloader

.PHONY: kind-tools kind-up kind-up-mock kind-down kind-app kind-drill

kind-tools:
	@mkdir -p .bin
	@[ -x .bin/kind ] || { curl -fsSL -o .bin/kind https://kind.sigs.k8s.io/dl/$(KIND_VERSION)/kind-$$(uname -s | tr A-Z a-z)-$$(uname -m | sed s/x86_64/amd64/\;s/arm64/arm64/) && chmod +x .bin/kind; }
	@[ -x .bin/helm ] || { os=$$(uname -s | tr A-Z a-z); arch=$$(uname -m | sed s/x86_64/amd64/); \
	  curl -fsSL https://get.helm.sh/helm-$(HELM_VERSION)-$$os-$$arch.tar.gz | tar xz -C .bin --strip-components=1 $$os-$$arch/helm; }

kind-up: kind-tools
	deploy/kind/up.sh

kind-up-mock: kind-tools
	deploy/kind/up.sh --mock

kind-down:
	deploy/kind/down.sh

# Create an app inside the cluster and print its key once:  make kind-app NAME=demo ARGS="--rpm 0"
kind-app:
	@[ -n "$(NAME)" ] || { echo "usage: make kind-app NAME=demo"; exit 2; }
	$(KUBECTL) exec deploy/gateway -- /usr/local/bin/mkapp --name "$(NAME)" $(ARGS)

# Level 7 check: delete a gateway pod in the middle of a 60 s load test (needs `make kind-up-mock`).
kind-drill:
	deploy/kind/drill.sh

# ---- Level 8: observability (after kind-up) ----
.PHONY: kind-monitoring kind-grafana dashboard
kind-monitoring: kind-tools
	OVERLAY=$${OVERLAY:-mock} deploy/kind/monitoring.sh

# Port-forwards Grafana (3000) and Prometheus (9090) and prints the admin password.
kind-grafana:
	@echo "Grafana http://127.0.0.1:3000  user admin  password $$(kubectl --context kind-freeloader -n monitoring get secret grafana-admin -o jsonpath='{.data.password}' | base64 -d)"
	@kubectl --context kind-freeloader -n monitoring port-forward svc/kps-prometheus 9090:9090 >/dev/null & \
	  kubectl --context kind-freeloader -n monitoring port-forward svc/kps-grafana 3000:80

dashboard:
	python3 tools/dashboard/gen.py

# ---- Level 9: control panel (needs Node 22+) ----
.PHONY: panel-install panel-dev panel-check panel-api
panel-install:
	cd panel && npm ci

# Dev server on http://localhost:5173, proxying /admin to the Compose gateway (GATEWAY_URL overrides).
panel-dev: panel-install
	cd panel && npm run dev

# What CI runs: types regenerated from api/openapi.yaml, strict tsc, lint (no any), tests, production build.
panel-check: panel-install
	cd panel && npm run check:api && npm run lint && npm test && npm run build

panel-api:  ## regenerate panel/src/api/schema.d.ts after editing api/openapi.yaml
	cd panel && npm run gen:api
