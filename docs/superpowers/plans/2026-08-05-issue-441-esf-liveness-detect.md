# ESF Liveness-Gated Detect Implementation Plan (issue #441)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `agentsh detect` (and the tier/status surfaces) report `esf` available only when the system extension *process is provably running*, not merely activated — with diagnostics explaining why when it isn't.

**Architecture:** One new liveness helper in `internal/platform/darwin` (`CheckSysExtLiveness`) parses `systemextensionsctl list` (activation + team ID) and `launchctl print system/<TeamID>.ai.canyonroad.agentsh.SysExt` (service state), failing closed on any probe failure. The three existing activation-only call sites (`capabilities/detect_darwin.go`, `platform/darwin/permissions.go`, `platform/darwin/sysext.go`) collapse onto it. Reason-sensitive tips in `capabilities/tips.go` turn the liveness `Detail` string into actionable guidance (e.g. `OS_REASON_EXEC` → AMFI/provisioning-profile hint).

**Tech Stack:** Go stdlib only (`os/exec` with `CommandContext` timeouts, pure string parsing). Tests: std `testing` in `platform/darwin` and `detect_darwin_test.go`; `testify/assert` in `tips_test.go` (existing per-file conventions).

**Spec:** `docs/superpowers/specs/2026-08-05-issue-441-esf-liveness-detect-design.md` — read it before starting; its decision table is normative.

**Branch:** work on `issue-441-esf-liveness-detect` (already exists, contains the spec commit).

**Module path:** `github.com/agentsh/agentsh`

**Verified environment facts** (from design session, on a machine exhibiting the bug):
- `systemextensionsctl list` row format (tab-separated, bundle ID appears TWICE — as identifier and display name):
  `*	*	WCKWMMKJ35	ai.canyonroad.agentsh.SysExt (1.0/14)	ai.canyonroad.agentsh.SysExt	[activated enabled]`
- `launchctl print system/<TeamID>.<bundleID>` works unprivileged; service-level `state = …` is the FIRST `state =` line; nested sub-sections contain their own `state = active` lines.
- `platform/darwin` builds without CGO (`sysext_activate_nocgo.go` fallback), and nothing in `platform/darwin` imports `internal/capabilities` — the new `capabilities → platform/darwin` edge is verified acyclic, so no build-graph risk.
- `DetectResult.Table()` already renders `DetectedBackend.Detail` — no rendering changes needed.

---

### Task 1: `parseSysExtList` — per-line activation + team-ID parsing

> **Amended during execution** (see the Task 1 commit): code review hardened `parseSysExtList` — activation now requires an exact bundle-ID field token (immune to prefix-sibling bundles like `...SysExtBeta`), scanning continues past rows that yield no team ID, and `"*"` is rejected as a team ID (blank-column marker). `runLivenessCommand` additionally folds captured stderr — whitespace-collapsed to one line — into returned errors so probe diagnostics carry the tool's actual message. Three regression subtests were added. The committed code is authoritative over the blocks below.

**Files:**
- Create: `internal/platform/darwin/sysext_liveness.go`
- Create: `internal/platform/darwin/sysext_liveness_test.go`
- Modify: `internal/platform/darwin/sysext_activate_cgo.go:115` (delete its duplicate `sysExtBundleID` const)

- [ ] **Step 1: Write the failing test**

Create `internal/platform/darwin/sysext_liveness_test.go`:

```go
//go:build darwin

package darwin

import "testing"

// Real output captured 2026-08-05 from a machine where the agentsh sysext is
// activated but AMFI/launchd keeps it from running (the #441 specimen). Note
// the co-installed beacon extension: whole-output substring matching would
// false-positive on it.
const sysextListBoth = `2 extension(s)
--- com.apple.system_extension.endpoint_security (Go to 'System Settings > General > Login Items & Extensions > Endpoint Security Extensions' to modify these system extension(s))
enabled	active	teamID	bundleID (version)	name	[state]
*	*	WCKWMMKJ35	ai.canyonroad.agentsh.SysExt (1.0/14)	ai.canyonroad.agentsh.SysExt	[activated enabled]
*	*	WCKWMMKJ35	ai.canyonroad.beacon.sysext (0.1.0/1781653639)	Beacon System Extension	[activated enabled]
`

const sysextListBeaconOnly = `1 extension(s)
--- com.apple.system_extension.endpoint_security
enabled	active	teamID	bundleID (version)	name	[state]
*	*	WCKWMMKJ35	ai.canyonroad.beacon.sysext (0.1.0/1781653639)	Beacon System Extension	[activated enabled]
`

const sysextListWaiting = `1 extension(s)
--- com.apple.system_extension.endpoint_security
enabled	active	teamID	bundleID (version)	name	[state]
		WCKWMMKJ35	ai.canyonroad.agentsh.SysExt (1.0/14)	ai.canyonroad.agentsh.SysExt	[activated waiting for user]
`

// Upgrade transient: old version terminating, new one activated enabled.
const sysextListUpgrade = `2 extension(s)
--- com.apple.system_extension.endpoint_security
enabled	active	teamID	bundleID (version)	name	[state]
		WCKWMMKJ35	ai.canyonroad.agentsh.SysExt (1.0/13)	ai.canyonroad.agentsh.SysExt	[terminated waiting to uninstall on reboot]
