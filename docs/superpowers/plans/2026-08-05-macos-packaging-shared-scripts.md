# Shared macOS Packaging Scripts Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Extract the macOS app-bundle assemble/sign logic into shared scripts used by both the Makefile and release.yml, fixing issue #442's drift (missing provisioning profiles, stale product name, wrong paths, no verify gate).

**Architecture:** Two new bash scripts (`scripts/assemble-macos-bundle.sh`, `scripts/sign-macos-bundle.sh`) become the single source of truth, extracted verbatim from release.yml's current steps. The Makefile targets and release.yml steps become one-line callers. A Linux CI smoke test assembles a fake bundle to catch path/case drift.

**Tech Stack:** Bash, Make, GitHub Actions, Go, codesign.

**Spec:** `docs/superpowers/specs/2026-08-05-macos-packaging-shared-scripts-design.md` — read it before starting.

**Branch:** work on `issue-442-macos-packaging-shared-scripts` (already exists, contains the spec).

**Context you need:** This repo builds a macOS app bundle (`AgentSH.app`) containing Go binaries (`Contents/MacOS/`), a system extension, an XPC service, and a helper app. Restricted entitlements (e.g. `endpoint-security.client`) require an embedded provisioning profile or macOS (AMFI) refuses to run the code — that was issue #436. `scripts/verify-macos-bundle.sh` (already exists) checks a built bundle for those profiles. The release pipeline does all this correctly; the Makefile's local path drifted. Signing requires macOS + a signing identity, so signing is NOT testable in this implementation — assembly is, and that's what the smoke test covers.

---

### Task 1: Assemble script + smoke test

**Files:**
- Create: `scripts/assemble-macos-bundle.sh`
- Create: `scripts/test-assemble-macos-bundle.sh` (the test)

- [ ] **Step 1: Write the failing smoke test**

Create `scripts/test-assemble-macos-bundle.sh` with this exact content:

```bash
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
```

Make it executable: `chmod +x scripts/test-assemble-macos-bundle.sh`

- [ ] **Step 2: Run the test to verify it fails**

Run: `scripts/test-assemble-macos-bundle.sh`
Expected: FAIL — `scripts/assemble-macos-bundle.sh: No such file or directory`

- [ ] **Step 3: Write the assemble script**

Create `scripts/assemble-macos-bundle.sh` with this exact content. It is a verbatim extraction of release.yml's "Assemble app bundle" step (release.yml lines 355–399 on `main` @ `b16add89`) plus preflight checks — do not "improve" the copy steps:

```bash
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
```

Make it executable: `chmod +x scripts/assemble-macos-bundle.sh`

- [ ] **Step 4: Run the test to verify it passes**

Run: `scripts/test-assemble-macos-bundle.sh`
Expected: `PASS: assemble smoke test`, exit 0.

- [ ] **Step 5: Commit**

```bash
git add scripts/assemble-macos-bundle.sh scripts/test-assemble-macos-bundle.sh
git commit -m "feat(build): shared macOS bundle assemble script + smoke test (#442)"
```

---

### Task 2: Sign script

**Files:**
- Create: `scripts/sign-macos-bundle.sh`

Signing needs macOS + an identity, so there is no runnable test here. Validation is `bash -n` (and shellcheck if installed). The script must reproduce release.yml's "Sign app bundle (inside-out)" step (lines 401–430) EXACTLY, plus the Go-binary loop from the "Create and sign universal Go binaries" step (lines 317–335): `agentsh-shell-shim` signs with no entitlements; every other `Contents/MacOS` binary signs with `agentsh.entitlements`. Do NOT change which binaries get entitlements — the spec explicitly preserves release behavior verbatim (including `agentsh-stub` keeping the restricted entitlement).

- [ ] **Step 1: Write the sign script**

Create `scripts/sign-macos-bundle.sh` with this exact content:

