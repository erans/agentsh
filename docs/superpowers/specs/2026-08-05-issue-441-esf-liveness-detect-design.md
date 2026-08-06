# detect: require sysext liveness before reporting esf (issue #441)

**Date:** 2026-08-05
**Issue:** [#441](https://github.com/canyonroad/agentsh/issues/441)
**Prior art:** #388/#390/#392 (Linux seccomp detect honesty), #436/#440 (AMFI-blocked sysext)

## Problem

`agentsh detect` on macOS reports `esf ✓` (and selects the `esf` security mode,
protection score 90) based solely on `systemextensionsctl list` containing the
bundle ID and `activated enabled`. It never verifies that the extension
*process is running*, so any activated-but-not-running condition (AMFI
rejection, provisioning-profile expiry, cert revocation, launchd throttling)
reports a healthy sandbox while enforcement is entirely absent. Issue #436 was
exactly this: every release v0.17.0–v0.20.5 shipped a sysext AMFI refused to
exec; `detect` said `esf ✓` throughout.

The same activated-equals-working assumption exists in three places:

| Site | Consumes it for |
|---|---|
| `internal/capabilities/detect_darwin.go:checkSysExtInstalled` | `caps["esf"]`, mode selection, protection score |
| `internal/platform/darwin/permissions.go:CheckSysExtInstalled` | `HasSystemExtension` → tier computation (`TierEnterprise`) |
| `internal/platform/darwin/sysext.go:SysExtManager.Status` | `SysExtStatus.Running` (a field that currently lies) |

Verified live during design: a machine with the sysext `[activated enabled]`
but `launchctl print` showing `state = spawn scheduled`, `last exit code = 1`,
and no SysExt process — `detect` reports `esf ✓` there today.

## Decisions made during brainstorming

1. **Scope:** one shared liveness helper in `internal/platform/darwin`; all
   three call sites collapse onto it.
2. **Ambiguity policy: fail closed.** `esf` is reported available only on
   positive proof of liveness (`state = running`). Probe failure (launchctl
   missing, label not found, unparseable output) → not running, with a Detail
   explaining that liveness could not be verified. Consistent with the #390
   precedent: never claim protection you cannot prove.
3. **Depth: launchd liveness only.** The extension is a *client* that dials
   into the agentsh server's policy socket, so only a running server can know
   whether the extension is functionally connected. Server-side connection
   reporting is out of scope; a follow-up issue will be filed.
4. **Probe mechanism: parse `launchctl print system/<TeamID>.<bundleID>`**
   (approach A). Unprivileged (verified), and yields the diagnostics the
   issue asks for (`state`, `last exit code`, `last exit reason`). Process-
   enumeration probes were rejected: non-root callers cannot reliably read
   root-owned process paths/args on macOS, and they yield no diagnostics.

## Design

### Shared helper: `internal/platform/darwin/sysext_liveness.go`

```go
// SysExtLiveness reports both facts about the system extension separately:
// whether it is activated (systemextensionsctl) and whether its process is
// actually running (launchd service state).
type SysExtLiveness struct {
    Activated bool   // systemextensionsctl list: row for our bundle ID with "activated enabled"
    Running   bool   // launchctl print: state = running (positive proof only)
    State     string // raw launchd state ("running", "spawn scheduled", ""); "" when unknown
    LastExit  string // "exit code 1", "OS_REASON_EXEC: ...", ""; from launchctl output
    Detail    string // human-readable one-liner for display and tip matching
}

func CheckSysExtLiveness() SysExtLiveness
```

Internally: two pure parsing functions plus a thin, injectable exec layer
(package-level `runCommand` func var swapped in tests).

1. `parseSysExtList(output string) (activated bool, teamID string)` — scans
   `systemextensionsctl list` **per line**. Only lines containing the exact
   bundle-ID token `ai.canyonroad.agentsh.SysExt` count; the team ID is the
   whitespace-separated token immediately preceding the **first** occurrence
   of the bundle-ID token (the real row repeats the bundle ID as the display
   name — `… WCKWMMKJ35 ai.canyonroad.agentsh.SysExt (1.0/14)
   ai.canyonroad.agentsh.SysExt [activated enabled]` — so matching the last
   occurrence would yield `(1.0/14)` as the team ID). Any
   matching row containing `activated enabled` ⇒ activated (upgrade
   transients can produce multiple rows). Per-line matching also prevents a
   *different* extension's `activated enabled` (e.g. the co-installed
   `ai.canyonroad.beacon.sysext`) from satisfying a whole-output substring
   grep, which is what all three copies do today.
2. `parseLaunchdState(output string) (state, lastExit string)` — over
   `launchctl print system/<teamID>.ai.canyonroad.agentsh.SysExt` output,
   takes the **first** `state = …` line (nested sub-sections contain their
   own `state = active` lines) and, when present, `last exit code = …` /
   `last exit reason = …` (reason preferred when both exist).

Decision table (fail closed):

| systemextensionsctl | launchctl | Result |
|---|---|---|
| not activated / command fails | skipped | `Activated: false, Running: false`; Detail "not activated" |
| activated | `state = running` | `Running: true`; Detail "running" |
| activated | any other state | `Running: false`; Detail `activated but not running (state: <s>, last exit: <e>)` |
| activated | command fails / label missing / no `state =` line | `Running: false`; Detail `activated but liveness could not be verified (<cause>)` |