*	*	WCKWMMKJ35	ai.canyonroad.agentsh.SysExt (1.0/14)	ai.canyonroad.agentsh.SysExt	[activated enabled]
`

func TestParseSysExtList(t *testing.T) {
	tests := []struct {
		name          string
		output        string
		wantActivated bool
		wantTeamID    string
	}{
		{"activated with neighbor extension", sysextListBoth, true, "WCKWMMKJ35"},
		{"neighbor only must not match", sysextListBeaconOnly, false, ""},
		{"waiting for user is not activated", sysextListWaiting, false, ""},
		{"upgrade transient finds enabled row", sysextListUpgrade, true, "WCKWMMKJ35"},
		{"empty output", "", false, ""},
		{"garbage output", "no extensions here\njust noise\n", false, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			activated, teamID := parseSysExtList(tt.output)
			if activated != tt.wantActivated {
				t.Errorf("activated = %v, want %v", activated, tt.wantActivated)
			}
			if teamID != tt.wantTeamID {
				t.Errorf("teamID = %q, want %q", teamID, tt.wantTeamID)
			}
		})
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/platform/darwin/ -run TestParseSysExtList -v`
Expected: FAIL to build with `undefined: parseSysExtList`

- [ ] **Step 3: Write minimal implementation**

First, delete line 115 of `internal/platform/darwin/sysext_activate_cgo.go`:

```go
const sysExtBundleID = "ai.canyonroad.agentsh.SysExt"
```

That file is `//go:build darwin && cgo`, so with CGO enabled (the default on macOS) it compiles into the same package and would collide with the const below. `sysext_liveness.go` (plain `//go:build darwin`) becomes the owner — it is built on darwin both with and without cgo, so the remaining use at `sysext_activate_cgo.go:120` keeps compiling in every build mode.

Then create `internal/platform/darwin/sysext_liveness.go`:

```go
//go:build darwin

package darwin

import (
	"context"
	"os/exec"
	"strings"
	"time"
)

const (
	sysExtBundleID     = "ai.canyonroad.agentsh.SysExt"
	livenessCmdTimeout = 5 * time.Second
)

// SysExtLiveness reports two separate facts about the system extension:
// whether it is activated (systemextensionsctl) and whether its process is
// actually running (launchd service state). Running is set only on positive
// proof (state = running); every probe failure fails closed (issue #441 —
// an activated-but-AMFI-blocked extension must not report as healthy).
type SysExtLiveness struct {
	Activated   bool   // systemextensionsctl row for our bundle ID says "activated enabled"
	Running     bool   // launchctl service state is "running"
	ProbeFailed bool   // a probe command failed or its output was unparseable (Running stays false)
	State       string // raw launchd state ("running", "spawn scheduled", ...); "" if unknown
	LastExit    string // "exit code 1", "OS_REASON_EXEC ...", ...; "" if none
	Detail      string // human-readable one-liner; tips.go matches substrings of this
}

// runLivenessCommand executes a probe command with a timeout. Package-level
// var so tests can inject fixture output.
var runLivenessCommand = func(name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), livenessCmdTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, name, args...).Output()
	return string(out), err
}

// parseSysExtList scans systemextensionsctl list output line by line for an
// "activated enabled" row belonging to our bundle ID, and extracts the team
// ID from that row. The team ID is the field immediately preceding the FIRST
// occurrence of the bundle-ID token: the row repeats the bundle ID as its
// display name, so matching the last occurrence would return the version
// column instead. Per-line matching also prevents a different extension's
// "activated enabled" from satisfying the check.
func parseSysExtList(output string) (activated bool, teamID string) {
	for _, line := range strings.Split(output, "\n") {
		if !strings.Contains(line, sysExtBundleID) ||
			!strings.Contains(line, "activated enabled") {
			continue
		}
		fields := strings.Fields(line)
		for i, f := range fields {
			if f == sysExtBundleID {
				if i > 0 {
					return true, fields[i-1]
				}
				return true, ""
			}
		}
		return true, ""
	}
	return false, ""
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/platform/darwin/ -run TestParseSysExtList -v`
Expected: PASS (all 6 subtests)

- [ ] **Step 5: Commit**

```bash
git add internal/platform/darwin/sysext_liveness.go internal/platform/darwin/sysext_liveness_test.go internal/platform/darwin/sysext_activate_cgo.go
git commit -m "feat(#441): parseSysExtList — per-line activation + team-ID parsing"
```

---

### Task 2: `parseLaunchdState` — service state + last-exit extraction

**Files:**
- Modify: `internal/platform/darwin/sysext_liveness.go` (append function)
- Modify: `internal/platform/darwin/sysext_liveness_test.go` (append fixtures + test)

- [ ] **Step 1: Write the failing test**

Append to `internal/platform/darwin/sysext_liveness_test.go`:

