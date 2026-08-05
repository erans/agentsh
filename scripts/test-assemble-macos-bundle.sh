#!/usr/bin/env bash
# Smoke test for assemble-macos-bundle.sh. Runs anywhere (no Xcode, no
# signing identity): fakes the Xcode products and Go binaries, assembles,
# and asserts the bundle tree. Running on a case-sensitive filesystem
# (Linux CI) also catches macos/agentsh-vs-macos/AgentSH path drift.
set -euo pipefail
cd "$(dirname "$0")/.."

SYSEXT="ai.canyonroad.agentsh.SysExt.systemextension"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

# Fake Xcode build products
mkdir -p "$TMP/products/$SYSEXT/Contents/MacOS"
touch "$TMP/products/$SYSEXT/Contents/MacOS/sysext"
mkdir -p "$TMP/products/xpc.xpc/Contents"
mkdir -p "$TMP/products/approval-dialog.app/Contents"

# Fake Go binaries
mkdir -p "$TMP/go-bin"
touch "$TMP/go-bin/agentsh" "$TMP/go-bin/agentsh-shell-shim" "$TMP/go-bin/agentsh-stub"

APP="$TMP/AgentSH.app"
GO_BIN_DIR="$TMP/go-bin" PRODUCTS_DIR="$TMP/products" \
  scripts/assemble-macos-bundle.sh "$APP"

fail=0
require() {
  if [ ! -e "$1" ]; then
    echo "MISSING: $1" >&2
    fail=1
  fi
}

require "$APP/Contents/MacOS/agentsh"
require "$APP/Contents/MacOS/agentsh-shell-shim"
require "$APP/Contents/MacOS/agentsh-stub"
require "$APP/Contents/Info.plist"
require "$APP/Contents/Library/SystemExtensions/$SYSEXT"
require "$APP/Contents/Library/SystemExtensions/$SYSEXT/Contents/embedded.provisionprofile"
require "$APP/Contents/XPCServices/xpc.xpc"
require "$APP/Contents/Resources/approval-dialog.app"
require "$APP/Contents/embedded.provisionprofile"
require "$APP/Contents/Resources/config.yml"
require "$APP/Contents/Resources/default-policy.yml"
[ -n "$(ls "$APP/Contents/Resources/configs/policies/"*.yaml 2>/dev/null)" ] \
  || { echo "MISSING: Resources/configs/policies/*.yaml" >&2; fail=1; }

# GO_BIN_DIR must be required, not silently skipped
if PRODUCTS_DIR="$TMP/products" scripts/assemble-macos-bundle.sh "$TMP/Other.app" 2>/dev/null; then
  echo "FAIL: assemble succeeded without GO_BIN_DIR" >&2
  fail=1
fi

[ "$fail" -eq 0 ] && echo "PASS: assemble smoke test"
exit "$fail"