Both exec calls use `exec.CommandContext` with a 5-second timeout; timeout ⇒
probe failure ⇒ fail closed. No hardcoded team ID anywhere (dev-signed builds
keep working).

Non-darwin builds: the helper is `//go:build darwin`; no new stubs are needed
because all consumers are darwin-only files.

### Consumer changes

**`internal/capabilities/detect_darwin.go`**
- `checkSysExtInstalled()` is deleted; `Detect()` calls
  `darwin.CheckSysExtLiveness()` once (import verified acyclic).
- `caps["esf"] = liveness.Running` — mode selection (`selectDarwinMode`) and
  scoring become honest with no logic changes.
- `caps["esf_activated"] = liveness.Activated` — JSON/YAML output preserves
  the installed-but-broken distinction.
- The two esf backend entries in `buildDarwinDomains` (File Protection,
  Command Control) carry `Detail: liveness.Detail` instead of `""`.

**`internal/capabilities/tips.go`** — `tipsByBackend["esf"]` becomes
reason-sensitive (first match wins, ordered):
1. `Contains: "OS_REASON_EXEC"` → binary rejected at exec — likely
   AMFI/code-signing; verify `embedded.provisionprofile` in the sysext bundle
   (#436), then reinstall.
2. `Contains: "not running"` → activated but not running; inspect
   `launchctl print system/<TeamID>.ai.canyonroad.agentsh.SysExt` for state
   and last exit reason.
3. `Contains: "could not be verified"` → liveness unverifiable; check the
   sysext service manually with launchctl.
4. Fallback (unchanged): install the agentsh macOS app bundle.

Note: the legacy `darwinTips` esf entry consumed by `GenerateTips` keeps the
old "install the app bundle" wording. `GenerateTips` has no non-test callers
(production uses `GenerateTipsFromDomains` only); update its esf `Impact`/
`Action` for consistency rather than leaving stale text behind.

**`internal/platform/darwin/permissions.go`**
- `HasSystemExtension = CheckSysExtLiveness().Running` — `TierEnterprise` now
  requires a *working* extension.
- The `MissingPermissions` entry branches: activated-but-not-running gets
  `HowToEnable` text pointing at the launchctl diagnostics (embedding the
  Detail) instead of the misleading "install the app bundle".
- The local `CheckSysExtInstalled()` is removed in favor of the shared helper.

**`internal/platform/darwin/sysext.go`**
- `Status()` uses the shared helper: `Installed = Activated`, `Running` is
  real liveness. `SysExtStatus` gains `State` and `LastExit` fields
  (`json:",omitempty"`), mirrored in the `//go:build !darwin` copy in
  `sysext_stub.go` to keep struct parity. The stale "Delegates to the darwin
  package" comment in detect_darwin.go disappears along with the duplicated
  code.

## Testing

- **Pure parser tests** (table-driven, real captured fixtures):
  - current live machine output: activated + `spawn scheduled` +
    `last exit code = 1` (the false-positive specimen)
  - synthesized `OS_REASON_EXEC` variant matching the #436 report
  - `state = running` (healthy)
  - extension absent; only `ai.canyonroad.beacon.sysext` present
    (neighbor-collision regression test)
  - garbage / empty output; missing `state =` line
  - nested `state = active` lines after the service-level state (first-match)
- **Decision-table tests** for `CheckSysExtLiveness` with the injected runner,
  asserting fail-closed on every probe-failure branch (exec error, timeout,
  label missing, unparseable).
- **tips_test.go**: new esf reason entries and their precedence
  (`OS_REASON_EXEC` must match before `not running`).
- **detect_darwin_test.go**: domains carry the liveness Detail;
  `esf_activated` present in caps.
- **Cross-compile gate:** `GOOS=windows go build ./...` (CLAUDE.md).
- **Live acceptance** (manual, on the design machine): `agentsh detect` flips
  from `esf ✓ / 90` to `esf ✗` with the launchctl tip and mode falls back to
  `lima`; after the sysext is repaired (`state = running`), `esf ✓` returns.

## Execution amendments

Deltas from this spec adopted during implementation code review (committed code
is authoritative):

1. `SysExtLiveness` gained a `ProbeFailed bool` field — set on every
   probe-failure path (command error, missing team ID, unparseable state) so
   consumers can distinguish "cleanly not activated" from "couldn't verify"
   without matching Detail string literals.
2. When `systemextensionsctl` itself fails, Detail is
   `not activated (liveness could not be verified: systemextensionsctl failed: …)`
   — carrying the "could not be verified" token so the reason-sensitive tips
   route to the diagnostics tip instead of the misleading install tip.
3. `runLivenessCommand` folds captured stderr (whitespace-collapsed to a
   single line) into returned errors, and reports timeouts as
   `timed out after 5s` instead of `signal: killed` — Detail stays a
   one-liner while carrying the tool's actual message.
4. The no-state-line Detail appends `(last exit: …)` when launchctl output
   carried an exit reason despite lacking a parseable state, so an AMFI
   reason still reaches the tips layer on that path.

## Out of scope / follow-ups

- **Server-side functional check:** report/warn when the server runs in esf
  mode but the extension never connects to the policy socket (the true
  end-to-end signal). To be filed as a separate issue.
- Windows/Linux detect surfaces are untouched.
