# Design: `daemon install --force` must reload the running service (#439)

**Date:** 2026-08-05
**Issue:** [#439](https://github.com/canyonroad/agentsh/issues/439)
**Status:** Approved

## Problem

`agentsh daemon install --force` on macOS writes the new launchd plist but never
reloads it. `launchctl load` fails with "service already loaded" — which is the
normal case under `--force`, since an installation already exists — and the code
downgrades the failure to a `Warning:` line and returns success. launchd keeps
running the old job definition until a manual `agentsh daemon restart` or
re-login. The #437 deprecation notice tells users to run exactly this command,
so the documented remediation appears not to work.

Linux has a milder variant: `installSystemdService` runs `daemon-reload` after
rewriting the unit, but never restarts an already-running service, so the old
`ExecStart` invocation keeps running until the next restart or login.

Both platforms share a second defect: a failed load is reported as a warning and
the command exits 0, so an install that leaves the daemon not running (or
running stale) claims success. That masking is what let #437 go unnoticed.

## Scope

- Fix the macOS launchd install path (unload before load).
- Linux parity: restart the systemd service on install when it is already
  running.
- Promote load/restart failures from warnings to hard errors.
- Add regression tests for the command sequences and error propagation.

Out of scope:

- Migrating from `launchctl load`/`unload` to `bootstrap`/`bootout`. The file
  uses `load`/`unload` consistently (install, restart, uninstall); modernizing
  is a separate change.
- Promoting `daemon-reload` / `enable` failures on Linux to hard errors.
  Considered and deferred: those failures do not leave a *stale* service
  running, and the unit still takes effect on next login. Revisit separately.
- Windows service support (unchanged: unsupported message).

## Design

### Shared helpers in `internal/cli/daemon.go`

```go
// reloadLaunchdService replaces whatever job definition launchd currently
// holds with the plist on disk. The unload error is ignored: not-loaded is
// the expected case on a fresh install.
func reloadLaunchdService(plistPath string) error {
    _ = exec.Command("launchctl", "unload", plistPath).Run()
    out, err := exec.Command("launchctl", "load", plistPath).CombinedOutput()
    // on failure, include launchctl's diagnostic output (it goes to stderr,
    // so a bare exit status would be unactionable) in the returned error
    ...
}

// restartSystemdIfActive restarts the agentsh user unit only when it is
// currently active, so install keeps its existing behavior of never being
// the thing that first starts the daemon on Linux. The bool reports whether
// a restart occurred, so the caller can print the success message only in
// that case.
func restartSystemdIfActive() (restarted bool, err error)
```

`restartSystemdIfActive` checks `systemctl --user is-active agentsh` using
`exec.Command(...).Output()` (as `getCurrentSession` already does), **not**
`runSystemctl`, which wires stdout/stderr to the terminal and would leak
`inactive` to the user. Only when the output is `active` does it run
`systemctl --user restart agentsh` (via `runSystemctl`).

### Call-site changes

- `installLaunchdService`: replace the bare `launchctl load` with
  `reloadLaunchdService(plistPath)`. On error, return a hard error whose
  message states that the plist **was** written and gives the manual
  remediation (`launchctl load <path>`). On success, keep printing
  `Service loaded and started`.
- `newDaemonRestartCmd` (darwin branch): call `reloadLaunchdService` instead of
  its inline unload/load pair. Behavior is unchanged; install and restart can
  no longer drift apart — drift between those two paths is what caused this
  bug.
- `installSystemdService`: after `daemon-reload` and `enable`, call
  `restartSystemdIfActive()`. On error, return a hard error stating the unit
  file was written and suggesting `systemctl --user restart agentsh`. When a
  restart occurs, print `Service restarted with updated configuration`. When
  the unit is not active, behavior is unchanged, including the
  "To start the daemon now" hint.

### Home-directory consistency (both install paths)

`installLaunchdService` derives the LaunchAgents dir, log dir, and the plist's
`HOME` environment variable from `user.Current().HomeDir`, while
`getLaunchdPlistPath` uses `os.UserHomeDir()`. Under cgo, `user.Current()`
ignores `$HOME`, so the two can disagree — and the mismatch blocks HOME-based
test redirection. Unify on `os.UserHomeDir()`; `user.Current()` drops out of
`installLaunchdService` entirely.

`installSystemdService` has the same problem: the systemd unit dir, data dir,
and the unit's `Environment=HOME=` value come from `user.Current().HomeDir`.
Switch those to `os.UserHomeDir()` as well, for the same reason — without it,
the Linux test cases below would write to the developer's real
`~/.config/systemd/user/agentsh.service`. `user.Current()` remains in that
function solely for `Uid` (the `XDG_RUNTIME_DIR=/run/user/%s` template value),
which has no filesystem effect.

Use the existing `userHomeDir()` helper (`internal/cli/root.go:99`) rather than
calling `os.UserHomeDir()` directly — it already handles the error return by
falling back to `$HOME`.

## Error handling

| Failure | Today | After |
| --- | --- | --- |
| `launchctl load` fails on install (macOS) | `Warning:`, exit 0 | Hard error, exit non-zero; message includes plist path + manual load command |
| `systemctl restart` fails on install with active unit (Linux) | n/a (never attempted) | Hard error, exit non-zero; message includes manual restart command |
| `launchctl unload` fails on install/restart | n/a / ignored | Ignored (not-loaded is the expected fresh-install case) |
| `daemon-reload` / `enable` fail (Linux) | `Warning:`, exit 0 | Unchanged (deferred) |
| `is-active` query fails (Linux) | n/a | Treated as not-active; restart skipped (a broken systemctl already produced warnings above) |

## Testing

New tests in `internal/cli/daemon_install_test.go` (a new file, keeping the
existing 385-line `daemon_test.go` focused), in the codebase's existing style
(shell-script helpers, as `auto_daemon_test.go` already uses):

- Fake `launchctl` and `systemctl` shell scripts in a `t.TempDir()` prepended
  to `PATH`, appending their arguments to a calls file.
- `t.Setenv("HOME", t.TempDir())` so plist/unit/data/log paths land in the
  sandbox (enabled by the `os.UserHomeDir()` unification in **both** install
  functions).
- The install functions are not GOOS-gated at compile time, so these tests run
  on both macOS and Linux CI. Skip on Windows (shell scripts).

Cases:

1. macOS fresh install: calls are `unload <plist>` then `load <plist>`, in
   that order; command succeeds.
2. macOS `--force` over an existing plist: same ordering assertion.
3. macOS load failure (fake exits non-zero for `load`): install returns an
   error mentioning the plist path.
4. Linux `--force` with `is-active` reporting `active`: `restart agentsh` is
   invoked after `daemon-reload`/`enable`.
5. Linux `--force` with `is-active` reporting `inactive`: no `restart` call;
   command succeeds.
6. Linux restart failure with active unit: install returns an error.

Manual verification on macOS before the PR merges, per the issue: install,
mutate the plist, `install --force`, confirm `launchctl list` reflects the new
definition without a manual restart.

## Compatibility

- Fresh installs: identical observable behavior on both platforms (the added
  `unload` is a silent no-op).
- macOS `--force`: the running daemon is now bounced to the new definition —
  the behavior users already expect from the command.
- Linux `--force` with a running daemon: the service restarts. This is a
  deliberate behavior change and the point of the parity fix; a monitoring
  daemon restart is momentary and `Restart=on-failure` semantics are
  unaffected.
- Scripts that relied on `install` exiting 0 despite a load failure will now
  see a non-zero exit. That is the intended contract fix.
