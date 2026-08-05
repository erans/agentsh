# Issue #436: Embed SysExt provisioning profile; verify profiles at release

**Date:** 2026-08-05
**Issue:** [#436](https://github.com/canyonroad/agentsh/issues/436) — macOS
system extension ships without `embedded.provisionprofile`; AMFI blocks it,
ESF/NE never start
**Approach:** Embed the existing distribution profile in the release
pipeline, plus a standalone verification script wired into the release
workflow so this class of failure can never ship silently again.

## Problem

The `.systemextension` bundle inside `AgentSH.app` is signed with the
restricted `com.apple.developer.endpoint-security.client` entitlement, but
the release pipeline never copies a provisioning profile into it. Restricted
entitlements require an embedded profile that grants them; without one, AMFI
refuses to exec the binary at runtime (`Error -413 "No matching profile
found"`), `launchd` respawns it until throttled, and all ESF/NE enforcement
is silently absent. `codesign --verify` and notarization both pass — neither
checks profile presence — so nothing in the pipeline caught it, and the bug
shipped in every release from at least v0.17.0 through v0.20.5.

The gap is visible in `.github/workflows/release.yml`: the "Assemble app
bundle" step embeds the app's profile
(`macos/AgentSH/AgentSH_Distribution.provisionprofile` →
`Contents/embedded.provisionprofile`) but nothing embeds
`macos/AgentSH/AgentSH_SysExt_Distribution.provisionprofile` into the sysext
bundle before the inside-out signing step.

The repo's SysExt profile is valid for the fix (verified by decoding it):
application identifier `WCKWMMKJ35.ai.canyonroad.agentsh.SysExt`, grants
`com.apple.developer.endpoint-security.client`, `ProvisionsAllDevices: true`
(Developer ID distribution), expires 2044.

## Decision summary

| Question | Decision |
|---|---|
| Fix | Copy the SysExt profile into the sysext bundle during assembly, before signing |
| Guardrail | New `scripts/verify-macos-bundle.sh`, run as a release step after signing; failure fails the release |
| `agentsh detect` false `esf ✓` | Out of scope — separate issue (detection trusts `systemextensionsctl list`, never checks the process runs) |
| `staple-macos.yml` | Unchanged; the script is manually runnable there if ever needed |

## Design

### 1. Embed fix (`.github/workflows/release.yml`)

In the "Assemble app bundle" step, immediately after the existing app-profile
copy, add:

```
cp macos/AgentSH/AgentSH_SysExt_Distribution.provisionprofile \
  "build/AgentSH.app/Contents/Library/SystemExtensions/ai.canyonroad.agentsh.SysExt.systemextension/Contents/embedded.provisionprofile"
```

Assembly runs before the "Sign app bundle (inside-out)" step, so the sysext
signature seals the profile into its `_CodeSignature` resource envelope. No
ordering changes.

### 2. Verification script (`scripts/verify-macos-bundle.sh`)

Standalone bash, macOS-native tools only (`security`, `codesign`, `plutil`,
`date`). Usage:

```
scripts/verify-macos-bundle.sh <path-to-AgentSH.app>
```

Runnable against a CI build (`build/AgentSH.app`), an installed
`/Applications/AgentSH.app`, or a mounted DMG — the same tool serves release
gating and field triage of user reports.

It checks two profile-bearing bundles: the app itself and
`Contents/Library/SystemExtensions/ai.canyonroad.agentsh.SysExt.systemextension`.
Per bundle:

1. **Profile present:** `Contents/embedded.provisionprofile` exists and
   decodes via `security cms -D`. Missing or undecodable → failure.
2. **Identity match:** the decoded profile's
   `Entitlements.com.apple.application-identifier` equals
   `<TeamIdentifier>.<CFBundleIdentifier from the bundle's Info.plist>`, and
   the profile's `TeamIdentifier` matches the code signature's
   `TeamIdentifier` (from `codesign -d`). Note: the profile's
   `TeamIdentifier` is a plist *array* — compare by membership (first
   element), not direct string equality.
3. **Distribution profile:** `ProvisionsAllDevices` is true — a
   device-limited development profile would pass on the build machine and
   fail everywhere else.
4. **Not expired:** `ExpirationDate` is in the future. Expired → failure;
   under 90 days remaining → warning only.
5. **Entitlement cross-check (the check that would have caught #436):**
   for each entitlement in a hardcoded restricted set —
   `com.apple.developer.endpoint-security.client`,
   `com.apple.developer.networking.networkextension`,
   `com.apple.developer.system-extension.install` — if the bundle's actual
   code signature (`codesign -d --entitlements`) claims it, the profile's
   `Entitlements` dict must grant it. A claimed-but-not-granted restricted
   entitlement is exactly the AMFI-rejection condition. "Grants" means the
   key is **present** in the profile's `Entitlements` dict — key-presence is
   the check, not value equality, because
   `com.apple.developer.networking.networkextension` is array-valued in both
   the signature and the profile (a `== true` test would false-fail on every
   correct bundle). Value-subset comparison is optional hardening, not
   required.

The restricted set is hardcoded because Apple publishes no machine-readable
classification of restricted entitlements; these three are the ones this
project uses. Adding a new restricted entitlement means adding it to the
list — cheap, and the failure mode of forgetting is a missing check, not a
false failure.

Output: a per-check pass/fail line per bundle, then a summary. The script
collects **all** failures before exiting non-zero (not fail-fast), so one CI
run shows the complete picture. Warnings (expiry window) never affect the
exit code.

Guards: refuses with a clear message when not on macOS (`uname`), when the
argument is missing or not a directory containing `Contents/Info.plist`, or
when a required tool is absent.

Tooling note for the implementer: decoded profiles contain plist `date`
values (`ExpirationDate`), which make `plutil -convert json` fail outright.
Extract individual fields with `plutil -extract <keypath> raw` or
`/usr/libexec/PlistBuddy` instead of converting the whole plist to JSON.

### 3. Workflow integration

New release.yml step **"Verify provisioning profiles"** between "Sign app
bundle (inside-out)" and "Notarize app bundle":

```
scripts/verify-macos-bundle.sh build/AgentSH.app
```

A failure fails the job before notarization — no Apple round-trip wasted on
a bundle AMFI would reject, and no broken DMG reaches the release.

### Error handling

| Failure | Behavior |
|---|---|
| Profile file missing | Check failure, listed in summary, exit non-zero |
| Profile present but `security cms -D` fails | Failure (corrupt profile is not a skip) |
| Bundle unsigned / `codesign -d` fails | Failure |
| Info.plist missing or unreadable | Failure |
| Profile expired | Failure |
| Profile expires within 90 days | Warning only (exit unaffected) |
| Run on non-macOS | Immediate error, distinct exit message |

The script never warns-and-passes on any condition that gates AMFI
acceptance.

### Non-goals

- Fixing `agentsh detect`'s false `esf ✓` (trusts `systemextensionsctl
  list`, never verifies the extension process runs) — real, but a separate
  issue against `internal/capabilities/detect_darwin.go`.
- Verifying notarization/stapling state — `stapler validate` already exists
  for that.
- Signing-manifest refactor of the assemble/sign steps — rejected as
  overkill for a four-component bundle.
- Verifying `xpc.xpc` or `approval-dialog.app` profiles — they carry no
  restricted entitlements and need no profile.

## Testing

1. **Detector validates against the real bug:** run the script against an
   installed v0.20.5 `/Applications/AgentSH.app` — must fail with the
   sysext missing-profile error (and the entitlement cross-check must flag
   `endpoint-security.client` as claimed-but-not-granted).
2. **Fixed bundle passes:** a locally assembled bundle with both profiles
   embedded (or the next release run) must pass all checks.
3. **Negative paths:** missing argument, non-bundle path, non-macOS guard —
   each exits non-zero with its distinct message.
4. **No CI test harness for the script itself:** the sysext is only built in
   the release workflow; a fabricated fixture bundle would test the mock,
   not the release. The release-blocking step is the ongoing test.

## Acceptance mapping

| Acceptance criterion | How satisfied |
|---|---|
| Sysext bundle in the shipped DMG contains `embedded.provisionprofile` granting the ES entitlement | Assembly-step copy, sealed by the existing signing step; verify step gates the release |
| On an affected machine after upgrade, the extension actually runs | AMFI accepts the profiled binary: `launchctl print system/WCKWMMKJ35.ai.canyonroad.agentsh.SysExt` shows the service running, not `spawn scheduled` / `OS_REASON_EXEC` |
| Nested execs are policed again; `sh -c` no longer hits `shellc-opaque-script` pre-deny | ESF exec interception active once the extension runs |
| Regression cannot ship silently | "Verify provisioning profiles" release step fails the job on any missing/mismatched profile |