```bash
#!/usr/bin/env bash
# Sign the macOS app bundle inside-out (nested code first, outer app last).
# Single source of truth for local (Makefile) and release (release.yml)
# signing — fix signing here, not in the callers (issue #442).
#
# Usage: SIGNING_IDENTITY="Developer ID Application: ..." \
#          sign-macos-bundle.sh <app-bundle-path>
#
# Must run from the repository root (entitlements paths are repo-relative).
set -euo pipefail

APP="${1:?usage: SIGNING_IDENTITY=... sign-macos-bundle.sh <app-bundle-path>}"
SYSEXT="ai.canyonroad.agentsh.SysExt.systemextension"

if [ -z "${SIGNING_IDENTITY:-}" ]; then
  echo "error: SIGNING_IDENTITY must be set (see 'security find-identity -v -p codesigning')" >&2
  exit 1
fi

# 1. Go binaries. agentsh-shell-shim uses minimal (no) entitlements; every
# other binary gets the app entitlements — this matches the release
# pipeline's historical per-binary selection exactly.
for bin in "${APP}/Contents/MacOS"/*; do
  echo "Signing $(basename "$bin")"
  if [ "$(basename "$bin")" = "agentsh-shell-shim" ]; then
    codesign --force --sign "$SIGNING_IDENTITY" \
      --options runtime --timestamp \
      "$bin"
  else
    codesign --force --sign "$SIGNING_IDENTITY" \
      --entitlements macos/AgentSH/agentsh/agentsh.entitlements \
      --options runtime --timestamp \
      "$bin"
  fi
done

# 2. System Extension
codesign --force --sign "$SIGNING_IDENTITY" \
  --entitlements macos/AgentSH/SysExt.entitlements \
  --options runtime --timestamp \
  "${APP}/Contents/Library/SystemExtensions/${SYSEXT}"

# 3. XPC Service
codesign --force --sign "$SIGNING_IDENTITY" \
  --options runtime --timestamp \
  "${APP}/Contents/XPCServices/xpc.xpc"

# 4. Approval Dialog
codesign --force --sign "$SIGNING_IDENTITY" \
  --entitlements macos/AgentSH/approval-dialog/approval-dialog.entitlements \
  --options runtime --timestamp \
  "${APP}/Contents/Resources/approval-dialog.app"

# 5. Main app bundle
codesign --force --sign "$SIGNING_IDENTITY" \
  --entitlements macos/AgentSH/agentsh/agentsh.entitlements \
  --options runtime --timestamp \
  "${APP}"

# 6. Verify
codesign --verify --deep --strict --verbose=2 "${APP}"
```

Make it executable: `chmod +x scripts/sign-macos-bundle.sh`

- [ ] **Step 2: Validate syntax**

Run: `bash -n scripts/sign-macos-bundle.sh && echo OK`
Expected: `OK`
Also run `shellcheck scripts/sign-macos-bundle.sh scripts/assemble-macos-bundle.sh scripts/test-assemble-macos-bundle.sh` if shellcheck is installed; fix any errors (info/style-level findings may be ignored).

- [ ] **Step 3: Verify SIGNING_IDENTITY guard works**

Run: `scripts/sign-macos-bundle.sh build/Nonexistent.app; echo "exit=$?"`
Expected: the `error: SIGNING_IDENTITY must be set` message and `exit=1`.

- [ ] **Step 4: Commit**

```bash
git add scripts/sign-macos-bundle.sh
git commit -m "feat(build): shared macOS bundle sign script (#442)"
```

---

### Task 3: Makefile targets call the scripts

**Files:**
- Modify: `Makefile:90-145` (targets `build-macos-go`, `build-swift`, `assemble-bundle`, `sign-bundle`, `build-macos-enterprise`)

- [ ] **Step 1: Rewrite `build-macos-go`**

Replace the current target (Makefile lines 90–93):

```make
# Build Go binary for macOS (CGO disabled for cross-compilation)
build-macos-go:
	mkdir -p build/AgentSH.app/Contents/MacOS
	GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 go build $(LDFLAGS) -o build/AgentSH.app/Contents/MacOS/agentsh ./cmd/agentsh
```

with:

```make
# Build the Go binaries that ship in the app bundle. agentsh needs CGO for
# system extension support (nofuse: no macFUSE headers required), matching
# the release pipeline's rebuild.
build-macos-go:
	mkdir -p build/go-local
	GOOS=darwin GOARCH=arm64 CGO_ENABLED=1 go build -tags nofuse $(LDFLAGS) -o build/go-local/agentsh ./cmd/agentsh
	GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 go build $(LDFLAGS) -o build/go-local/agentsh-shell-shim ./cmd/agentsh-shell-shim
	GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 go build $(LDFLAGS) -o build/go-local/agentsh-stub ./cmd/agentsh-stub
```

(Makefile recipes are TAB-indented — keep the tabs.)

- [ ] **Step 2: Fix the `build-swift` project path case**

In the `build-swift` target (line 98), change `-project macos/agentsh/agentsh.xcodeproj \` to `-project macos/AgentSH/agentsh.xcodeproj \` (the directory on disk is `macos/AgentSH/`; the lowercase form only works on case-insensitive filesystems).

- [ ] **Step 3: Replace `assemble-bundle`, `sign-bundle`, `build-macos-enterprise`**

Replace all three targets (currently lines 106–145, from the `# Assemble app bundle` comment through the `build-macos-enterprise` recipe) with:

