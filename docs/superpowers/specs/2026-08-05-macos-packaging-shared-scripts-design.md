# macOS Packaging: Shared Assemble/Sign Scripts

**Date:** 2026-08-05
**Issue:** [#442](https://github.com/canyonroad/agentsh/issues/442)
**Status:** Approved

## Problem

The Makefile's local packaging targets (`assemble-bundle`, `sign-bundle`,
`build-macos-enterprise`) and the release pipeline
(`.github/workflows/release.yml`) each carry their own copy of the macOS app
bundle assemble/sign logic. The copies have drifted twice: the #436 fix
(embedding provisioning profiles so AMFI will exec the system extension)
landed only in release.yml, and #442 catalogs the resulting gap. A locally
built bundle today either fails to assemble or is rejected by AMFI at
runtime.

Full drift inventory (Makefile vs. release.yml on `main` @ `b16add89`):

1. **No provisioning profiles embedded.** Neither
   `macos/AgentSH/AgentSH_Distribution.provisionprofile` into the app nor
   `macos/AgentSH/AgentSH_SysExt_Distribution.provisionprofile` into the
   sysext. The sysext carries the restricted `endpoint-security.client`
   entitlement with no granting profile, so AMFI refuses to exec it (#436).
2. **Stale sysext product name.** The Makefile copies
   `SysExt.systemextension`; Xcode names the product
   `ai.canyonroad.agentsh.SysExt.systemextension`, so `assemble-bundle`
   fails outright.
3. **Path case drift.** The Makefile references `macos/agentsh/...` for
   entitlements and the Xcode project; the directory is `macos/AgentSH/`.
   Works only on case-insensitive filesystems. release.yml has the same
   latent bug in its `xcodebuild -project` path.
4. **No verify gate.** release.yml runs `scripts/verify-macos-bundle.sh`
   (#440); the Makefile path has no equivalent, so a broken local bundle
   fails silently at runtime.
5. **Missing bundle resources.** release.yml copies `config.yml`,
   `default-policy.yml`, and `configs/policies/*.yaml` into
   `Contents/Resources/`; the Makefile copies none of them.
6. **Wrong Go binary set.** release.yml bundles `agentsh`,
   `agentsh-shell-shim`, and `agentsh-stub` into `Contents/MacOS/`; the
   Makefile builds only `agentsh`.
7. **Wrong `agentsh` build flags.** release.yml rebuilds `agentsh` with
   `CGO_ENABLED=1 -tags nofuse` for system extension support; the Makefile
   uses `CGO_ENABLED=0`.

## Goal

One canonical implementation of assemble and sign, used by both callers,
with the verify gate on both paths. Packaging fixes then land in exactly one
place, and `make build-macos-enterprise` produces a bundle AMFI will run.

## Design

### New script: `scripts/assemble-macos-bundle.sh <app-bundle-path>`

Verbatim extraction of release.yml's "Assemble app bundle" step.

Environment:

- `PRODUCTS_DIR` — Xcode build products directory. Default
  `build/DerivedData/Build/Products/Release`.
- `GO_BIN_DIR` — directory of Go binaries to copy into `Contents/MacOS/`.
  Required. release.yml passes `build/go-universal`; the Makefile passes
  `build/go-local`.

Steps:

1. Copy `${GO_BIN_DIR}/*` into `Contents/MacOS/`.
2. Copy `macos/AgentSH-files/Info.plist` into `Contents/`.
3. Copy `${PRODUCTS_DIR}/ai.canyonroad.agentsh.SysExt.systemextension` into
   `Contents/Library/SystemExtensions/`.
4. Copy `${PRODUCTS_DIR}/xpc.xpc` into `Contents/XPCServices/`.
5. Copy `${PRODUCTS_DIR}/approval-dialog.app` into `Contents/Resources/`.
6. Copy `macos/AgentSH/AgentSH_Distribution.provisionprofile` to
   `Contents/embedded.provisionprofile`.
7. Copy `macos/AgentSH/AgentSH_SysExt_Distribution.provisionprofile` to the
   sysext's `Contents/embedded.provisionprofile`.
8. Copy `config.yml`, `default-policy.yml`, and `configs/policies/*.yaml`
   into `Contents/Resources/` (policies under
   `Resources/configs/policies/`).
9. Print the resulting bundle tree.

### New script: `scripts/sign-macos-bundle.sh <app-bundle-path>`

Inside-out signing. Requires `SIGNING_IDENTITY` in the environment. All
signing uses `--force --options runtime --timestamp`.

1. Each binary in `Contents/MacOS/`: `agentsh` signs with
   `macos/AgentSH/agentsh/agentsh.entitlements`; all others (shim, stub)
   sign with no entitlements. This mirrors release.yml's per-binary
   entitlement selection.
2. The sysext, with `macos/AgentSH/SysExt.entitlements`.
3. `xpc.xpc`, no entitlements.
4. `approval-dialog.app`, with
   `macos/AgentSH/approval-dialog/approval-dialog.entitlements`.
5. The outer app, with `macos/AgentSH/agentsh/agentsh.entitlements`.
6. `codesign --verify --deep --strict --verbose=2` on the app.

All repo paths use canonical case (`macos/AgentSH/...`).

### Caller: release.yml

- The "Assemble app bundle" step body becomes
  `GO_BIN_DIR=build/go-universal scripts/assemble-macos-bundle.sh build/AgentSH.app`.
- The "Sign app bundle (inside-out)" step body becomes
  `scripts/sign-macos-bundle.sh build/AgentSH.app`.
- The "Create and sign universal Go binaries" step keeps its lipo logic and
  drops its codesign loop: the shared sign script signs those binaries
  post-assembly, and a later `--force` re-sign would overwrite the earlier
  signatures anyway. The step's zero-binaries sanity check stays.
- Fix the `xcodebuild -project` path case to
  `macos/AgentSH/agentsh.xcodeproj`.
- The existing "Verify provisioning profiles" step is unchanged.

Net behavior change to the release pipeline: none. Same signatures, same
order, guarded by the existing verify and notarization gates on the next
tag.

### Caller: Makefile

- `build-macos-go`: build the release binary set into `build/go-local/`,
  `GOARCH=arm64` —
  - `agentsh` with `CGO_ENABLED=1 -tags nofuse` (matches release.yml's
    system-extension-support rebuild),
  - `agentsh-shell-shim` and `agentsh-stub` with `CGO_ENABLED=0`.
- `assemble-bundle`:
  `GO_BIN_DIR=build/go-local scripts/assemble-macos-bundle.sh build/AgentSH.app`.
- `sign-bundle`: `scripts/sign-macos-bundle.sh build/AgentSH.app`.
- `build-macos-enterprise`: assemble + sign, then
  `scripts/verify-macos-bundle.sh build/AgentSH.app` as the final step.
- `build-swift`: fix the project path case to
  `macos/AgentSH/agentsh.xcodeproj`.

### Docs

`docs/macos-build.md` keeps the same targets and `SIGNING_IDENTITY` flow;
update any step-level descriptions that no longer match (e.g. the verify
gate now runs as part of `build-macos-enterprise`).

## Error handling

Both scripts run `set -euo pipefail` and preflight with actionable errors:

- `sign-macos-bundle.sh` exits with a message naming `SIGNING_IDENTITY` if
  it is unset or empty.
- `assemble-macos-bundle.sh` exits if the sysext product is missing from
  `PRODUCTS_DIR` ("run `make build-swift` first"), if `GO_BIN_DIR` is unset
  or empty, or if either provisioning profile is missing, naming the
  expected path in each case.

## Testing

- **Assemble smoke test**, runnable anywhere (no Xcode, no identity): fake
  `PRODUCTS_DIR` and `GO_BIN_DIR` with stub files, run
  `assemble-macos-bundle.sh`, assert the expected bundle tree including
  both `embedded.provisionprofile` files. Wire it into Linux CI: Linux
  filesystems are case-sensitive, so this permanently catches the
  `macos/agentsh` vs `macos/AgentSH` bug class that macOS runners cannot
  see.
- Signing and verification need Xcode plus a signing identity: acceptance
  is `make build-macos-enterprise` passing the verify gate on a developer
  Mac, and the next release tag exercising the extracted release path.

## Out of scope

Local notarization, universal (multi-arch) local binaries, local DMG
creation, and `agentsh-macwrap` (commented out in `.goreleaser.yml`).
