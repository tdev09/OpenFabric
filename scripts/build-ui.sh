#!/bin/sh
# build-ui.sh - Build SvelteKit UI and verify dist/ exists.
set -e

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT="$SCRIPT_DIR/.."
UI_DIR="$ROOT/ui"

echo "🏗  Building OpenFabric UI…"

if [ ! -d "$UI_DIR/node_modules" ]; then
  echo "📦 Installing UI dependencies…"
  (cd "$UI_DIR" && npm install)
fi

(cd "$UI_DIR" && npm run build)

if [ ! -d "$UI_DIR/dist" ]; then
  echo "❌ UI build failed - dist/ not found"
  exit 1
fi

echo "✅ UI built to $UI_DIR/dist"
