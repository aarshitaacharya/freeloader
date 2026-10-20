#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/../.."
PATH="$PWD/.bin:$PATH" kind delete cluster --name freeloader
