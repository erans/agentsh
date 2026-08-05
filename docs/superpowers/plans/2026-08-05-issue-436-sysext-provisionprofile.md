# Issue #436: SysExt Provisioning Profile Embed + Release Verification — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Embed the SysExt provisioning profile into the `.systemextension` bundle in the release pipeline, and add a release-blocking verification script so a bundle whose restricted entitlements lack a granting profile can never ship again.

**Architecture:** Two independent artifacts. (1) A one-hunk change to `.github/workflows/release.yml` copying `macos/AgentSH/AgentSH_SysExt_Distribution.provisionprofile` into the sysext bundle during assembly, before the inside-out signing step seals it. (2) A standalone bash script `scripts/verify-macos-bundle.sh` that cross-checks each bundle's signed entitlements against its embedded profile, wired in as a workflow step between signing and notarization.

**Tech Stack:** GitHub Actions YAML, bash 3.2 (macOS `/bin/bash`), macOS-native tools only: `security`, `codesign`, `plutil`, `/usr/libexec/PlistBuddy`, `date`.

**Spec:** `docs/superpowers/specs/2026-08-05-issue-436-sysext-provisionprofile-design.md` — read it before starting; the "Design" section defines every check the script performs.

**Branch:** work on `feature/issue-436-sysext-provisionprofile` (already exists, contains the spec commit).

---

## Context you need (no prior knowledge assumed)

**The bug:** macOS refuses (via AMFI, at exec time) to run any binary signed with a *restricted* entitlement — like `com.apple.developer.endpoint-security.client` — unless the binary's bundle embeds a provisioning profile (`Contents/embedded.provisionprofile`) that grants that entitlement. `codesign --verify` and Apple's notarization do **not** check this, so a broken bundle sails through the pipeline and only fails on user machines, silently. That is exactly what shipped in v0.17.0–v0.20.5.

**Verified facts about this repo (do not re-derive; already confirmed):**
- `macos/AgentSH/AgentSH_SysExt_Distribution.provisionprofile` decodes to: app ID `WCKWMMKJ35.ai.canyonroad.agentsh.SysExt`, grants `endpoint-security.client` + `networking.networkextension`, `ProvisionsAllDevices: true`, expires `2044-03-27T19:47:48Z`.
- `macos/AgentSH/AgentSH_Distribution.provisionprofile` (app profile, already embedded by the workflow): app ID `WCKWMMKJ35.ai.canyonroad.agentsh`, grants `system-extension.install`, `ProvisionsAllDevices: true`, expires `2044-03-27T05:34:09Z`.
- SysExt entitlements file (`macos/AgentSH/SysExt.entitlements`): `endpoint-security.client` (boolean) and `networking.networkextension` (**array** — this is why the profile check is key-presence, not `== true`).
- App entitlements file (`macos/AgentSH/agentsh/agentsh.entitlements`): `system-extension.install` (boolean) only.
- The profile's `TeamIdentifier` is a plist **array**; read element 0.
- `plutil -convert json` fails on decoded profiles (plist `date` values). Use `plutil -extract <key> raw` and PlistBuddy. All extraction commands in the script below were validated against the real decoded profiles on this machine.
- `/Applications/AgentSH.app` is NOT installed on this dev machine — the "real broken artifact" test from the spec is replaced by an ad-hoc-signed fixture (Task 1, steps 4–5), which reproduces the same failure shape without needing certificates.

**No Go code changes anywhere in this plan** — the CLAUDE.md cross-compile/test gates don't apply (nothing Go is touched); do not run them.

---

### Task 1: Verification script `scripts/verify-macos-bundle.sh`

**Files:**
- Create: `scripts/verify-macos-bundle.sh` (mode 755)

- [ ] **Step 1: Write the script**

Create `scripts/verify-macos-bundle.sh` with exactly this content:

