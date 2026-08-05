# `daemon install --force` Service Reload Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make `agentsh daemon install --force` actually apply the new service definition — unload/reload on macOS, restart-if-active on Linux — and fail loudly instead of warning when the service can't be (re)loaded. Fixes [#439](https://github.com/canyonroad/agentsh/issues/439).

**Architecture:** All changes live in `internal/cli/daemon.go`: two new helpers (`reloadLaunchdService`, `restartSystemdIfActive`) shared by the install/restart call sites, plus unifying home-dir resolution on the existing `userHomeDir()` helper so tests can sandbox with `$HOME`. Tests use fake `launchctl`/`systemctl` shell scripts on a prepended `PATH` (house style — see `internal/cli/auto_daemon_test.go`).

**Tech Stack:** Go, cobra, stdlib testing. No new dependencies.

**Spec:** `docs/superpowers/specs/2026-08-05-daemon-install-reload-design.md` — read it before starting.

**Branch:** work happens on `fix/439-daemon-install-reload` (already exists, contains the spec).

---

## File Structure

- Modify: `internal/cli/daemon.go` — helpers + call-site changes (only file with production changes)
- Create: `internal/cli/daemon_install_test.go` — fake-tool test helpers + all new tests (kept out of the already-385-line `daemon_test.go`)

---

### Task 1: Unify home-directory resolution on `userHomeDir()`

**Why no failing test first:** running the install functions under a redirected `$HOME` *before* this change writes real files into the developer's `~/Library/LaunchAgents` / `~/.config/systemd/user` (with cgo, `user.Current()` ignores `$HOME`). The spec reviewer flagged exactly this hazard. This task is a mechanical refactor with no behavior change on normal systems; every test added in Tasks 2–3 exercises it (they all assert files land inside the sandbox `$HOME`).

**Files:**
- Modify: `internal/cli/daemon.go` (functions `installSystemdService`, `uninstallSystemdService`, `installLaunchdService`)

- [ ] **Step 1: Switch `installSystemdService` to `userHomeDir()`**

Keep the `user.Current()` block — `currentUser.Uid` is still needed for the `XDG_RUNTIME_DIR=/run/user/%s` template value. Add `home := userHomeDir()` right after it, then replace the three `currentUser.HomeDir` uses:

```go
	// Get user info
	currentUser, err := user.Current()
	if err != nil {
		return fmt.Errorf("get current user: %w", err)
	}
	home := userHomeDir()
```

```go
	systemdDir := filepath.Join(home, ".config", "systemd", "user")
```

```go
	dataDir := filepath.Join(home, ".local", "share", "agentsh")
```

```go
	serviceContent := fmt.Sprintf(systemdServiceTemplate,
		exePath,
		home,
		currentUser.Uid,
		dataDir,
	)
```

- [ ] **Step 2: Switch `uninstallSystemdService` to `userHomeDir()`**

Install now derives the unit path from `userHomeDir()`; uninstall must resolve the identical path or it could miss the file. The `user.Current()` call drops out of this function entirely:

```go
func uninstallSystemdService(cmd *cobra.Command) error {
	w := cmd.OutOrStdout()

	servicePath := filepath.Join(userHomeDir(), ".config", "systemd", "user", "agentsh.service")
```

(The rest of the function is unchanged.)

- [ ] **Step 3: Switch `installLaunchdService` to `userHomeDir()`**

`user.Current()` drops out of this function entirely (it was only used for `HomeDir`). This also makes the LaunchAgents dir agree with `getLaunchdPlistPath()`, which already uses `os.UserHomeDir()`:

```go
func installLaunchdService(cmd *cobra.Command, force bool) error {
	w := cmd.OutOrStdout()

	home := userHomeDir()

	exePath, err := os.Executable()
```

```go
	launchAgentsDir := filepath.Join(home, "Library", "LaunchAgents")
```

```go
	logDir := filepath.Join(home, "Library", "Logs", "agentsh")
```

