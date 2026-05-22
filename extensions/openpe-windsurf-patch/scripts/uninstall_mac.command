#!/bin/bash
# Double-click launcher for openpe-windsurf-patch uninstall on macOS.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
SUBPROJECT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"

echo "============================================================================"
echo "  openpe-windsurf-patch  ·  uninstall (macOS)"
echo "============================================================================"
echo "subproject: $SUBPROJECT_DIR"
echo

if ! command -v python3 >/dev/null 2>&1; then
  echo "✗ python3 not found"
  read -r -p "Press return to exit…" _
  exit 1
fi

echo "▸ Running uninstall under sudo (so it can restore /Applications/Windsurf.app)."
echo

cd "$SUBPROJECT_DIR"
sudo -E python3 -m installer uninstall "$@"
UNINSTALL_RC=$?

echo
echo "============================================================================"
if [ $UNINSTALL_RC -eq 0 ]; then
  echo "  ✓ uninstall complete · restart Windsurf"
else
  echo "  ✗ uninstall exited with code $UNINSTALL_RC"
fi
echo "============================================================================"
read -r -p "Press return to close…" _
exit $UNINSTALL_RC