```bash
#!/bin/bash
# verify-macos-bundle.sh — verify provisioning profiles inside AgentSH.app.
#
# AMFI refuses to exec any binary signed with a restricted entitlement unless
# its bundle embeds a provisioning profile granting that entitlement. Neither
# codesign --verify nor notarization checks this, so a broken bundle only
# fails on user machines (issue #436). This script is the release gate for
# that condition, and is equally runnable against an installed
# /Applications/AgentSH.app or a mounted DMG when triaging user reports.
#
# Usage: scripts/verify-macos-bundle.sh /path/to/AgentSH.app
#
# Exit codes: 0 all checks pass (warnings allowed), 1 check failures,
# 2 usage/environment error.
#
# Deliberately no `set -e`: failures are collected and all reported at once.
set -u

PLISTBUDDY=/usr/libexec/PlistBuddy
SYSEXT_REL="Contents/Library/SystemExtensions/ai.canyonroad.agentsh.SysExt.systemextension"
# Restricted entitlements this project uses. Apple publishes no
# machine-readable classification; extend this list when a new restricted
# entitlement is adopted.
RESTRICTED_ENTITLEMENTS=(
  com.apple.developer.endpoint-security.client
  com.apple.developer.networking.networkextension
  com.apple.developer.system-extension.install
)
EXPIRY_WARN_DAYS=90

failures=()
warnings=()
passes=0

fail() { failures+=("$1"); printf 'FAIL  %s\n' "$1"; }
warn() { warnings+=("$1"); printf 'WARN  %s\n' "$1"; }
pass() { passes=$((passes + 1)); printf 'ok    %s\n' "$1"; }
die()  { printf 'error: %s\n' "$1" >&2; exit 2; }

[ "$(uname -s)" = "Darwin" ] || die "requires macOS (needs security/codesign/plutil/PlistBuddy)"
for tool in security codesign plutil date "$PLISTBUDDY"; do
  command -v "$tool" >/dev/null 2>&1 || die "required tool not found: $tool"
done
[ $# -eq 1 ] || die "usage: $0 /path/to/AgentSH.app"

APP="${1%/}"
[ -f "$APP/Contents/Info.plist" ] || die "not an app bundle (no Contents/Info.plist): $APP"

WORKDIR="$(mktemp -d)"
trap 'rm -rf "$WORKDIR"' EXIT

# check_bundle <label> <bundle-path>
check_bundle() {
  local label="$1" bundle="$2"
  local profile="$bundle/Contents/embedded.provisionprofile"
  local decoded="$WORKDIR/$label-profile.plist"
  local ents="$WORKDIR/$label-entitlements.plist"
  local ent

  printf '\n== %s: %s\n' "$label" "$bundle"

  if [ ! -d "$bundle" ]; then
    fail "$label: bundle not found at $bundle"
    return
  fi

  # Signed entitlements, needed for the profile cross-check.
  local have_ents=0
  if codesign -d --entitlements "$ents" --xml "$bundle" 2>/dev/null && [ -s "$ents" ]; then
    have_ents=1
  else
    fail "$label: cannot read signed entitlements (unsigned bundle?)"
  fi
  local sig_team
  sig_team="$(codesign -dvv "$bundle" 2>&1 | awk -F= '/^TeamIdentifier=/{print $2; exit}')"

  # Check 1: profile present and decodable.
  if [ ! -f "$profile" ]; then
    fail "$label: missing $profile"
    # With no profile, every claimed restricted entitlement is ungranted.
    if [ "$have_ents" -eq 1 ]; then
      for ent in "${RESTRICTED_ENTITLEMENTS[@]}"; do
        if "$PLISTBUDDY" -c "Print :$ent" "$ents" >/dev/null 2>&1; then
          fail "$label: restricted entitlement $ent claimed by signature but no profile grants it (AMFI will reject at exec)"
        fi
      done
    fi
    return
  fi
  if ! security cms -D -i "$profile" > "$decoded" 2>/dev/null || [ ! -s "$decoded" ]; then
    fail "$label: embedded.provisionprofile cannot be decoded (corrupt?)"
    return
  fi
  pass "$label: embedded.provisionprofile present and decodable"

  # Check 2: identity — profile app ID matches team + bundle ID, and the
  # signature's team matches the profile's. TeamIdentifier is a plist array.
  local bundle_id profile_appid profile_team
  bundle_id="$("$PLISTBUDDY" -c 'Print :CFBundleIdentifier' "$bundle/Contents/Info.plist" 2>/dev/null)"
  profile_appid="$("$PLISTBUDDY" -c 'Print :Entitlements:com.apple.application-identifier' "$decoded" 2>/dev/null)"
  profile_team="$("$PLISTBUDDY" -c 'Print :TeamIdentifier:0' "$decoded" 2>/dev/null)"

  if [ -z "$bundle_id" ]; then
    fail "$label: cannot read CFBundleIdentifier from $bundle/Contents/Info.plist"
  elif [ "$profile_appid" != "$profile_team.$bundle_id" ]; then
    fail "$label: profile application-identifier '$profile_appid' != expected '$profile_team.$bundle_id'"
  else
    pass "$label: profile application-identifier matches $profile_appid"
  fi

  if [ -z "$sig_team" ] || [ "$sig_team" = "not set" ]; then
    fail "$label: code signature has no team identifier (ad-hoc or unsigned?)"
  elif [ "$sig_team" != "$profile_team" ]; then
    fail "$label: signature team '$sig_team' != profile team '$profile_team'"
  else
    pass "$label: signature team matches profile team ($sig_team)"
  fi

  # Check 3: must be a Developer ID distribution profile. A device-limited
  # development profile passes on the build machine and fails everywhere else.
  local all_devices
  all_devices="$("$PLISTBUDDY" -c 'Print :ProvisionsAllDevices' "$decoded" 2>/dev/null)"
  if [ "$all_devices" = "true" ]; then
    pass "$label: ProvisionsAllDevices=true"
  else
    fail "$label: not a ProvisionsAllDevices distribution profile (got '${all_devices:-absent}')"
  fi

  # Check 4: expiry. plutil raw prints plist dates as ISO8601 UTC.
  local expiry_iso expiry_epoch now_epoch
  expiry_iso="$(plutil -extract ExpirationDate raw -o - "$decoded" 2>/dev/null)"
  if [ -z "$expiry_iso" ]; then
    fail "$label: profile has no readable ExpirationDate"
  else
    expiry_epoch="$(date -j -u -f '%Y-%m-%dT%H:%M:%SZ' "$expiry_iso" +%s 2>/dev/null || echo 0)"
    now_epoch="$(date -u +%s)"
    if [ "$expiry_epoch" -eq 0 ]; then
      fail "$label: cannot parse ExpirationDate '$expiry_iso'"
    elif [ "$expiry_epoch" -le "$now_epoch" ]; then
      fail "$label: profile expired on $expiry_iso"
    elif [ "$expiry_epoch" -le $((now_epoch + EXPIRY_WARN_DAYS * 86400)) ]; then
      warn "$label: profile expires within ${EXPIRY_WARN_DAYS} days ($expiry_iso)"
    else
      pass "$label: profile valid until $expiry_iso"
    fi
  fi

  # Check 5: every restricted entitlement the signature claims must be
  # granted by the profile. Key-presence, not value equality —
  # networking.networkextension is array-valued on both sides.
  if [ "$have_ents" -eq 1 ]; then
    for ent in "${RESTRICTED_ENTITLEMENTS[@]}"; do
      if "$PLISTBUDDY" -c "Print :$ent" "$ents" >/dev/null 2>&1; then
        if "$PLISTBUDDY" -c "Print :Entitlements:$ent" "$decoded" >/dev/null 2>&1; then
          pass "$label: restricted entitlement $ent granted by profile"
        else
          fail "$label: restricted entitlement $ent claimed by signature but NOT granted by profile (AMFI will reject at exec)"
        fi
      fi
    done
  fi
}

check_bundle "app" "$APP"
check_bundle "sysext" "$APP/$SYSEXT_REL"

printf '\n== summary: %d ok, %d warning(s), %d failure(s)\n' \
  "$passes" "${#warnings[@]}" "${#failures[@]}"
if [ "${#failures[@]}" -gt 0 ]; then
  printf 'failure: %s\n' "${failures[@]}"
  exit 1
fi
exit 0
```

