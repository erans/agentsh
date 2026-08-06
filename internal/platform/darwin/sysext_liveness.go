//go:build darwin

package darwin

import (
	"context"
	"errors"
	"fmt"
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
// var so tests can inject fixture output (tests swapping it must not use
// t.Parallel()). Captured stderr is collapsed to a single line before being
// folded into the returned error, so probe failures carry the tool's actual
// message while Detail stays a one-liner.
var runLivenessCommand = func(name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), livenessCmdTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, name, args...).Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			if msg := strings.Join(strings.Fields(string(exitErr.Stderr)), " "); msg != "" {
				err = fmt.Errorf("%w: %s", err, msg)
			}
		}
	}
	return string(out), err
}

// parseSysExtList scans systemextensionsctl list output line by line for an
// "activated enabled" row whose fields contain the exact bundle-ID token
// (the row repeats the bundle ID as its display name, so the team ID is the
// field immediately preceding the FIRST occurrence; matching the last would
// return the version column). Exact-token matching prevents both a different
// extension's "activated enabled" row and a prefix-sibling bundle ID (e.g. a
// future ...SysExtBeta) from satisfying the check, and scanning continues
// past rows that yield no team ID so a transient extra row cannot mask a
// healthy one. A "*" in the preceding field is the `active` column marker of
// a blank team-ID row, not a team ID.
func parseSysExtList(output string) (activated bool, teamID string) {
	for _, line := range strings.Split(output, "\n") {
		if !strings.Contains(line, "activated enabled") {
			continue
		}
		fields := strings.Fields(line)
		for i, f := range fields {
			if f != sysExtBundleID {
				continue
			}
			activated = true
			if i > 0 && fields[i-1] != "*" {
				teamID = fields[i-1]
			}
			break
		}
		if activated && teamID != "" {
			return true, teamID
		}
	}
	return activated, teamID
}

// parseLaunchdState extracts the service-level state and last-exit info from
// `launchctl print system/<label>` output. Only the FIRST "state =" line is
// the service state: nested sub-sections (event triggers, XPC endpoints)
// contain their own "state = active" lines. "last exit reason" (present on
// exec-level failures like AMFI rejection, per #436) is preferred over
// "last exit code"; a code of "(never exited)" or "0" is not an exit signal.
// The reported last exit is the most recent exit launchd ever recorded for
// the service and may predate the current state.
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
