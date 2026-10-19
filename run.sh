#!/usr/bin/env bash
# One-shot launcher for macOS.
# Creates a virtualenv on first run, installs deps, then starts the server
# and opens the dashboard in your default browser.

set -euo pipefail
cd "$(dirname "$0")"

PY="${PYTHON:-python3}"
VENV=".venv"

if [ ! -d "$VENV" ]; then
  echo "[setup] Creating virtualenv..."
  "$PY" -m venv "$VENV"
fi

# shellcheck disable=SC1091
source "$VENV/bin/activate"

if [ ! -f "$VENV/.deps_installed" ] || [ requirements.txt -nt "$VENV/.deps_installed" ]; then
  echo "[setup] Installing dependencies..."
  pip install --quiet --upgrade pip
  pip install --quiet -r requirements.txt
  touch "$VENV/.deps_installed"
fi

# Build the SvelteKit SPA into web/next (served at / + /_app).
if [ -f ui/package.json ]; then
  if [ ! -d ui/node_modules ] || [ ui/package-lock.json -nt ui/node_modules/.package-lock.json ] 2>/dev/null; then
    echo "[setup] Installing UI dependencies..."
    npm ci --prefix ui
  fi
  echo "[setup] Building SvelteKit UI..."
  npm run build --prefix ui
fi

PORT="${PORT:-8000}"
URL="http://localhost:${PORT}"

echo "[launch] Starting Solar Pow Monitor at $URL"
( sleep 1 && open "$URL" ) &
exec python server.py