- [ ] **Step 2: Make it executable and syntax-check it**

Run:
```bash
chmod +x scripts/verify-macos-bundle.sh
/bin/bash -n scripts/verify-macos-bundle.sh && echo "syntax ok"
```
Expected: `syntax ok`, no other output.

- [ ] **Step 3: Verify the usage/environment guards fail correctly**

Run:
```bash
scripts/verify-macos-bundle.sh; echo "exit=$?"
scripts/verify-macos-bundle.sh /tmp; echo "exit=$?"
```
Expected output:
```
error: usage: scripts/verify-macos-bundle.sh /path/to/AgentSH.app
exit=2
error: not an app bundle (no Contents/Info.plist): /tmp
exit=2
```

- [ ] **Step 4: Build a broken fixture reproducing #436 and verify the script catches it**

This is the detector's failing-test: an ad-hoc-signed bundle claiming restricted entitlements with **no** embedded profiles — the exact shape of the shipped bug. Build it in the scratchpad (do NOT commit it). `$SCRATCHPAD` below means this session's scratchpad directory. Run all commands in steps 4–5 from the repo root (the `macos/...` and `scripts/...` paths are repo-relative).

```bash
cd /Users/eran/work/canyonroad/agentsh
FIX="$SCRATCHPAD/fixture/AgentSH.app"
SX="$FIX/Contents/Library/SystemExtensions/ai.canyonroad.agentsh.SysExt.systemextension"
rm -rf "$SCRATCHPAD/fixture"
mkdir -p "$FIX/Contents/MacOS" "$SX/Contents/MacOS"

cat > "$FIX/Contents/Info.plist" <<'EOF'
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>CFBundleIdentifier</key><string>ai.canyonroad.agentsh</string>
  <key>CFBundleExecutable</key><string>agentsh</string>
  <key>CFBundlePackageType</key><string>APPL</string>
</dict></plist>
EOF
cat > "$SX/Contents/Info.plist" <<'EOF'
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>CFBundleIdentifier</key><string>ai.canyonroad.agentsh.SysExt</string>
  <key>CFBundleExecutable</key><string>ai.canyonroad.agentsh.SysExt</string>
  <key>CFBundlePackageType</key><string>SYSX</string>
</dict></plist>
EOF
cp /bin/ls "$FIX/Contents/MacOS/agentsh"
cp /bin/ls "$SX/Contents/MacOS/ai.canyonroad.agentsh.SysExt"

# Ad-hoc sign (no certificate needed) with the real entitlements files.
codesign -s - -f --entitlements macos/AgentSH/SysExt.entitlements "$SX"
codesign -s - -f --entitlements macos/AgentSH/agentsh/agentsh.entitlements "$FIX"

scripts/verify-macos-bundle.sh "$FIX"; echo "exit=$?"
```