```go
	plistContent := fmt.Sprintf(launchdPlistTemplate,
		exePath,
		logDir,
		logDir,
		home,
	)
```

- [ ] **Step 4: Verify build and existing tests**

Run: `go build ./... && go test ./internal/cli/`
Expected: build succeeds, all existing tests PASS (`os/user` must still be imported — `installSystemdService` and `getCurrentSession` use it).

- [ ] **Step 5: Commit**

```bash
git add internal/cli/daemon.go
git commit -m "refactor(daemon): resolve service paths via userHomeDir() consistently

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 2: macOS — reload the service on install, hard-error on load failure

**Files:**
- Modify: `internal/cli/daemon.go` (add `reloadLaunchdService`; change `installLaunchdService` load block and `newDaemonRestartCmd` darwin branch)
- Create: `internal/cli/daemon_install_test.go`

- [ ] **Step 1: Create the test file with fake-tool helpers and the macOS tests**

Create `internal/cli/daemon_install_test.go`:

```go
package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// setupFakeTools installs fake launchctl/systemctl executables on PATH that
// append each invocation to a calls file, and redirects HOME to a temp dir so
// service files land in the test sandbox. The body script runs after the call
// is recorded and controls the fake's behavior; an empty body means exit 0.
func setupFakeTools(t *testing.T, launchctlBody, systemctlBody string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake-tool tests require /bin/sh")
	}
	binDir := t.TempDir()
	callsFile := filepath.Join(t.TempDir(), "calls")
	t.Setenv("AGENTSH_TEST_CALLS", callsFile)
	writeTool := func(name, body string) {
		script := "#!/bin/sh\n" +
			"echo \"$(basename \"$0\") $@\" >> \"$AGENTSH_TEST_CALLS\"\n" +
			body + "\nexit 0\n"
		if err := os.WriteFile(filepath.Join(binDir, name), []byte(script), 0o755); err != nil {
			t.Fatalf("write fake %s: %v", name, err)
		}
	}
	writeTool("launchctl", launchctlBody)
	writeTool("systemctl", systemctlBody)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("HOME", t.TempDir())
	return callsFile
}

// recordedCalls returns the fake-tool invocations, one per line, or nil if no
// tool was ever called.
func recordedCalls(t *testing.T, callsFile string) []string {
	t.Helper()
	data, err := os.ReadFile(callsFile)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("read calls file: %v", err)
	}
	return strings.Split(strings.TrimSpace(string(data)), "\n")
}

func newTestCmd() (*cobra.Command, *bytes.Buffer) {
	cmd := &cobra.Command{}
	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	return cmd, buf
}

