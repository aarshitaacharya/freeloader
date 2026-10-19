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