Expected: `exit=1`, summary line `== summary: 0 ok, 0 warning(s), 5 failure(s)`, and the five failures are exactly:
- `app: missing .../embedded.provisionprofile`
- `app: restricted entitlement com.apple.developer.system-extension.install claimed by signature but no profile grants it (AMFI will reject at exec)`
- `sysext: missing .../embedded.provisionprofile`
- `sysext: restricted entitlement com.apple.developer.endpoint-security.client claimed by signature but no profile grants it (AMFI will reject at exec)`
- `sysext: restricted entitlement com.apple.developer.networking.networkextension claimed by signature but no profile grants it (AMFI will reject at exec)`

If output differs, fix the script — do not adjust expectations.

- [ ] **Step 5: Embed the real profiles into the fixture and verify checks 1–5 pass**

The Developer ID certificate isn't available locally, so the ad-hoc signature's missing team is the ONLY expected failure; everything else must pass. This validates the positive-path parsing against the real profiles.

```bash
cp macos/AgentSH/AgentSH_Distribution.provisionprofile "$FIX/Contents/embedded.provisionprofile"
cp macos/AgentSH/AgentSH_SysExt_Distribution.provisionprofile "$SX/Contents/embedded.provisionprofile"
# Re-sign so the signature covers the added files (order: inner bundle first).
codesign -s - -f --entitlements macos/AgentSH/SysExt.entitlements "$SX"
codesign -s - -f --entitlements macos/AgentSH/agentsh/agentsh.entitlements "$FIX"

scripts/verify-macos-bundle.sh "$FIX"; echo "exit=$?"
```

