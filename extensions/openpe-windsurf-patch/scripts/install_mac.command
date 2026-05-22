#!/bin/bash
# Double-click launcher for the openpe-windsurf-patch installer on macOS.
# Validates Python availability, locates the installer relative to this
# script, ensures the inject payload is built, then re-runs install
# under sudo with the EULA disclaimer flag passed through so the user
# sees the prompt before any mutation happens.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
SUBPROJECT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"

echo "============================================================================"
echo "  openpe-windsurf-patch  ·  install (macOS)"
echo "============================================================================"
echo "subproject: $SUBPROJECT_DIR"
echo

if ! command -v python3 >/dev/null 2>&1; then
  echo "✗ python3 not found in PATH"
  echo "  Install Python 3.8+ from https://www.python.org/downloads/ and retry."
  read -r -p "Press return to exit…" _
  exit 1
fi

PYTHON_VERSION="$(python3 --version 2>&1)"
echo "✓ $PYTHON_VERSION"

INJECT_PAYLOAD="$SUBPROJECT_DIR/inject/dist/inject.js"
if [ ! -f "$INJECT_PAYLOAD" ]; then
  echo "  ! inject payload missing at $INJECT_PAYLOAD"
  if command -v npm >/dev/null 2>&1; then
    echo "  → building it now via npm…"
    (cd "$SUBPROJECT_DIR/inject" && npm install --no-audit --no-fund && npm run build)
  else
    echo "✗ npm not found; cannot build inject payload."
    echo "  Install Node.js 18+ (https://nodejs.org/) then re-run this script."
    read -r -p "Press return to exit…" _
    exit 1
  fi
fi

echo
echo "▸ Running installer under sudo (so it can write to /Applications/Windsurf.app)."
echo "  You will be prompted for your macOS password and the EULA disclaimer."
echo

cd "$SUBPROJECT_DIR"
sudo -E python3 -m installer install "$@"
INSTALL_RC=$?

echo
echo "============================================================================"
if [ $INSTALL_RC -eq 0 ]; then
  echo "  ✓ install complete · restart Windsurf to load the openPE logo button"
else
  echo "  ✗ install exited with code $INSTALL_RC · see message above"
fi
echo "============================================================================"
read -r -p "Press return to close…" _
exit $INSTALL_RC