```go
// Trimmed real output (2026-08-05) from the #441 specimen machine. The
// nested "state = active" lines below the top-level state are real — the
// parser must take only the FIRST "state =" line.
const launchdSpawnScheduled = `system/WCKWMMKJ35.ai.canyonroad.agentsh.SysExt = {
	active count = 0
	path = (submitted by smd[532])
	type = Submitted
	state = spawn scheduled

	program = /Library/SystemExtensions/0FED1DDB-30D3-4AAD-A31C-8E7F1229868E/ai.canyonroad.agentsh.SysExt.systemextension/Contents/MacOS/ai.canyonroad.agentsh.SysExt
	domain = system
	minimum runtime = 10
	exit timeout = 5
	runs = 199773
	last exit code = 1

	event triggers = {
		ai.canyonroad.agentsh.SysExt => {
			state = active
		}
		ai.canyonroad.agentsh.SysExt.esf => {
			state = active
		}
	}
}
`

const launchdRunning = `system/WCKWMMKJ35.ai.canyonroad.agentsh.SysExt = {
	active count = 1
	path = (submitted by smd[532])
	type = Submitted
	state = running

	program = /Library/SystemExtensions/0FED1DDB-30D3-4AAD-A31C-8E7F1229868E/ai.canyonroad.agentsh.SysExt.systemextension/Contents/MacOS/ai.canyonroad.agentsh.SysExt
	pid = 4242
	domain = system
	runs = 1
	last exit code = (never exited)
}
`

// Synthesized from the #436 report (AMFI rejects the binary at exec).
const launchdAMFIBlocked = `system/WCKWMMKJ35.ai.canyonroad.agentsh.SysExt = {
	active count = 0
	state = spawn scheduled
	runs = 324
	last exit reason = OS_REASON_EXEC | Error -413 "No matching profile found"
	last exit code = (never exited)
}
`

func TestParseLaunchdState(t *testing.T) {
	tests := []struct {
		name         string
		output       string
		wantState    string
		wantLastExit string
	}{
		{"spawn scheduled with exit code", launchdSpawnScheduled, "spawn scheduled", "exit code 1"},
		{"running, never exited suppressed", launchdRunning, "running", ""},
		{"amfi blocked reason preferred over code", launchdAMFIBlocked, "spawn scheduled", `OS_REASON_EXEC | Error -413 "No matching profile found"`},
		{"no state line", "system/x = {\n\truns = 3\n}\n", "", ""},
		{"empty output", "", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state, lastExit := parseLaunchdState(tt.output)
			if state != tt.wantState {
				t.Errorf("state = %q, want %q", state, tt.wantState)
			}
			if lastExit != tt.wantLastExit {
				t.Errorf("lastExit = %q, want %q", lastExit, tt.wantLastExit)
			}
		})
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/platform/darwin/ -run TestParseLaunchdState -v`
Expected: FAIL to build with `undefined: parseLaunchdState`

- [ ] **Step 3: Write minimal implementation**

Append to `internal/platform/darwin/sysext_liveness.go`:

```go
// parseLaunchdState extracts the service-level state and last-exit info from
// `launchctl print system/<label>` output. Only the FIRST "state =" line is
// the service state: nested sub-sections (event triggers, XPC endpoints)
// contain their own "state = active" lines. "last exit reason" (present on
// exec-level failures like AMFI rejection, per #436) is preferred over
// "last exit code"; a code of "(never exited)" or "0" is not an exit signal.
func parseLaunchdState(output string) (state, lastExit string) {
	var exitCode, exitReason string
	for _, line := range strings.Split(output, "\n") {
		trimmed := strings.TrimSpace(line)
		if state == "" {
			if v, ok := strings.CutPrefix(trimmed, "state = "); ok {
				state = strings.TrimSpace(v)
				continue
			}
		}
		if v, ok := strings.CutPrefix(trimmed, "last exit reason = "); ok && exitReason == "" {
			exitReason = strings.TrimSpace(v)
		}
		if v, ok := strings.CutPrefix(trimmed, "last exit code = "); ok && exitCode == "" {
			exitCode = strings.TrimSpace(v)
		}
	}
	switch {
	case exitReason != "":
		lastExit = exitReason
	case exitCode != "" && exitCode != "0" && exitCode != "(never exited)":
		lastExit = "exit code " + exitCode
	}
	return state, lastExit
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/platform/darwin/ -run TestParseLaunchdState -v`
Expected: PASS (all 5 subtests)

- [ ] **Step 5: Commit**

```bash
git add internal/platform/darwin/sysext_liveness.go internal/platform/darwin/sysext_liveness_test.go
git commit -m "feat(#441): parseLaunchdState — first state line + last-exit extraction"
```

---

### Task 3: `CheckSysExtLiveness` — fail-closed decision table

> **Amended during execution** (see the Task 3 commit): the decision-table test grew to 9 rows with `wantState`/`wantLastExit` columns, single-line/non-empty Detail invariant assertions, and mutation-verified pins on the exact `state == "running"` gate and the blank-team-ID guard. Code deltas: the systemextensionsctl-failure Detail now carries the "could not be verified" token (tips routing), the no-state branch appends `(last exit: …)` when present, timeouts report as `timed out after 5s`, and the branch switch reads `switch state`. The committed code is authoritative over the blocks below.

**Files:**
- Modify: `internal/platform/darwin/sysext_liveness.go` (append function)
- Modify: `internal/platform/darwin/sysext_liveness_test.go` (append test)

- [ ] **Step 1: Write the failing test**

First, change the import line at the top of `internal/platform/darwin/sysext_liveness_test.go` from `import "testing"` to:

```go
import (
	"errors"
	"strings"
	"testing"
)
```

Then append to the file:

```go
func TestCheckSysExtLiveness_DecisionTable(t *testing.T) {
	tests := []struct {
		name            string
		sysextOut       string
		sysextErr       error
		launchctlOut    string
		launchctlErr    error
		wantActivated   bool
		wantRunning     bool
		wantProbeFailed bool
		wantDetailSub   string // Detail must contain this substring
	}{
		{
			name:          "not activated",
			sysextOut:     sysextListBeaconOnly,
			wantDetailSub: "not activated",
		},
		{
			name:            "systemextensionsctl fails -> not activated, probe failed",
			sysextErr:       errors.New("exec: not found"),
			wantProbeFailed: true,
			wantDetailSub:   "not activated",
		},
		{
			name:          "activated and running",
			sysextOut:     sysextListBoth,
			launchctlOut:  launchdRunning,
			wantActivated: true,
			wantRunning:   true,
			wantDetailSub: "running",
		},
		{
			name:          "activated, spawn scheduled -> not running with diagnostics",
			sysextOut:     sysextListBoth,
			launchctlOut:  launchdSpawnScheduled,
			wantActivated: true,
			wantRunning:   false,
			wantDetailSub: "activated but not running (state: spawn scheduled, last exit: exit code 1)",
		},
		{
			name:          "activated, AMFI blocked -> Detail carries OS_REASON_EXEC",
			sysextOut:     sysextListBoth,
			launchctlOut:  launchdAMFIBlocked,
			wantActivated: true,
			wantRunning:   false,
			wantDetailSub: "OS_REASON_EXEC",
		},
		{
			name:            "activated, launchctl fails -> fail closed",
			sysextOut:       sysextListBoth,
			launchctlErr:    errors.New("Could not find service"),
			wantActivated:   true,
			wantRunning:     false,
			wantProbeFailed: true,
			wantDetailSub:   "could not be verified",
		},
		{
			name:            "activated, no state line -> fail closed",
			sysextOut:       sysextListBoth,
			launchctlOut:    "system/x = {\n\truns = 3\n}\n",
			wantActivated:   true,
			wantRunning:     false,
			wantProbeFailed: true,
			wantDetailSub:   "could not be verified",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			restore := runLivenessCommand
			defer func() { runLivenessCommand = restore }()
			var launchctlLabel string
			runLivenessCommand = func(name string, args ...string) (string, error) {
				if name == "systemextensionsctl" {
					return tt.sysextOut, tt.sysextErr
				}
				if name == "launchctl" && len(args) == 2 && args[0] == "print" {
					launchctlLabel = args[1]
				}
				return tt.launchctlOut, tt.launchctlErr
			}

			got := CheckSysExtLiveness()
			if got.Activated != tt.wantActivated {
				t.Errorf("Activated = %v, want %v", got.Activated, tt.wantActivated)
			}
			if got.Running != tt.wantRunning {
				t.Errorf("Running = %v, want %v", got.Running, tt.wantRunning)
			}
			if got.ProbeFailed != tt.wantProbeFailed {
				t.Errorf("ProbeFailed = %v, want %v", got.ProbeFailed, tt.wantProbeFailed)
			}
			if !strings.Contains(got.Detail, tt.wantDetailSub) {
				t.Errorf("Detail = %q, want substring %q", got.Detail, tt.wantDetailSub)
			}
			if tt.wantActivated && tt.launchctlErr == nil && tt.launchctlOut != "" {
				want := "system/WCKWMMKJ35." + sysExtBundleID
				if launchctlLabel != want {
					t.Errorf("launchctl label = %q, want %q", launchctlLabel, want)
				}
			}
		})
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/platform/darwin/ -run TestCheckSysExtLiveness -v`
Expected: FAIL to build with `undefined: CheckSysExtLiveness`

- [ ] **Step 3: Write minimal implementation**

Append to `internal/platform/darwin/sysext_liveness.go`:

```go
// CheckSysExtLiveness probes whether the agentsh system extension is
// activated AND its process is actually running. Decision table (fail
// closed — Running requires positive proof of state = running):
//
//	systemextensionsctl        launchctl                      -> result
//	not activated / cmd fails  (skipped)                      Activated=false, Running=false
//	activated                  state = running                Running=true
//	activated                  any other state                Running=false, Detail has state + last exit
//	activated                  cmd fails / no state / no team Running=false, Detail "could not be verified"
func CheckSysExtLiveness() SysExtLiveness {
	out, err := runLivenessCommand("systemextensionsctl", "list")
	if err != nil {
		return SysExtLiveness{ProbeFailed: true, Detail: "not activated (systemextensionsctl failed: " + err.Error() + ")"}
	}
	activated, teamID := parseSysExtList(out)
	if !activated {
		return SysExtLiveness{Detail: "not activated"}
	}

	liveness := SysExtLiveness{Activated: true}
	if teamID == "" {
		liveness.ProbeFailed = true
		liveness.Detail = "activated but liveness could not be verified (no team ID in systemextensionsctl output)"
		return liveness
	}

	label := "system/" + teamID + "." + sysExtBundleID
	lout, err := runLivenessCommand("launchctl", "print", label)
	if err != nil {
		liveness.ProbeFailed = true
		liveness.Detail = "activated but liveness could not be verified (launchctl print " + label + " failed: " + err.Error() + ")"
		return liveness
	}

	state, lastExit := parseLaunchdState(lout)
	liveness.State = state
	liveness.LastExit = lastExit
	switch {
	case state == "running":
		liveness.Running = true
		liveness.Detail = "running"
	case state == "":
		liveness.ProbeFailed = true
		liveness.Detail = "activated but liveness could not be verified (no state in launchctl output)"
	default:
		detail := "activated but not running (state: " + state
		if lastExit != "" {
			detail += ", last exit: " + lastExit
		}
		liveness.Detail = detail + ")"
	}
	return liveness
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/platform/darwin/ -run TestCheckSysExtLiveness -v`
Expected: PASS (all 7 subtests)

