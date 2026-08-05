# Daemon Install `--daemon` Flag Fix — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make `agentsh daemon install` produce a service unit that no longer fails at flag parsing, and repair installations already broken by the nonexistent `--daemon` flag.

**Architecture:** Two independent halves. Drop `--daemon` from the generated systemd and launchd templates so new installs are clean, and register `--daemon` on the `server` command as a hidden deprecated no-op so units already written to disk — which a binary upgrade does not rewrite — stop failing. A guard test renders each template, extracts the argv the service manager would exec, and dry-parses it against the real root command.

**Tech Stack:** Go 1.25, `github.com/spf13/cobra`, `github.com/spf13/pflag` v1.0.9, standard `testing`.

**Spec:** `docs/superpowers/specs/2026-08-05-daemon-install-flag-design.md`
**Issue:** [#437](https://github.com/canyonroad/agentsh/issues/437) (also audit finding H20)

## Global Constraints

- Per `CLAUDE.md` / `AGENTS.md`, before committing: `go test ./...` passes and `GOOS=windows go build ./...` succeeds.
- No hardcoded paths or OS-specific assumptions in test code — tests must run on all three platforms. Both tasks' tests are pure string/flag manipulation with no filesystem or service-manager access, so neither needs a `runtime.GOOS` guard.
- Do not edit `AUDIT-FINDINGS.md`; it is a point-in-time report with no status tracking.
- `pflag.MarkDeprecated` requires a non-empty message (it returns an error otherwise) and sets `Hidden = true` as a side effect — verified at `pflag@v1.0.9/flag.go:437-441`.

## File Structure

| File | Responsibility | Change |
| --- | --- | --- |
| `internal/cli/daemon.go` | Generates systemd/launchd unit files | Modify two template constants (lines 208, 362) |
| `internal/cli/server.go` | Defines the `server` command and its flags | Add the compatibility flag registration |
| `internal/cli/daemon_test.go` | Tests for daemon install/templates | Add guard test + argv helpers |

No new files. All three already exist and follow the package's established single-file-per-command-group pattern.

---

### Task 1: Stop generating `--daemon` in the unit templates

**Files:**
- Modify: `internal/cli/daemon.go:208` (`systemdServiceTemplate`), `internal/cli/daemon.go:362` (`launchdPlistTemplate`)
- Test: `internal/cli/daemon_test.go`

**Interfaces:**
- Consumes: `systemdServiceTemplate`, `launchdPlistTemplate` (package-level `const` strings, both `fmt.Sprintf` format strings); `NewRoot(version string) *cobra.Command` from `internal/cli/root.go:10`.
- Produces: test helpers `argvFromSystemdUnit(t *testing.T, unit string) []string` and `argvFromLaunchdPlist(t *testing.T, plist string) []string`, both returning argv **with `argv[0]` (the executable path) removed**. Task 2 does not use these.

**Format-string argument order** (needed to render the templates in the test — read off the two call sites at `daemon.go:266` and `daemon.go:426`):
- `systemdServiceTemplate`: `exePath`, `homeDir`, `uid`, `dataDir`
- `launchdPlistTemplate`: `exePath`, `logDir`, `logDir`, `homeDir`

- [ ] **Step 1: Write the failing test**

Add to `internal/cli/daemon_test.go`. The imports `fmt` and `regexp` are new — add them to the existing import block (`bytes`, `os`, `runtime`, `strings`, `testing`, `time` are already there).

```go
// launchdProgramArgsRE captures the body of the ProgramArguments <array>.
var launchdProgramArgsRE = regexp.MustCompile(`(?s)<key>ProgramArguments</key>\s*<array>(.*?)</array>`)

// plistStringRE captures individual <string> values.
var plistStringRE = regexp.MustCompile(`<string>(.*?)</string>`)

// argvFromSystemdUnit returns the arguments systemd would pass to the binary,
// with argv[0] (the executable path) removed.
func argvFromSystemdUnit(t *testing.T, unit string) []string {
	t.Helper()
	for _, line := range strings.Split(unit, "\n") {
		rest, ok := strings.CutPrefix(line, "ExecStart=")
		if !ok {
			continue
		}
		fields := strings.Fields(rest)
		if len(fields) == 0 {
			t.Fatal("ExecStart= line has no command")
		}
		return fields[1:]
	}
	t.Fatal("unit file has no ExecStart= line")
	return nil
}

// argvFromLaunchdPlist returns the arguments launchd would pass to the binary,
// with argv[0] (the executable path) removed.
func argvFromLaunchdPlist(t *testing.T, plist string) []string {
	t.Helper()
	block := launchdProgramArgsRE.FindStringSubmatch(plist)
	if block == nil {
		t.Fatal("plist has no ProgramArguments <array>")
	}
	var argv []string
	for _, m := range plistStringRE.FindAllStringSubmatch(block[1], -1) {
		argv = append(argv, m[1])
	}
	if len(argv) == 0 {
		t.Fatal("ProgramArguments <array> is empty")
	}
	return argv[1:]
}

// TestDaemonTemplates_GenerateRunnableCommand renders each unit template and
// dry-parses the resulting argv against the real root command. It never starts
// a server or binds a port. This guards the whole failure class behind issue
// #437: an unregistered flag, a renamed or removed `server` subcommand, and a
// structurally broken ExecStart / ProgramArguments all fail here.
func TestDaemonTemplates_GenerateRunnableCommand(t *testing.T) {
	const exePath = "/usr/local/bin/agentsh"

	systemdUnit := fmt.Sprintf(systemdServiceTemplate,
		exePath, "/home/testuser", "1000", "/home/testuser/.local/share/agentsh")
	launchdPlist := fmt.Sprintf(launchdPlistTemplate,
		exePath, "/home/testuser/Library/Logs/agentsh",
		"/home/testuser/Library/Logs/agentsh", "/home/testuser")

	for _, tc := range []struct {
		name string
		argv []string
	}{
		{"systemd", argvFromSystemdUnit(t, systemdUnit)},
		{"launchd", argvFromLaunchdPlist(t, launchdPlist)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if len(tc.argv) == 0 {
				t.Fatal("generated unit invokes the binary with no subcommand")
			}

			root := NewRoot("test")
			found, rest, err := root.Find(tc.argv)
			if err != nil {
				t.Fatalf("argv %q does not resolve to a command: %v", tc.argv, err)
			}
			// Find falls back to the root command when the subcommand does not
			// exist, so an explicit check is required to catch a rename.
			if found == root {
				t.Fatalf("argv %q does not resolve to a subcommand (was %q renamed or removed?)",
					tc.argv, tc.argv[0])
			}
			if err := found.ParseFlags(rest); err != nil {
				t.Fatalf("generated unit runs `%s %s`, which fails to parse: %v",
					found.Name(), strings.Join(rest, " "), err)
			}
		})
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/cli/ -run TestDaemonTemplates_GenerateRunnableCommand -v`

Expected: FAIL on both subtests with `generated unit runs `server --daemon`, which fails to parse: unknown flag: --daemon`.

If it fails on `t.Fatal("unit file has no ExecStart= line")` or `t.Fatal("plist has no ProgramArguments <array>")` instead, the helper regexes or the format-argument order are wrong — fix the helper, not the template.

- [ ] **Step 3: Remove the flag from both templates**

In `internal/cli/daemon.go`, `systemdServiceTemplate`:

```diff
-ExecStart=%s server --daemon
+ExecStart=%s server
```

In `internal/cli/daemon.go`, `launchdPlistTemplate`:

```diff
         <string>server</string>
-        <string>--daemon</string>
     </array>
```

Change nothing else in either template. In particular leave `Restart=on-failure`, `KeepAlive`, and the `ProtectSystem` / `ProtectHome` / `ReadWritePaths` hardening block exactly as they are — they are out of scope per the spec.

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./internal/cli/ -run TestDaemonTemplates_GenerateRunnableCommand -v`

Expected: PASS, both `systemd` and `launchd` subtests.

Then confirm nothing else in the package regressed — `TestSystemdServiceTemplate` and `TestLaunchdPlistTemplate` assert on these same constants:

Run: `go test ./internal/cli/`
Expected: `ok  github.com/agentsh/agentsh/internal/cli`

- [ ] **Step 5: Commit**

```bash
git add internal/cli/daemon.go internal/cli/daemon_test.go
git commit -m "fix(daemon): stop generating units that pass a nonexistent --daemon flag

The systemd and launchd templates both invoked `agentsh server --daemon`,
which the server command never registered. Cobra rejected it, the process
exited 1, and both service managers restart-looped it forever.

Guard test renders each template, extracts the argv the service manager
would exec, and dry-parses it against the real root command.

Refs #437"
```

---

### Task 2: Accept `--daemon` as a hidden deprecated no-op

Repairs installations already on disk. A user who ran `agentsh daemon install` before this fix has `--daemon` baked into their unit file, and upgrading the binary does not rewrite it — without this task they stay in the restart loop until they manually re-run `agentsh daemon install --force`.

**Files:**
- Modify: `internal/cli/server.go:39` (append after the existing `--config` registration in `newServerCmd`)
- Test: `internal/cli/daemon_test.go`

**Interfaces:**
- Consumes: `newServerCmd() *cobra.Command` from `internal/cli/server.go:11`.
- Produces: nothing consumed by other tasks.

- [ ] **Step 1: Write the failing test**

Add to `internal/cli/daemon_test.go`. No new imports.

```go
// TestServerCmd_AcceptsLegacyDaemonFlag pins the compatibility promise for unit
// files generated before the fix for issue #437, which hardcode `server
// --daemon`. Upgrading the binary does not rewrite those files, so the server
// must keep tolerating the flag even though it no longer emits it.
func TestServerCmd_AcceptsLegacyDaemonFlag(t *testing.T) {
	cmd := newServerCmd()

	if err := cmd.ParseFlags([]string{"--daemon"}); err != nil {
		t.Fatalf("server must tolerate the legacy --daemon flag: %v", err)
	}

	f := cmd.Flags().Lookup("daemon")
	if f == nil {
		t.Fatal("expected a --daemon flag to be registered")
	}
	if f.Deprecated == "" {
		t.Error("--daemon should be marked deprecated so it is not treated as supported")
	}
	if !f.Hidden {
		t.Error("--daemon is a compatibility no-op and should not appear in --help")
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/cli/ -run TestServerCmd_AcceptsLegacyDaemonFlag -v`

Expected: FAIL with `server must tolerate the legacy --daemon flag: unknown flag: --daemon`.

- [ ] **Step 3: Register the flag**

In `internal/cli/server.go`, in `newServerCmd`, directly after the existing `--config` line:

```go
	cmd.Flags().StringVar(&configPath, "config", "", "Path to server config YAML (default: ./config.yml, ./config.yaml, or /etc/agentsh/config.yaml)")

	// Accepted and ignored for compatibility with service units generated
	// before #437, which hardcode `agentsh server --daemon`. A binary upgrade
	// does not rewrite those files, and rejecting the flag leaves the daemon
	// restart-looping. There is no behavior to implement: systemd Type=simple
	// and launchd both require the supervised process to stay in the
	// foreground, so self-daemonizing would break process tracking either way.
	cmd.Flags().Bool("daemon", false, "Deprecated: accepted for compatibility, ignored")
	_ = cmd.Flags().MarkDeprecated("daemon",
		"the server always runs in the foreground under systemd/launchd; remove it or re-run `agentsh daemon install --force`")

	return cmd
```

Note there is no variable to bind — the value is never read, so `Bool` (which returns a discarded `*bool`) is used rather than `BoolVar`. `MarkDeprecated` sets `Hidden = true` itself, so no separate `MarkHidden` call is needed.

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./internal/cli/ -run TestServerCmd_AcceptsLegacyDaemonFlag -v`
Expected: PASS

Confirm the flag is genuinely hidden from help:

Run: `go run ./cmd/agentsh server --help`
Expected: usage text that does **not** list `--daemon`.

Confirm the original reported failure is gone — the command must get past flag parsing and fail (or succeed) on config loading instead:

Run: `go run ./cmd/agentsh server --daemon --config /nonexistent/config.yaml`
Expected: a deprecation notice on stderr mentioning `--daemon`, then a config-related error. Specifically **not** `unknown flag: --daemon`.

- [ ] **Step 5: Commit**

```bash
git add internal/cli/server.go internal/cli/daemon_test.go
git commit -m "fix(server): accept legacy --daemon as a hidden deprecated no-op

Units written by an earlier `agentsh daemon install` hardcode
`server --daemon`, and upgrading the binary does not rewrite them.
Accepting and ignoring the flag lets those installations recover without
the user re-running `agentsh daemon install --force`.

The deprecation notice pflag prints on each start lands in the daemon's
error log, telling an operator how to clean the unit up.

Fixes #437"
```

---

### Task 3: Full verification

**Files:** none modified — verification only.

- [ ] **Step 1: Run the full test suite**

Run: `go test ./...`
Expected: all packages `ok` or `no test files`.

Three tests are known to fail only in eran's local environment (long `TMPDIR`, `/workspace-policy` mount, symlink sandbox) and pass in CI — they are not regressions from this change. Anything failing in `internal/cli` **is** a regression and must be fixed before proceeding.

- [ ] **Step 2: Verify Windows cross-compilation**

Run: `GOOS=windows go build ./...`
Expected: no output (success).

Required by `CLAUDE.md`. `internal/cli/daemon.go` is built on all platforms — the template constants are not behind a build tag — so a syntax error there breaks the Windows build.

- [ ] **Step 3: Verify the generated unit end-to-end**

Confirm the rendered template no longer contains the flag:

Run: `go test ./internal/cli/ -run 'TestDaemonTemplates|TestServerCmd_AcceptsLegacy|TestSystemdServiceTemplate|TestLaunchdPlistTemplate' -v`
Expected: all PASS.

- [ ] **Step 4: Commit any fixes**

Only if steps 1-3 surfaced problems. Otherwise skip — the work is already committed by Tasks 1 and 2.

## Self-Review

**Spec coverage:**
- Spec §1 (register hidden deprecated no-op) → Task 2, Step 3.
- Spec §2 (why a no-op is correct) → captured as the code comment in Task 2, Step 3.
- Spec §3 (drop from both templates) → Task 1, Step 3.
- Spec §4 (regression guard: both helpers, `Find` + `ParseFlags`, plus the smaller compat test) → Task 1, Step 1 and Task 2, Step 1.
- Spec "Verification" (`go test ./...`, `GOOS=windows go build ./...`, `server --daemon` reaching config loading) → Task 3, and Task 2 Step 4.
- Spec "Out of scope" (`ProtectSystem=strict`, auto-repair, `AUDIT-FINDINGS.md`) → stated in Global Constraints and reinforced in Task 1, Step 3.

**Placeholder scan:** No TBDs. Every code step carries the literal code. Both test bodies are written out in full rather than cross-referenced, since tasks may be read out of order.

**Type consistency:** `argvFromSystemdUnit` / `argvFromLaunchdPlist` are defined once (Task 1) with the same signature used at their call sites in the same task. `NewRoot("test")` matches `NewRoot(version string)` at `root.go:10`. `newServerCmd()` matches `server.go:11`. Both helpers document the same argv[0]-removed contract that the `root.Find` call depends on.
