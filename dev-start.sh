#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

cleanup() {
  if [[ -n "${SERVER_PID:-}" ]] && ps -p "$SERVER_PID" >/dev/null 2>&1; then
    echo "Stopping TorrServer (PID $SERVER_PID)"
    kill "$SERVER_PID"
    wait "$SERVER_PID" 2>/dev/null || true
  fi
}

trap cleanup EXIT INT TERM

if ! command -v yarn >/dev/null 2>&1; then
  echo "yarn is required but not found in PATH" >&2
  exit 1
fi

pushd "$ROOT_DIR/server" >/dev/null
GO111MODULE=on go run ./cmd &
SERVER_PID=$!
echo "TorrServer backend started (PID $SERVER_PID)"
popd >/dev/null

pushd "$ROOT_DIR/web" >/dev/null
export NODE_OPTIONS=--openssl-legacy-provider

if [[ ! -d node_modules ]]; then
  echo "Installing web dependencies..."
  yarn install
fi

yarn start
popd >/dev/null