func assertCalls(t *testing.T, got, want []string) {
	t.Helper()
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("recorded calls:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestInstallLaunchd_UnloadsBeforeLoad(t *testing.T) {
	callsFile := setupFakeTools(t, "", "")
	cmd, buf := newTestCmd()

	if err := installLaunchdService(cmd, false); err != nil {
		t.Fatalf("install failed: %v", err)
	}

	plistPath := getLaunchdPlistPath()
	assertCalls(t, recordedCalls(t, callsFile), []string{
		"launchctl unload " + plistPath,
		"launchctl load " + plistPath,
	})
	if _, err := os.Stat(plistPath); err != nil {
		t.Errorf("plist not written inside sandbox HOME: %v", err)
	}
	if !strings.Contains(buf.String(), "Service loaded and started") {
		t.Errorf("missing success message, got: %s", buf.String())
	}
}

func TestInstallLaunchd_ForceReplacesLoadedDefinition(t *testing.T) {
	callsFile := setupFakeTools(t, "", "")
	plistPath := getLaunchdPlistPath()
	if err := os.MkdirAll(filepath.Dir(plistPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(plistPath, []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd, _ := newTestCmd()
	if err := installLaunchdService(cmd, true); err != nil {
		t.Fatalf("install --force failed: %v", err)
	}

	assertCalls(t, recordedCalls(t, callsFile), []string{
		"launchctl unload " + plistPath,
		"launchctl load " + plistPath,
	})
	content, err := os.ReadFile(plistPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), "ai.canyonroad.agentsh.daemon") {
		t.Errorf("plist not rewritten, still: %s", content)
	}
}

func TestInstallLaunchd_ExistingPlistWithoutForce(t *testing.T) {
	callsFile := setupFakeTools(t, "", "")
	plistPath := getLaunchdPlistPath()
	if err := os.MkdirAll(filepath.Dir(plistPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(plistPath, []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd, buf := newTestCmd()
	if err := installLaunchdService(cmd, false); err != nil {
		t.Fatalf("install failed: %v", err)
	}

	if calls := recordedCalls(t, callsFile); calls != nil {
		t.Errorf("expected no launchctl calls without --force, got %v", calls)
	}
	if !strings.Contains(buf.String(), "Use --force to overwrite") {
		t.Errorf("missing --force hint, got: %s", buf.String())
	}
}

func TestInstallLaunchd_LoadFailureIsError(t *testing.T) {
	setupFakeTools(t, `[ "$1" = "load" ] && exit 1`, "")
	cmd, _ := newTestCmd()

	err := installLaunchdService(cmd, false)
	if err == nil {
		t.Fatal("expected error when launchctl load fails")
	}
	if !strings.Contains(err.Error(), getLaunchdPlistPath()) {
		t.Errorf("error should mention plist path: %v", err)
	}
}
```

- [ ] **Step 2: Run the new tests to verify they fail**

Run: `go test ./internal/cli/ -run 'TestInstallLaunchd' -v`
Expected: `TestInstallLaunchd_UnloadsBeforeLoad` and `TestInstallLaunchd_ForceReplacesLoadedDefinition` FAIL (recorded calls contain only `load`, no `unload`). `TestInstallLaunchd_LoadFailureIsError` FAILS (install returns nil — load failure is currently only a warning). `TestInstallLaunchd_ExistingPlistWithoutForce` passes (already-correct behavior, kept as a regression guard).

- [ ] **Step 3: Add `reloadLaunchdService` and change the call sites**

In `internal/cli/daemon.go`, add next to `getLaunchdPlistPath`:

```go
// reloadLaunchdService replaces whatever job definition launchd currently
// holds with the plist on disk. The unload error is ignored: not-loaded is
// the expected case on a fresh install.
func reloadLaunchdService(plistPath string) error {
	_ = exec.Command("launchctl", "unload", plistPath).Run()
	return exec.Command("launchctl", "load", plistPath).Run()
}
```

In `installLaunchdService`, replace the load block (currently `daemon.go:439-444`):

```go
	// Load the service, replacing any already-loaded definition — the normal
	// case under --force, where an installation exists by definition (#439).
	if err := reloadLaunchdService(plistPath); err != nil {
		return fmt.Errorf("load service: %w (plist written to %s; load manually with: launchctl load %s)", err, plistPath, plistPath)
	}
	fmt.Fprintln(w, "Service loaded and started")
```

In `newDaemonRestartCmd`, replace the darwin branch's inline unload/load pair (currently `daemon.go:178-187`):

```go
		case "darwin":
			fmt.Fprintln(w, "Restarting agentsh daemon...")
			if err := reloadLaunchdService(getLaunchdPlistPath()); err != nil {
				return fmt.Errorf("restart failed: %w", err)
			}
			fmt.Fprintln(w, "Daemon restarted successfully")
			return nil
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/cli/ -run 'TestInstallLaunchd' -v`
Expected: all four PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/cli/daemon.go internal/cli/daemon_install_test.go
git commit -m "fix(daemon): reload launchd service on install so --force takes effect (#439)

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 3: Linux — restart an active unit on install, hard-error on restart failure

**Files:**
- Modify: `internal/cli/daemon.go` (add `restartSystemdIfActive`; change `installSystemdService`)
- Modify: `internal/cli/daemon_install_test.go`

- [ ] **Step 1: Add the systemd tests**

Append to `internal/cli/daemon_install_test.go`:

```go
func TestInstallSystemd_RestartsActiveUnit(t *testing.T) {
	callsFile := setupFakeTools(t, "", `[ "$2" = "is-active" ] && { echo active; exit 0; }`)
	cmd, buf := newTestCmd()

	if err := installSystemdService(cmd, true); err != nil {
		t.Fatalf("install failed: %v", err)
	}

	assertCalls(t, recordedCalls(t, callsFile), []string{
		"systemctl --user daemon-reload",
		"systemctl --user enable agentsh",
		"systemctl --user is-active agentsh",
		"systemctl --user restart agentsh",
	})
	unitPath := filepath.Join(os.Getenv("HOME"), ".config", "systemd", "user", "agentsh.service")
	if _, err := os.Stat(unitPath); err != nil {
		t.Errorf("unit not written inside sandbox HOME: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "Service restarted with updated configuration") {
		t.Errorf("missing restart message, got: %s", out)
	}
	if strings.Contains(out, "To start the daemon now") {
		t.Errorf("start hint should be suppressed after a restart, got: %s", out)
	}
}

func TestInstallSystemd_InactiveUnitNotRestarted(t *testing.T) {
	callsFile := setupFakeTools(t, "", `[ "$2" = "is-active" ] && { echo inactive; exit 3; }`)
	cmd, buf := newTestCmd()

	if err := installSystemdService(cmd, false); err != nil {
		t.Fatalf("install failed: %v", err)
	}

	for _, call := range recordedCalls(t, callsFile) {
		if strings.Contains(call, " restart ") || strings.HasSuffix(call, " restart agentsh") {
			t.Errorf("unexpected restart call: %s", call)
		}
	}
	if !strings.Contains(buf.String(), "To start the daemon now") {
		t.Errorf("start hint should be preserved when unit inactive, got: %s", buf.String())
	}
}

func TestInstallSystemd_RestartFailureIsError(t *testing.T) {
	setupFakeTools(t, "", `[ "$2" = "is-active" ] && { echo active; exit 0; }
[ "$2" = "restart" ] && exit 1`)
	cmd, _ := newTestCmd()

	err := installSystemdService(cmd, true)
	if err == nil {
		t.Fatal("expected error when systemctl restart fails")
	}
	if !strings.Contains(err.Error(), "systemctl --user restart agentsh") {
		t.Errorf("error should include manual remediation: %v", err)
	}
}
```

- [ ] **Step 2: Run the new tests to verify they fail**

Run: `go test ./internal/cli/ -run 'TestInstallSystemd' -v`
Expected: `TestInstallSystemd_RestartsActiveUnit` FAILS (no `is-active`/`restart` calls recorded, no restart message). `TestInstallSystemd_RestartFailureIsError` FAILS (returns nil). `TestInstallSystemd_InactiveUnitNotRestarted` does NOT fail — it guards current behavior and must stay green through the change.

- [ ] **Step 3: Add `restartSystemdIfActive` and the call site**

In `internal/cli/daemon.go`, add next to `runSystemctl`:

```go
// restartSystemdIfActive restarts the agentsh user unit only when it is
// currently active, so install is never the thing that first starts the
// daemon on Linux. The bool reports whether a restart occurred. is-active is
// queried via Output() rather than runSystemctl so "inactive" does not leak
// to the terminal.
func restartSystemdIfActive() (bool, error) {
	out, err := exec.Command("systemctl", "--user", "is-active", "agentsh").Output()
	if err != nil || strings.TrimSpace(string(out)) != "active" {
		return false, nil
	}
	if err := runSystemctl("restart", "agentsh"); err != nil {
		return false, err
	}
	return true, nil
}
```

In `installSystemdService`, insert after the enable block and change the closing prints (currently `daemon.go:286-301`, through `return nil`) to:

```go
	// Enable service
	if err := runSystemctl("enable", "agentsh"); err != nil {
		fmt.Fprintf(w, "Warning: failed to enable service: %v\n", err)
	} else {
		fmt.Fprintln(w, "Service enabled for automatic start on login")
	}

	// A pre-existing daemon (notably under --force) keeps running the old
	// ExecStart until bounced; restart so the new unit takes effect (#439).
	restarted, err := restartSystemdIfActive()
	if err != nil {
		return fmt.Errorf("restart service: %w (unit written to %s; restart manually with: systemctl --user restart agentsh)", err, servicePath)
	}
	if restarted {
		fmt.Fprintln(w, "Service restarted with updated configuration")
	}

	fmt.Fprintln(w)
	if !restarted {
		fmt.Fprintln(w, "To start the daemon now:")
		fmt.Fprintln(w, "  systemctl --user start agentsh")
		fmt.Fprintln(w)
	}
	fmt.Fprintln(w, "To check status:")
	fmt.Fprintln(w, "  systemctl --user status agentsh")
	fmt.Fprintln(w, "  agentsh daemon status")

	return nil
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/cli/ -run 'TestInstallSystemd' -v`
Expected: all three PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/cli/daemon.go internal/cli/daemon_install_test.go
git commit -m "fix(daemon): restart active systemd unit on install so --force takes effect (#439)

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 4: Full verification and PR

- [ ] **Step 1: Format check**

Run: `gofmt -l internal/cli/`
Expected: no output.

- [ ] **Step 2: Full test suite**

Run: `go test ./...`
Expected: all packages PASS (or pre-existing skips only).

- [ ] **Step 3: Cross-compile check (required by CLAUDE.md)**

Run: `GOOS=windows go build ./... && GOOS=linux go build ./...`
Expected: both succeed.

- [ ] **Step 4: Manual macOS verification — coordinate with the user first**

This step touches the real launchd session on the development machine; do not run it unattended. Per the issue: `agentsh daemon install`, mutate the plist (or install a build with different `ProgramArguments`), `agentsh daemon install --force`, then `launchctl list | grep agentsh` — the running job must reflect the new definition without a manual `agentsh daemon restart`. If the user prefers, skip and rely on CI + the fake-tool tests.

- [ ] **Step 5: Push and open PR**

Per the user's workflow: PR against `main`, wait for green CI, squash merge.

```bash
git push -u origin fix/439-daemon-install-reload
gh pr create --title "fix(daemon): daemon install --force leaves the old service definition running (#439)" --body "$(cat <<'EOF'
Fixes #439.

`daemon install --force` rewrote the service file but never made the running
service pick it up: on macOS `launchctl load` fails ("already loaded") and was
downgraded to a warning, leaving the old job definition active; on Linux the
unit was daemon-reloaded but a running daemon kept its old ExecStart.

- macOS: install now unloads before loading (shared `reloadLaunchdService`
  helper, also used by `daemon restart` so the two paths cannot drift again).
- Linux: install now restarts the unit iff it is already active
  (`restartSystemdIfActive`); install still never *starts* the daemon.
- Load/restart failures are hard errors instead of exit-0 warnings — an
  install that reports success while the daemon is not running is what hid
  #437.
- Home-dir resolution unified on `userHomeDir()` (was a `user.Current()` /
  `os.UserHomeDir()` mix), which also lets tests sandbox via `$HOME`.
- Regression tests with fake `launchctl`/`systemctl` binaries assert the exact
  call sequences and error propagation on both platforms.

Design spec: docs/superpowers/specs/2026-08-05-daemon-install-reload-design.md

🤖 Generated with [Claude Code](https://claude.com/claude-code)
EOF
)"
```

Then watch CI to green and squash merge (ask the user before merging if in doubt).