Expected: `exit=1` with exactly 2 failures (`app: code signature has no team identifier (ad-hoc or unsigned?)` and the same for `sysext`), and these passes, all present:
- `app: embedded.provisionprofile present and decodable`
- `app: profile application-identifier matches WCKWMMKJ35.ai.canyonroad.agentsh`
- `app: ProvisionsAllDevices=true`
- `app: profile valid until 2044-03-27T05:34:09Z`
- `app: restricted entitlement com.apple.developer.system-extension.install granted by profile`
- `sysext: embedded.provisionprofile present and decodable`
- `sysext: profile application-identifier matches WCKWMMKJ35.ai.canyonroad.agentsh.SysExt`
- `sysext: ProvisionsAllDevices=true`
- `sysext: profile valid until 2044-03-27T19:47:48Z`
- `sysext: restricted entitlement com.apple.developer.endpoint-security.client granted by profile`
- `sysext: restricted entitlement com.apple.developer.networking.networkextension granted by profile`

Summary: `== summary: 11 ok, 0 warning(s), 2 failure(s)`. On a real signed release bundle the team check passes too, giving exit 0.

- [ ] **Step 6: Commit**

```bash
git add scripts/verify-macos-bundle.sh
git commit -m "feat(release): add verify-macos-bundle.sh provisioning profile checker (#436)

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 1b: Required-claims check (from Task 1 code review)

Code review of Task 1 found a design gap: check 5 verifies claimed ⊆ granted, but nothing verifies the load-bearing entitlements are *claimed* at all. A signing regression (wrong `--entitlements` file) would pass the gate while shipping a sysext that cannot create an ES client. Spec updated (check 6). This task extends the committed script; its fixture expectations **supersede Task 1 steps 4–5 summary counts**.

**Files:**
- Modify: `scripts/verify-macos-bundle.sh`

- [ ] **Step 1: Make three edits to the script**

Edit 1 — in the header comment block, after the `# Usage:` line, add:

```bash
#
# Requires macOS 12+ (codesign --xml needs 11+, plutil -extract raw needs 12+).
```

Edit 2 — change the misleading diagnostic (it also fires for a signed bundle with zero entitlements):

```bash
    fail "$label: cannot read signed entitlements (unsigned bundle?)"
```
becomes
```bash
    fail "$label: cannot read signed entitlements (unsigned, ad-hoc, or signed with no entitlements)"
```

Edit 3 — insert the required-claims check between the `sig_team=` assignment and the `# Check 1:` comment:

```bash
  # Check 6: each bundle must actually CLAIM its load-bearing restricted
  # entitlements. Check 5 alone (claimed ⊆ granted) false-passes when a
  # signing regression drops entitlements from the signature entirely —
  # the wrong --entitlements file ships a sysext that cannot create an ES
  # client, silently (same failure character as #436).
  local required_ents=""
  case "$label" in
    app)    required_ents="com.apple.developer.system-extension.install" ;;
    sysext) required_ents="com.apple.developer.endpoint-security.client com.apple.developer.networking.networkextension" ;;
  esac
  if [ "$have_ents" -eq 1 ]; then
    for ent in $required_ents; do
      if "$PLISTBUDDY" -c "Print :$ent" "$ents" >/dev/null 2>&1; then
        pass "$label: signature claims required entitlement $ent"
      else
        fail "$label: signature does NOT claim required entitlement $ent (wrong entitlements file at signing?)"
      fi
    done
  fi
```

Placement matters: it must run **before** check 1's missing-profile early `return`, so a bundle with no profile still gets its claims verified.

- [ ] **Step 2: Syntax check**

Run: `/bin/bash -n scripts/verify-macos-bundle.sh && echo "syntax ok"` — expected `syntax ok`.

- [ ] **Step 3: Re-run the broken fixture (Task 1 step 4 build)**

Rebuild the Task 1 step 4 fixture exactly as before (ad-hoc signed, no profiles), then:

```bash
scripts/verify-macos-bundle.sh "$FIX"; echo "exit=$?"
```

Expected: `exit=1`, summary `== summary: 3 ok, 0 warning(s), 5 failure(s)`. Same five failures as Task 1 step 4, plus these three new passes:
- `app: signature claims required entitlement com.apple.developer.system-extension.install`
- `sysext: signature claims required entitlement com.apple.developer.endpoint-security.client`
- `sysext: signature claims required entitlement com.apple.developer.networking.networkextension`

- [ ] **Step 4: Re-run the profiled fixture (Task 1 step 5 build)**

Embed both real profiles and re-sign as in Task 1 step 5, then run. Expected: `exit=1`, summary `== summary: 14 ok, 0 warning(s), 2 failure(s)` — the 11 passes from Task 1 step 5 plus the 3 claims passes; still exactly the two ad-hoc team failures.

- [ ] **Step 5: Simulate the regression this check exists for**

Re-sign the fixture's sysext with the WRONG entitlements file (the app's):

```bash
codesign -s - -f --entitlements macos/AgentSH/agentsh/agentsh.entitlements "$SX"
scripts/verify-macos-bundle.sh "$FIX"; echo "exit=$?"
```

Expected: `exit=1`, summary `== summary: 10 ok, 0 warning(s), 5 failure(s)`, and the sysext failures are exactly:
- `sysext: signature does NOT claim required entitlement com.apple.developer.endpoint-security.client (wrong entitlements file at signing?)`
- `sysext: signature does NOT claim required entitlement com.apple.developer.networking.networkextension (wrong entitlements file at signing?)`
- `sysext: code signature has no team identifier (ad-hoc or unsigned?)`
- `sysext: restricted entitlement com.apple.developer.system-extension.install claimed by signature but NOT granted by profile (AMFI will reject at exec)`

(plus the unchanged `app: code signature has no team identifier` failure). Before this task, this exact scenario produced only the team failure — the gate would have passed a properly-signed equivalent.

- [ ] **Step 6: Commit**

```bash
git add scripts/verify-macos-bundle.sh
git commit -m "feat(release): require load-bearing entitlement claims in verify-macos-bundle.sh (#436)

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 2: Embed the SysExt profile in the release workflow

**Files:**
- Modify: `.github/workflows/release.yml` — "Assemble app bundle" step (the app-profile `cp` is at lines 382–384 on current main)

- [ ] **Step 1: Add the copy**

In `.github/workflows/release.yml`, find this block inside the "Assemble app bundle" step:

```yaml
          # Provisioning profile (required for restricted entitlements like system-extension.install)
          cp macos/AgentSH/AgentSH_Distribution.provisionprofile \
            "build/AgentSH.app/Contents/embedded.provisionprofile"