```make
# Assemble app bundle (shared logic: scripts/assemble-macos-bundle.sh)
assemble-bundle: build-macos-go build-swift
	GO_BIN_DIR=build/go-local scripts/assemble-macos-bundle.sh build/AgentSH.app

# Sign bundle (requires SIGNING_IDENTITY env var; shared logic:
# scripts/sign-macos-bundle.sh)
sign-bundle:
	scripts/sign-macos-bundle.sh build/AgentSH.app

# Full enterprise build, gated on provisioning-profile verification (#440)
build-macos-enterprise: assemble-bundle sign-bundle
	scripts/verify-macos-bundle.sh build/AgentSH.app
	@echo "Enterprise build complete: build/AgentSH.app"
```

- [ ] **Step 4: Verify the Makefile parses and the Go targets build**

Run: `make -n assemble-bundle | head -20`
Expected: prints the go build commands and the `GO_BIN_DIR=build/go-local scripts/assemble-macos-bundle.sh build/AgentSH.app` line (no `*** missing separator` errors).

Run: `make build-macos-go`
Expected: succeeds; `ls build/go-local/` shows `agentsh agentsh-shell-shim agentsh-stub`. (Requires macOS with Xcode command-line tools for the CGO build — this plan is executed on the maintainer's Mac, so that holds. Do not run `make assemble-bundle`/`build-swift` here; the full Xcode build is slow and is covered by the acceptance run at the end.)

- [ ] **Step 5: Commit**

```bash
git add Makefile
git commit -m "fix(build): Makefile packaging targets use shared scripts (#442)"
```

---

### Task 4: release.yml calls the scripts

**Files:**
- Modify: `.github/workflows/release.yml` (steps "Create and sign universal Go binaries" ~line 297, "Build Swift targets (universal)" ~line 342, "Assemble app bundle" ~line 355, "Sign app bundle (inside-out)" ~line 401)

The goal is zero behavior change: the same files get the same signatures in the same order — the logic just lives in the scripts now.

- [ ] **Step 1: Trim the universal-binaries step**

Replace the "Create and sign universal Go binaries" step (name, `env:` block, and `run:` body — the shared sign script now signs these binaries post-assembly, so pre-signing here would be overwritten by `--force` anyway) with:

```yaml
      - name: Create universal Go binaries
        run: |
          mkdir -p build/go-universal
          # Lipo each Mach-O binary into a universal. Signing happens later,
          # inside scripts/sign-macos-bundle.sh, after assembly.
          created=0
          for bin in unsigned-arm64/agentsh*; do
            name=$(basename "$bin")
            [ -f "$bin" ] || continue
            file "$bin" | grep -q "Mach-O" || continue
            amd64_bin="unsigned-amd64/${name}"
            if [ -f "$amd64_bin" ]; then
              lipo -create -output "build/go-universal/${name}" \
                "$bin" "$amd64_bin"
              echo "Created universal binary: ${name}"
            else
              cp "$bin" "build/go-universal/${name}"
              echo "Copied arm64-only binary: ${name}"
            fi
            lipo -info "build/go-universal/${name}"
            created=$((created + 1))
          done
          if [ "$created" -eq 0 ]; then
            echo "::error::No universal binaries were created"
            exit 1
          fi
          echo "Created $created binaries"
```

- [ ] **Step 2: Fix the xcodebuild project path case**

In the "Build Swift targets (universal)" step (~line 345), change `-project macos/agentsh/agentsh.xcodeproj \` to `-project macos/AgentSH/agentsh.xcodeproj \`.

- [ ] **Step 3: Replace the assemble step body**

Replace the entire "Assemble app bundle" step with:

```yaml
      - name: Assemble app bundle
        run: GO_BIN_DIR=build/go-universal scripts/assemble-macos-bundle.sh build/AgentSH.app
```

- [ ] **Step 4: Replace the sign step body**

Replace the entire "Sign app bundle (inside-out)" step with:

```yaml
      - name: Sign app bundle (inside-out)
        env:
          SIGNING_IDENTITY: ${{ secrets.MACOS_SIGNING_IDENTITY }}
        run: scripts/sign-macos-bundle.sh build/AgentSH.app
```

Leave the following "Verify provisioning profiles" step exactly as is.

- [ ] **Step 5: Validate YAML and diff scope**

Run: `python3 -c "import yaml; yaml.safe_load(open('.github/workflows/release.yml')); print('YAML OK')"`
Expected: `YAML OK`

Run: `git diff .github/workflows/release.yml`
Expected: changes confined to the four steps above; in particular the notarization, DMG, and verify steps are untouched.

- [ ] **Step 6: Commit**

```bash
git add .github/workflows/release.yml
git commit -m "refactor(release): assemble/sign macOS bundle via shared scripts (#442)"
```

---

### Task 5: Smoke test in Linux CI

**Files:**
- Modify: `.github/workflows/ci.yml` (job `test-linux`, after the `smoke` step ~line 53)

- [ ] **Step 1: Add the CI step**

In `.github/workflows/ci.yml`, in the `test-linux` job, directly after the `- name: smoke` step, add:

```yaml
      - name: macOS bundle assemble smoke test
        run: scripts/test-assemble-macos-bundle.sh
```

- [ ] **Step 2: Validate YAML**

Run: `python3 -c "import yaml; yaml.safe_load(open('.github/workflows/ci.yml')); print('YAML OK')"`
Expected: `YAML OK`

- [ ] **Step 3: Commit**

```bash
git add .github/workflows/ci.yml
git commit -m "ci: run macOS bundle assemble smoke test on Linux (#442)"
```

---

### Task 6: Update docs/macos-build.md

**Files:**
- Modify: `docs/macos-build.md`

The targets and `SIGNING_IDENTITY` flow are unchanged, so most of the doc stands. Three drifted spots need fixing:

- [ ] **Step 1: Fix the `build-macos-go` description**

Find the section describing `make build-macos-go` (around line 55; it mentions CGO-disabled builds and an `AgentSH-amd64.app` output). Rewrite it to say: builds `agentsh` (CGO enabled, `nofuse` tag — required for system extension support), `agentsh-shell-shim`, and `agentsh-stub` into `build/go-local/`, arm64.

- [ ] **Step 2: Note the verify gate**

In the "Full Enterprise Build" section (~line 92), after the `make build-macos-enterprise` example, add one sentence: the target finishes by running `scripts/verify-macos-bundle.sh build/AgentSH.app`, which fails the build if the provisioning profiles are missing from the bundle.

- [ ] **Step 3: Update the Output Structure tree**

In the "Output Structure" section (~line 100), update the tree so `Contents/MacOS/` lists `agentsh`, `agentsh-shell-shim`, `agentsh-stub`; add `Contents/embedded.provisionprofile`; add `embedded.provisionprofile` under the sysext's `Contents/`; and use the real sysext directory name `ai.canyonroad.agentsh.SysExt.systemextension` (the doc currently writes `sysext` in lowercase).

- [ ] **Step 4: Commit**

```bash
git add docs/macos-build.md
git commit -m "docs: update macos-build.md for shared packaging scripts (#442)"
```

---

### Task 7: Final validation and PR

- [ ] **Step 1: Re-run everything runnable**

```bash
scripts/test-assemble-macos-bundle.sh
bash -n scripts/assemble-macos-bundle.sh scripts/sign-macos-bundle.sh
go build ./...
GOOS=windows go build ./...
```

Expected: smoke test PASS, both builds succeed (per CLAUDE.md's pre-commit checklist; no Go source changed, so this is a sanity check only — the full `go test ./...` suite is left to CI).

- [ ] **Step 2: Push and open the PR**

```bash
git push -u origin issue-442-macos-packaging-shared-scripts
gh pr create --title "fix(build): share macOS assemble/sign between Makefile and release.yml (#442)" --body "$(cat <<'EOF'
Closes #442.

Extracts the macOS app-bundle assemble/sign logic into
`scripts/assemble-macos-bundle.sh` and `scripts/sign-macos-bundle.sh`,
called by both the Makefile and release.yml, so the two paths can no
longer drift (the drift class that produced #436 and #442).

- Local bundles now embed both provisioning profiles, use the real
  sysext product name, canonical `macos/AgentSH/` paths, ship the full
  Go binary set (`agentsh` with CGO+nofuse, shim, stub) plus default
  config/policies, and `build-macos-enterprise` gates on
  `scripts/verify-macos-bundle.sh` (#440).
- release.yml behavior is unchanged: same signatures in the same order;
  its universal-binaries step keeps lipo and drops the codesign loop
  (subsumed by the shared sign script post-assembly).
- New Linux CI smoke test assembles a fake bundle on a case-sensitive
  filesystem, permanently catching path/case drift macOS runners miss.

Spec: docs/superpowers/specs/2026-08-05-macos-packaging-shared-scripts-design.md

🤖 Generated with [Claude Code](https://claude.com/claude-code)
EOF
)"
```

- [ ] **Step 3: Acceptance (manual, maintainer's Mac)**

Not automatable in CI: run `SIGNING_IDENTITY="Apple Development" make build-macos-enterprise` on a Mac with Xcode and confirm the verify gate passes. The release-side extraction is exercised by the next tagged release (verify + notarization gates).
