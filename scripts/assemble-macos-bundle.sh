#!/usr/bin/env bash
# Assemble the macOS app bundle from Go binaries and Xcode build products.
# Single source of truth for local (Makefile) and release (release.yml)
# packaging — fix packaging here, not in the callers (issue #442).
#
# Usage: GO_BIN_DIR=<dir> assemble-macos-bundle.sh <app-bundle-path>
#   GO_BIN_DIR   (required) directory of Go binaries to copy into Contents/MacOS
#   PRODUCTS_DIR (optional) Xcode products dir,
#                default build/DerivedData/Build/Products/Release
#
# Must run from the repository root.
set -euo pipefail

APP="${1:?usage: GO_BIN_DIR=<dir> assemble-macos-bundle.sh <app-bundle-path>}"
PRODUCTS_DIR="${PRODUCTS_DIR:-build/DerivedData/Build/Products/Release}"
SYSEXT="ai.canyonroad.agentsh.SysExt.systemextension"
APP_PROFILE="macos/AgentSH/AgentSH_Distribution.provisionprofile"
SYSEXT_PROFILE="macos/AgentSH/AgentSH_SysExt_Distribution.provisionprofile"

if [ -z "${GO_BIN_DIR:-}" ]; then
  echo "error: GO_BIN_DIR must be set to the directory of Go binaries to bundle" >&2
  exit 1
fi
if [ ! -d "${PRODUCTS_DIR}/${SYSEXT}" ]; then
  echo "error: ${PRODUCTS_DIR}/${SYSEXT} not found — run 'make build-swift' first" >&2
  exit 1
fi
for profile in "$APP_PROFILE" "$SYSEXT_PROFILE"; do
  if [ ! -f "$profile" ]; then
    echo "error: provisioning profile not found: $profile" >&2
    exit 1
  fi
done

# Start from a clean bundle — a stale local build/AgentSH.app could carry
# leftover files (no-op on fresh CI runners).
rm -rf "${APP}"

# Go binaries
mkdir -p "${APP}/Contents/MacOS"
cp "${GO_BIN_DIR}"/* "${APP}/Contents/MacOS/"

# Info.plist for host app
cp macos/AgentSH-files/Info.plist "${APP}/Contents/"

# System Extension (Xcode names the product with the full bundle ID)
mkdir -p "${APP}/Contents/Library/SystemExtensions"
cp -R "${PRODUCTS_DIR}/${SYSEXT}" "${APP}/Contents/Library/SystemExtensions/"

# XPC Service
mkdir -p "${APP}/Contents/XPCServices"
cp -R "${PRODUCTS_DIR}/xpc.xpc" "${APP}/Contents/XPCServices/"

# Approval Dialog
mkdir -p "${APP}/Contents/Resources"
cp -R "${PRODUCTS_DIR}/approval-dialog.app" "${APP}/Contents/Resources/"

# Provisioning profile (required for restricted entitlements like
# system-extension.install)
cp "$APP_PROFILE" "${APP}/Contents/embedded.provisionprofile"

# SysExt provisioning profile (required for the restricted
# endpoint-security.client entitlement; without it AMFI refuses to
# exec the extension on user machines — issue #436)
cp "$SYSEXT_PROFILE" \
  "${APP}/Contents/Library/SystemExtensions/${SYSEXT}/Contents/embedded.provisionprofile"

# Default config and policies (used as fallback when no user/system config exists)
cp config.yml default-policy.yml "${APP}/Contents/Resources/"
mkdir -p "${APP}/Contents/Resources/configs/policies"
cp configs/policies/*.yaml "${APP}/Contents/Resources/configs/policies/"

echo "=== App bundle structure ==="
find "${APP}" -type f | sort
