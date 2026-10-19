.PHONY: cache-build cache-test cache-run

cache-build:
	cd cache && cmake --preset debug && cmake --build --preset debug

cache-test: cache-build
	cd cache && ctest --preset debug

cache-run: cache-build
	./cache/build/debug/fl-cache --port 6400
