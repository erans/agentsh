# Daemon install: generated units invoke a nonexistent `--daemon` flag

**Issue:** [#437](https://github.com/canyonroad/agentsh/issues/437)
**Also tracked as:** audit finding H20 (`AUDIT-FINDINGS.md`)
**Date:** 2026-08-05

## Problem

`agentsh daemon install` writes a service unit that starts the server with a
`--daemon` argument:

- `internal/cli/daemon.go:208` — `ExecStart=%s server --daemon`
- `internal/cli/daemon.go:362` — `<string>--daemon</string>`

`internal/cli/server.go` registers only `--config`, so cobra rejects the
argument and the process exits non-zero before doing any work:

```
$ agentsh server --daemon
unknown flag: --daemon
```

Both service managers then restart it forever — systemd via `Restart=on-failure`,
launchd via `KeepAlive`. The reporter's `agentsh.err` is three identical
`unknown flag: --daemon` lines and `launchctl list` shows `last exit 1`.

`agentsh daemon install` is broken end-to-end on both Linux and macOS.

## Constraint that shapes the fix

Correcting the templates does not repair an existing installation. The unit
file already on disk still carries `--daemon`, and upgrading the binary does not
rewrite it. Every user who has already run `agentsh daemon install` stays in the
restart loop until they manually re-run `agentsh daemon install --force`.

The fix therefore has to work from both directions: stop emitting the argument,
and tolerate it where it has already been written.

## Design

### 1. Register `--daemon` as a hidden, deprecated no-op

In `newServerCmd` (`internal/cli/server.go`):

```go
cmd.Flags().Bool("daemon", false, "Deprecated: accepted for compatibility, ignored")
_ = cmd.Flags().MarkDeprecated("daemon",
    "the server always runs in the foreground under systemd/launchd; remove it or re-run `agentsh daemon install --force`")
```

A no-op is the semantically correct shim, not a placeholder for unimplemented
behavior. systemd `Type=simple` and launchd both require the supervised process
to stay in the foreground — self-daemonizing would break process tracking under
either. "Run as a daemon" has no work to do in this context, so there is nothing
being stubbed out.

`pflag.MarkDeprecated` also sets `Hidden`, keeping the flag out of `--help`, and
prints a one-line notice to stderr on each start. Under launchd that notice
lands in `~/Library/Logs/agentsh/agentsh.err`, where it tells an operator
exactly how to clean up a stale unit.

### 2. Drop `--daemon` from both templates

```diff
-ExecStart=%s server --daemon
+ExecStart=%s server
```

```diff
         <string>server</string>
-        <string>--daemon</string>
     </array>
```

Newly generated units are clean; the flag survives only as a compatibility
accommodation for units already on disk.

### 3. Regression guard

In `internal/cli/daemon_test.go`, render each template with realistic values,
extract the argv the service manager would execute, and dry-parse it against the
real root command. No server starts and no port is bound.

- `argvFromSystemdUnit` — take the `ExecStart=` line, split on whitespace, drop
  `argv[0]` (the resolved executable path).
- `argvFromLaunchdPlist` — take the `<string>` values inside the
  `ProgramArguments` `<array>`, drop `argv[0]`.
- For each: `NewRoot("test").Find(argv)` must resolve the subcommand, and
  `ParseFlags` on the found command must succeed.

This covers the whole failure class rather than this one instance — an
unregistered flag, a renamed or removed `server` subcommand, and a structurally
broken `ExecStart` / `ProgramArguments` all fail the test.

A second, smaller test asserts `server --daemon` still parses and that the flag
is hidden, pinning the compatibility promise from section 1 so a future cleanup
cannot silently drop it.

## Verification

- `go test ./internal/cli/...` and full `go test ./...`
- `GOOS=windows go build ./...` (per `AGENTS.md`)
- `go run ./cmd/agentsh server --daemon` reaches config loading rather than
  failing at flag parsing

## Out of scope

Examined and deliberately left alone:

- **`ProtectSystem=strict` in the systemd unit** makes `/run` read-only, which
  can prevent the unix socket listener from binding. The server already degrades
  gracefully here, logging `unix socket disabled` and continuing, so it does not
  block startup.
- **Auto-repairing stale unit files** on `agentsh daemon install`. Unnecessary
  once the no-op flag exists — stale units simply work.
- **`AUDIT-FINDINGS.md`** is a point-in-time report with no status tracking, so
  the H20 entry is not edited.