- [ ] **Step 5: Run the whole package to catch regressions**

Run: `go test ./internal/platform/darwin/...`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add internal/platform/darwin/sysext_liveness.go internal/platform/darwin/sysext_liveness_test.go
git commit -m "feat(#441): CheckSysExtLiveness — fail-closed sysext liveness probe"
```

---

### Task 4: Wire `detect_darwin.go` onto the helper

**Files:**
- Modify: `internal/capabilities/detect_darwin.go` (delete `checkSysExtInstalled` at lines 110-122; change `Detect()` and `buildDarwinDomains`)
- Modify: `internal/capabilities/detect_darwin_test.go`

- [ ] **Step 1: Write the failing tests**

In `internal/capabilities/detect_darwin_test.go`, add `"strings"` to imports, add the new test, and extend `TestDetect_Darwin`'s `expectedKeys`:

```go
func TestBuildDarwinDomains_ESFDetail(t *testing.T) {
	caps := map[string]any{"esf": false, "network_extension": false}
	detail := "activated but not running (state: spawn scheduled, last exit: exit code 1)"
	domains := buildDarwinDomains(caps, detail)

	found := 0
	for _, d := range domains {
		for _, b := range d.Backends {
			if b.Name != "esf" {
				continue
			}
			found++
			if b.Available {
				t.Errorf("domain %q: esf Available = true, want false", d.Name)
			}
			if !strings.Contains(b.Detail, "not running") {
				t.Errorf("domain %q: esf Detail = %q, want liveness detail", d.Name, b.Detail)
			}
		}
	}
	if found != 2 {
		t.Errorf("found %d esf backends, want 2 (File Protection, Command Control)", found)
	}
}
```

In `TestDetect_Darwin`, change:

```go
	expectedKeys := []string{"sandbox_exec", "esf", "esf_activated", "network_extension"}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/capabilities/ -run 'TestBuildDarwinDomains_ESFDetail|TestDetect_Darwin' -v`
Expected: FAIL to build — `buildDarwinDomains` takes 1 argument, and (once building) `esf_activated` key missing.

- [ ] **Step 3: Implement**

In `internal/capabilities/detect_darwin.go`:

1. Replace the imports block (only `checkSysExtInstalled` used `strings`):

```go
import (
	"os/exec"

	"github.com/agentsh/agentsh/internal/platform/darwin"
)
```

2. Change the signature `func buildDarwinDomains(caps map[string]any) []ProtectionDomain` to:

```go
func buildDarwinDomains(caps map[string]any, esfDetail string) []ProtectionDomain {
```

and set `Detail: esfDetail` on BOTH esf backend entries (File Protection line 24, Command Control line 30), replacing `Detail: ""`.

3. In `Detect()`, replace the caps construction and domain build:

```go
	liveness := darwin.CheckSysExtLiveness()
	caps := map[string]any{
		"sandbox_exec":      true,
		"esf":               liveness.Running,
		"esf_activated":     liveness.Activated,
		"network_extension": checkNetworkExtension(),
		"lima_available":    checkLima(),
	}

	mode, _ := selectDarwinMode(caps)
	domains := buildDarwinDomains(caps, liveness.Detail)
```

4. Delete `checkSysExtInstalled()` entirely (lines 110-122 including the stale "Delegates to the darwin package" comment).

`selectDarwinMode` is untouched — it reads `caps["esf"]`, which is now honest.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/capabilities/ -run 'TestBuildDarwinDomains_ESFDetail|TestDetect_Darwin|TestSelectDarwinMode' -v`
Expected: PASS. (`TestDetect_Darwin` shells out to the real system — it passes regardless of local sysext state because it only asserts key presence.)

- [ ] **Step 5: Commit**

```bash
git add internal/capabilities/detect_darwin.go internal/capabilities/detect_darwin_test.go
git commit -m "fix(#441): detect gates esf on sysext liveness, not activation"
```

---

### Task 5: Reason-sensitive esf tips

**Files:**
- Modify: `internal/capabilities/tips.go` (the `"esf"` entry in `tipsByBackend` ~line 178; the esf entry in `darwinTips` ~line 74)
- Modify: `internal/capabilities/tips_test.go`

- [ ] **Step 1: Write the failing test**

Append to `internal/capabilities/tips_test.go` (this file uses testify `assert`):

```go
func TestLookupTip_ESFReasonSensitive(t *testing.T) {
	// AMFI rejection: OS_REASON_EXEC must win even though the detail also
	// contains "not running" (first-match-wins ordering).
	amfi := lookupTip("esf", `activated but not running (state: spawn scheduled, last exit: OS_REASON_EXEC | Error -413 "No matching profile found")`)
	assert.NotNil(t, amfi)
	assert.Contains(t, amfi.Action, "embedded.provisionprofile")

	// Generic not-running: points at launchctl diagnostics.
	stuck := lookupTip("esf", "activated but not running (state: spawn scheduled, last exit: exit code 1)")
	assert.NotNil(t, stuck)
	assert.Contains(t, stuck.Action, "launchctl print")
	assert.NotContains(t, stuck.Action, "embedded.provisionprofile")

	// Probe failure: liveness unverifiable.
	unverified := lookupTip("esf", "activated but liveness could not be verified (no state in launchctl output)")
	assert.NotNil(t, unverified)
	assert.Contains(t, unverified.Action, "launchctl print")

	// Not installed at all: fallback install tip unchanged.
	fallback := lookupTip("esf", "not activated")
	assert.NotNil(t, fallback)
	assert.Contains(t, fallback.Action, "Install the agentsh macOS app bundle")
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/capabilities/ -run TestLookupTip_ESFReasonSensitive -v`
Expected: FAIL — the current single esf entry returns the install tip for every detail.

- [ ] **Step 3: Implement**

In `tips.go`, replace the `"esf"` entry in `tipsByBackend`:

```go
	"esf": {
		{Contains: "OS_REASON_EXEC", Tip: Tip{Feature: "esf", Impact: "Endpoint Security enforcement absent (extension binary rejected at exec)", Action: "macOS refuses to launch the activated extension — likely AMFI/code-signing (e.g. missing embedded.provisionprofile, see #436). Verify the profile inside the .systemextension bundle and reinstall, then check `launchctl print system/<TeamID>.ai.canyonroad.agentsh.SysExt`."}},
		{Contains: "not running", Tip: Tip{Feature: "esf", Impact: "Endpoint Security enforcement absent (extension activated but not running)", Action: "Inspect `launchctl print system/<TeamID>.ai.canyonroad.agentsh.SysExt` for state and last exit reason."}},
		{Contains: "could not be verified", Tip: Tip{Feature: "esf", Impact: "Endpoint Security liveness unverifiable", Action: "Could not verify the extension process is running. Check it manually: `launchctl print system/<TeamID>.ai.canyonroad.agentsh.SysExt`."}},
		{Tip: Tip{Feature: "esf", Impact: "Endpoint Security Framework unavailable", Action: "Install the agentsh macOS app bundle with system extension"}},
	},
```

Ordering is load-bearing: `OS_REASON_EXEC` before `not running` (a detail can contain both), specific entries before the empty-`Contains` fallback.

Also refresh the legacy `darwinTips` esf entry (consumed only by `GenerateTips`, which has no non-test callers — updated for consistency rather than left stale):

```go
	{
		Feature:  "esf",
		CheckKey: "esf",
		Impact:   "ESF enforcement unavailable (extension not installed or not running)",
		Action:   "Install the agentsh macOS app bundle and ensure the system extension is approved and running (see `agentsh detect` detail).",
	},
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/capabilities/`
Expected: PASS (includes `TestGenerateTips_Darwin` — if it asserts the old esf `Impact`/`Action` text, update those assertions to the new text as part of this step).

- [ ] **Step 5: Commit**

```bash
git add internal/capabilities/tips.go internal/capabilities/tips_test.go
git commit -m "feat(#441): reason-sensitive esf tips (AMFI, not-running, unverifiable)"
```

---

### Task 6: Honest permission tier in `permissions.go`

**Files:**
- Modify: `internal/platform/darwin/permissions.go`
- Modify: `internal/platform/darwin/permissions_test.go` (append — this file ALREADY EXISTS with ~205 lines of tests; do NOT overwrite it)

- [ ] **Step 1: Write the failing test**

Append the following functions to the END of the existing `internal/platform/darwin/permissions_test.go`. Its import block already has exactly what these need (`strings`, `testing`) — no import changes. The new names (`TestComputeTier_RequiresRunningSysExt`, `TestComputeMissingPermissions_SysExtBranches`, `findMissing`) don't collide with existing tests, and the existing `TestPermissions_computeMissingPermissions` keeps passing under the new branch logic.

```go
func TestComputeTier_RequiresRunningSysExt(t *testing.T) {
	p := &Permissions{HasSystemExtension: true}
	p.computeTier()
	if p.Tier != TierEnterprise {
		t.Errorf("Tier = %v, want TierEnterprise when sysext running", p.Tier)
	}

	// Activated but not running must NOT reach enterprise tier.
	p = &Permissions{SysExtActivated: true, HasSystemExtension: false}
	p.computeTier()
	if p.Tier == TierEnterprise {
		t.Error("Tier = TierEnterprise for activated-but-not-running sysext; want lower tier")
	}
}

func TestComputeMissingPermissions_SysExtBranches(t *testing.T) {
	// Not installed: install guidance.
	p := &Permissions{}
	p.computeMissingPermissions()
	mp := findMissing(t, p, "System Extension")
	if !strings.Contains(mp.HowToEnable, "Install the agentsh macOS app bundle") {
		t.Errorf("HowToEnable = %q, want install guidance", mp.HowToEnable)
	}

	// Activated but not running: launchctl diagnostics, not install guidance.
	p = &Permissions{
		SysExtActivated: true,
		SysExtDetail:    "activated but not running (state: spawn scheduled, last exit: exit code 1)",
	}
	p.computeMissingPermissions()
	mp = findMissing(t, p, "System Extension")
	if !strings.Contains(mp.HowToEnable, "launchctl print") {
		t.Errorf("HowToEnable = %q, want launchctl diagnostics", mp.HowToEnable)
	}
	if !strings.Contains(mp.HowToEnable, p.SysExtDetail) {
		t.Errorf("HowToEnable = %q, want embedded liveness detail", mp.HowToEnable)
	}
}

func findMissing(t *testing.T, p *Permissions, name string) MissingPermission {
	t.Helper()
	for _, mp := range p.MissingPermissions {
		if mp.Name == name {
			return mp
		}
	}
	t.Fatalf("MissingPermissions has no entry %q", name)
	return MissingPermission{}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/platform/darwin/ -run 'TestComputeTier_RequiresRunningSysExt|TestComputeMissingPermissions_SysExtBranches' -v`
Expected: FAIL to build with `unknown field SysExtActivated`

- [ ] **Step 3: Implement**

In `permissions.go`:

1. Extend the struct (after `HasSystemExtension bool`, line 55):

```go
	HasSystemExtension bool // extension activated AND its process is running

	// SysExtActivated distinguishes "activated but not running" (AMFI-blocked,
	// crash-looping — see #441) from "not installed at all".
	SysExtActivated bool
	SysExtDetail    string
```

2. In `DetectPermissions()` replace line 89 (`p.HasSystemExtension = CheckSysExtInstalled()`):

```go
	liveness := CheckSysExtLiveness()
	p.HasSystemExtension = liveness.Running
	p.SysExtActivated = liveness.Activated
	p.SysExtDetail = liveness.Detail
```

3. Delete `CheckSysExtInstalled()` (lines 104-113; no other callers — verified) and drop `"strings"` from imports if now unused (it is still used by `LogStatus`, so keep it).

4. In `computeMissingPermissions()` replace the sysext block:

```go
	if !p.HasSystemExtension {
		mp := MissingPermission{
			Name:        "System Extension",
			Description: "ESF-based file/process monitoring and Network Extension filtering",
			Impact:      "Cannot intercept or block file operations. File monitoring unavailable.",
			HowToEnable: "Install the agentsh macOS app bundle which includes the system extension.\n" +
				"After installation, approve it in System Settings > Privacy & Security.",
			Required: false,
		}
		if p.SysExtActivated {
			mp.Impact = "System extension is activated but its process is not running. ESF enforcement is absent."
			mp.HowToEnable = "Diagnose with: launchctl print system/<TeamID>.ai.canyonroad.agentsh.SysExt\n" +
				"Detected: " + p.SysExtDetail
		}
		p.MissingPermissions = append(p.MissingPermissions, mp)
	}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/platform/darwin/ -run 'TestComputeTier|TestComputeMissing' -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/platform/darwin/permissions.go internal/platform/darwin/permissions_test.go
git commit -m "fix(#441): enterprise tier requires a running sysext, not merely activated"
```

---

### Task 7: `SysExtManager.Status()` tells the truth; stub parity

> **Amended during execution** (see the Task 7 commit): `LastExit` is surfaced only when not running (spec amendment #5); `SysExtStatus` gained `ProbeFailed` (both build variants); the mapping test grew `wantState`/`wantProbeFailed` columns plus running-after-crash and systemextensionsctl-failure rows — mutation-verified against the suppression gate, the State passthrough, and the ProbeFailed→Error clause. `NewSysExtManager` uses the `sysExtBundleID` const; `Status()` documents the bundle-presence precondition and Installed-means-activated semantics. The committed code is authoritative over the blocks below.

**Files:**
- Modify: `internal/platform/darwin/sysext.go`
- Modify: `internal/platform/darwin/sysext_stub.go`
- Modify: `internal/platform/darwin/sysext_test.go`

- [ ] **Step 1: Update tests first**

In `sysext_test.go`:

1. Delete `TestContains` entirely (the `contains` helper dies in this task).
2. Extend `TestSysExtStatus_JSONTags` to cover the new fields — replace the struct literal and add assertions:

```go
	status := SysExtStatus{
		Installed:   true,
		Running:     true,
		State:       "running",
		LastExit:    "",
		Version:     "1.0.0",
		BundleID:    "ai.canyonroad.agentsh.SysExt",
		ExtensionID: "ext-123",
		Error:       "",
	}
```

and after the existing assertions:

```go
	if status.State != "running" {
		t.Error("State mismatch")
	}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/platform/darwin/ -run TestSysExtStatus_JSONTags -v`
Expected: FAIL to build with `unknown field State`

- [ ] **Step 3: Implement**

In `sysext.go`:

1. Extend `SysExtStatus`:

```go
type SysExtStatus struct {
	Installed   bool   `json:"installed"`
	Running     bool   `json:"running"`
	State       string `json:"state,omitempty"`
	LastExit    string `json:"last_exit,omitempty"`
	Version     string `json:"version,omitempty"`
	BundleID    string `json:"bundle_id,omitempty"`
	ExtensionID string `json:"extension_id,omitempty"`
	Error       string `json:"error,omitempty"`
}
```

2. Replace the body of `Status()` after the `m.bundlePath == ""` early return:

```go
	liveness := CheckSysExtLiveness()
	status.Installed = liveness.Activated
	status.Running = liveness.Running
	status.State = liveness.State
	status.LastExit = liveness.LastExit
	if liveness.ProbeFailed || (liveness.Activated && !liveness.Running) {
		status.Error = liveness.Detail
	}

	return status, nil
```

(Cleanly not-activated is a state, not an error — matching the old behavior where absence set no Error. Probe failures and activated-but-not-running do surface via Error, without coupling to Detail string literals.)

3. Delete the now-unused `contains()` helper, and remove `"os/exec"` from the imports — its only use was the `exec.Command("systemextensionsctl", "list")` call this step deletes, so leaving it fails the build with "imported and not used". Keep `"strings"` (`findAppBundle` still uses `strings.Index`).

4. In `sysext_stub.go`, mirror the two new fields in its `SysExtStatus` (identical `State`/`LastExit` lines) for struct parity.

- [ ] **Step 4: Run the package tests**

Run: `go test ./internal/platform/darwin/...`
Expected: PASS (note `TestSysExtManager_Status` shells out to the real system; it only asserts BundleID and non-nil status, so it passes in any sysext state)

- [ ] **Step 5: Commit**

```bash
git add internal/platform/darwin/sysext.go internal/platform/darwin/sysext_stub.go internal/platform/darwin/sysext_test.go
git commit -m "fix(#441): SysExtStatus.Running reflects real liveness; add State/LastExit"
```

---

### Task 8: Full verification, live acceptance, PR

**Files:** none (verification + git only)

- [ ] **Step 1: Full build and test suite**

Run: `go build ./... && go test ./...`
Expected: all packages build, all tests pass

- [ ] **Step 2: Cross-compile gates (CLAUDE.md requirement)**

Run: `GOOS=windows go build ./... && GOOS=linux go build ./...`
Expected: clean builds (the new file is `//go:build darwin`; stub parity covers `!darwin`)

- [ ] **Step 3: Live acceptance on the dev machine**

Run: `go run ./cmd/agentsh detect -o json | python3 -c "import json,sys; d=json.load(sys.stdin); print(d['security_mode'], d['capabilities']['esf'], d['capabilities']['esf_activated'])"`

Expected on the design machine (sysext activated, `spawn scheduled`): security mode is NOT `esf` (falls back to `lima`/`dynamic-seatbelt`/`sandbox-exec`), `esf` is `false`, `esf_activated` is `true`. Also run `go run ./cmd/agentsh detect` (table) and confirm the esf rows show the liveness detail and a tip block mentions `launchctl print`. If the machine's sysext has been repaired since the design session (`launchctl print system/WCKWMMKJ35.ai.canyonroad.agentsh.SysExt` shows `state = running`), expect `esf true` instead — verify against actual launchd state, and use the unit fixtures as the source of truth for the broken path.

- [ ] **Step 4: Push branch and open PR**

```bash
git push -u origin issue-441-esf-liveness-detect
gh pr create --title "fix(#441): detect gates esf on sysext liveness, not activation" --body "$(cat <<'EOF'
Fixes #441.

`detect` (and the tier/status surfaces) reported `esf ✓` from `systemextensionsctl list` "activated enabled" alone, so an activated-but-not-running extension (AMFI-blocked #436, launchd-throttled, crash-looping) scored 90 while enforcing nothing.

- New `darwin.CheckSysExtLiveness()`: activation via per-line `systemextensionsctl` parse (+ team ID), liveness via `launchctl print system/<TeamID>.ai.canyonroad.agentsh.SysExt` requiring `state = running`; fail-closed on probe failure.
- `caps["esf"]` = running (honest mode selection + score), new `caps["esf_activated"]` preserves the distinction; esf backends carry the liveness detail.
- Reason-sensitive tips: `OS_REASON_EXEC` → AMFI/provisionprofile guidance; not-running / unverifiable → launchctl diagnostics.
- `TierEnterprise` and `SysExtStatus.Running` now require real liveness; `SysExtStatus` gains `State`/`LastExit`.

Spec: docs/superpowers/specs/2026-08-05-issue-441-esf-liveness-detect-design.md

🤖 Generated with [Claude Code](https://claude.com/claude-code)
EOF
)"
```

- [ ] **Step 5: File the follow-up issue (spec out-of-scope item)**

```bash
gh issue create --title "server: report/warn when esf mode is active but the system extension never connects to the policy socket" --body "$(cat <<'EOF'
Follow-up from #441 (spec: docs/superpowers/specs/2026-08-05-issue-441-esf-liveness-detect-design.md).

#441 makes `detect` verify the sysext *process* is running via launchd state. The remaining gap is functional: the extension is a client that dials into the server's policy socket, so only a running server can know whether enforcement is actually wired end-to-end. The server should surface (log warning and/or status endpoint) when it runs in esf mode and no extension connection is established within a grace period.

🤖 Generated with [Claude Code](https://claude.com/claude-code)
EOF
)"
```

- [ ] **Step 6: Watch CI, then hand off**

Run: `gh pr checks --watch`
Expected: all green. Per the user's standard workflow: green CI → squash merge → watch main. Confirm with the user before merging.
