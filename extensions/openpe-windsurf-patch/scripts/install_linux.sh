#!/bin/bash
# Linux launcher for the openpe-windsurf-patch installer. Run from a
# terminal: `bash scripts/install_linux.sh`. Re-invokes itself via sudo
# when the target Windsurf install lives under /opt or /usr.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
SUBPROJECT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"

echo "============================================================================"
echo "  openpe-windsurf-patch  ·  install (Linux)"
echo "============================================================================"
echo "subproject: $SUBPROJECT_DIR"
echo

if ! command -v python3 >/dev/null 2>&1; then
  echo "✗ python3 not found"
  echo "  Install Python 3.8+ via your distro's package manager and retry."
  exit 1
fi
echo "✓ $(python3 --version 2>&1)"

INJECT_PAYLOAD="$SUBPROJECT_DIR/inject/dist/inject.js"
if [ ! -f "$INJECT_PAYLOAD" ]; then
  echo "  ! inject payload missing at $INJECT_PAYLOAD"
  if command -v npm >/dev/null 2>&1; then
    echo "  → building via npm…"
    (cd "$SUBPROJECT_DIR/inject" && npm install --no-audit --no-fund && npm run build)
  else
    echo "✗ npm not found; install Node.js 18+ and retry."
    exit 1
  fi
fi

# If the user has Windsurf in a system path, escalate to sudo unless
# already root. /opt and /usr need root; ~/.local/share/Windsurf does not.
NEED_SUDO=0
for candidate in /opt/Windsurf /usr/share/windsurf; do
  if [ -d "$candidate" ]; then
    NEED_SUDO=1
    break
  fi
done

if [ "$NEED_SUDO" -eq 1 ] && [ "$(id -u)" -ne 0 ]; then
  echo "▸ Windsurf is installed under a system path; re-running under sudo."
  exec sudo -E python3 -m installer install "$@"
fi

cd "$SUBPROJECT_DIR"
python3 -m installer install "$@"
INSTALL_RC=$?

echo
if [ $INSTALL_RC -eq 0 ]; then
  echo "✓ install complete · restart Windsurf to load the openPE logo button"
else
  echo "✗ install exited with code $INSTALL_RC"
fi
exit $INSTALL_RC
