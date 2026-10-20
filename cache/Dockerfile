# Build context is the repository root:  docker build -f cache/Dockerfile .
FROM ubuntu:24.04 AS build
RUN apt-get update && apt-get install -y --no-install-recommends \
      build-essential cmake ca-certificates && rm -rf /var/lib/apt/lists/*
WORKDIR /src
COPY cache/ cache/
COPY bench/store_bench.cpp bench/store_bench.cpp
RUN cd cache && cmake --preset release >/dev/null && cmake --build --preset release --target fl-cache -j"$(nproc)"

FROM ubuntu:24.04
RUN apt-get update && apt-get install -y --no-install-recommends libstdc++6 && rm -rf /var/lib/apt/lists/* \
    && useradd --system --no-create-home --uid 10001 flcache
COPY --from=build /src/cache/build/release/fl-cache /usr/local/bin/fl-cache
USER 10001
EXPOSE 6400 9400
# SIGTERM (docker stop / Kubernetes) triggers a clean drain and exit 0.
ENTRYPOINT ["/usr/local/bin/fl-cache"]
CMD ["--port", "6400", "--metrics-port", "9400"]
