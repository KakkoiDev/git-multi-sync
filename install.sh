#!/usr/bin/env bash
set -euo pipefail

# git-multi-sync installer. Builds the `gms` binary from source and installs it.
#
#   Local (inside a clone):   ./install.sh
#   Remote:                   curl -fsSL https://raw.githubusercontent.com/KakkoiDev/git-multi-sync/main/install.sh | bash
#
# Environment:
#   BINDIR    install directory (default: $HOME/.local/bin)
#   GMS_REPO  git URL to clone when not run from inside the source tree
#             (default: https://github.com/KakkoiDev/git-multi-sync)

BINDIR="${BINDIR:-$HOME/.local/bin}"
GMS_REPO="${GMS_REPO:-https://github.com/KakkoiDev/git-multi-sync}"

command -v go >/dev/null 2>&1 || {
  echo "error: go is required to build gms (https://go.dev/dl/)" >&2
  exit 1
}

is_src() { [ -f "$1/go.mod" ] && grep -q 'git-multi-sync$' "$1/go.mod" 2>/dev/null; }

# Locate the source: current dir, then the script's dir, else clone GMS_REPO.
SRC=""
if is_src "$(pwd)"; then
  SRC="$(pwd)"
elif [ -n "${BASH_SOURCE:-}" ]; then
  d="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
  if is_src "$d"; then SRC="$d"; fi
fi

if [ -z "$SRC" ]; then
  command -v git >/dev/null 2>&1 || {
    echo "error: git is required to fetch the source" >&2
    exit 1
  }
  TMP="$(mktemp -d)"
  trap 'rm -rf "$TMP"' EXIT
  echo "cloning $GMS_REPO ..."
  git clone --depth 1 "$GMS_REPO" "$TMP/src" >/dev/null 2>&1
  SRC="$TMP/src"
fi

mkdir -p "$BINDIR"
echo "building gms from $SRC ..."
( cd "$SRC" && go build -o "$BINDIR/gms" . )
chmod 0755 "$BINDIR/gms"
echo "installed: $BINDIR/gms"

case ":$PATH:" in
  *":$BINDIR:"*) ;;
  *)
    echo "note: $BINDIR is not on your PATH. add to your shell profile:"
    echo "      export PATH=\"$BINDIR:\$PATH\""
    ;;
esac

echo "run: gms help"
