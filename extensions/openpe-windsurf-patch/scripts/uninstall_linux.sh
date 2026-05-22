#!/bin/bash
# Linux launcher for openpe-windsurf-patch uninstall.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
SUBPROJECT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"

echo "============================================================================"
echo "  openpe-windsurf-patch  ·  uninstall (Linux)"
echo "============================================================================"
echo

if ! command -v python3 >/dev/null 2>&1; then
  echo "✗ python3 not found"
  exit 1
fi

NEED_SUDO=0
for candidate in /opt/Windsurf /usr/share/windsurf; do
  if [ -d "$candidate" ]; then
    NEED_SUDO=1
    break
  fi
done

if [ "$NEED_SUDO" -eq 1 ] && [ "$(id -u)" -ne 0 ]; then
  echo "▸ Re-running under sudo for system-path Windsurf install."
  exec sudo -E python3 -m installer uninstall "$@"
fi

cd "$SUBPROJECT_DIR"
python3 -m installer uninstall "$@"
UNINSTALL_RC=$?

if [ $UNINSTALL_RC -eq 0 ]; then
  echo "✓ uninstall complete · restart Windsurf"
else
  echo "✗ uninstall exited with code $UNINSTALL_RC"
fi
exit $UNINSTALL_RC