```

and add immediately after it:

```yaml
          # SysExt provisioning profile (required for the restricted
          # endpoint-security.client entitlement; without it AMFI refuses to
          # exec the extension on user machines — issue #436)
          cp macos/AgentSH/AgentSH_SysExt_Distribution.provisionprofile \
            "build/AgentSH.app/Contents/Library/SystemExtensions/ai.canyonroad.agentsh.SysExt.systemextension/Contents/embedded.provisionprofile"
```

The assemble step runs before "Sign app bundle (inside-out)", so the sysext signature (step 1 of the signing block) seals the profile. Do not touch the signing step.

- [ ] **Step 2: Validate the YAML still parses**

Run:
```bash
ruby -ryaml -e "YAML.load_file('.github/workflows/release.yml'); puts 'yaml ok'"
```
Expected: `yaml ok`. (macOS system ruby 2.6 — `load_file`, not `unsafe_load_file`.)

- [ ] **Step 3: Commit**

```bash
git add .github/workflows/release.yml
git commit -m "fix(release): embed SysExt provisioning profile in systemextension bundle (#436)

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 3: Release-blocking verification step

**Files:**
- Modify: `.github/workflows/release.yml` — insert between "Sign app bundle (inside-out)" and "Notarize app bundle" (lines 395–425 on current main)

- [ ] **Step 1: Insert the verify step**

Between the "Sign app bundle (inside-out)" step (ends with `codesign --verify --deep --strict --verbose=2 "build/AgentSH.app"`) and the "Notarize app bundle" step, insert:

```yaml
      - name: Verify provisioning profiles
        run: scripts/verify-macos-bundle.sh build/AgentSH.app
```

Match the indentation of the sibling `- name:` steps (6 spaces). Failing before notarization means no Apple round-trip is wasted on a bundle AMFI would reject.

- [ ] **Step 2: Validate the YAML still parses**

Run:
```bash
ruby -ryaml -e "YAML.load_file('.github/workflows/release.yml'); puts 'yaml ok'"
```
Expected: `yaml ok`.

- [ ] **Step 3: Commit**

```bash
git add .github/workflows/release.yml
git commit -m "ci(release): gate macOS release on provisioning profile verification (#436)

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 4: Final verification against the spec

- [ ] **Step 1: Review the full diff**

Run: `git log --oneline origin/main..HEAD && git diff origin/main..HEAD --stat`

Expected: the spec and plan commits plus three implementation commits; changes only in `scripts/verify-macos-bundle.sh`, `.github/workflows/release.yml`, and `docs/superpowers/`.

- [ ] **Step 2: Confirm acceptance criteria coverage**

Check each against the spec's "Acceptance mapping" table:
1. Sysext profile embedded before signing — Task 2 `cp`, placed inside the assemble step. Confirm with `git diff origin/main..HEAD .github/workflows/release.yml` that the copy targets `.../ai.canyonroad.agentsh.SysExt.systemextension/Contents/embedded.provisionprofile` and precedes the signing step in the file.
2. Regression cannot ship silently — "Verify provisioning profiles" step exists between signing and notarization.
3. Script validated against the bug's shape — Task 1 steps 4–5 output matched expectations.

Criteria that can only be confirmed on the next real release (note them, don't block): the release job passes the verify step; on an affected machine `launchctl print system/WCKWMMKJ35.ai.canyonroad.agentsh.SysExt` shows the service running instead of `spawn scheduled`.

- [ ] **Step 3: Re-run the script guard checks once more (regression)**

```bash
/bin/bash -n scripts/verify-macos-bundle.sh && echo "syntax ok"
scripts/verify-macos-bundle.sh /tmp; echo "exit=$?"
```
Expected: `syntax ok`, then the not-an-app-bundle error with `exit=2`.
