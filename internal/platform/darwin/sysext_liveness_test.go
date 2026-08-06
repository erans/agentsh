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

// A hypothetical prefix-sibling extension: must NOT match our exact token.
const sysextListPrefixSibling = `1 extension(s)
--- com.apple.system_extension.endpoint_security
enabled	active	teamID	bundleID (version)	name	[state]
*	*	WCKWMMKJ35	ai.canyonroad.agentsh.SysExtBeta (1.0/2)	ai.canyonroad.agentsh.SysExtBeta	[activated enabled]
`

// Prefix-sibling row ABOVE the real row: real row must still win with its team ID.
const sysextListSiblingBeforeReal = `2 extension(s)
--- com.apple.system_extension.endpoint_security
enabled	active	teamID	bundleID (version)	name	[state]
*	*	WCKWMMKJ35	ai.canyonroad.agentsh.SysExtBeta (1.0/2)	ai.canyonroad.agentsh.SysExtBeta	[activated enabled]
*	*	WCKWMMKJ35	ai.canyonroad.agentsh.SysExt (1.0/14)	ai.canyonroad.agentsh.SysExt	[activated enabled]
`

// Blank team-ID column (possible under `systemextensionsctl developer on`):
// consecutive tabs mean strings.Fields sees `*`, `*`, then the bundle ID, so
// the "*" guard must keep teamID empty rather than returning the active-
// column marker.
const sysextListBlankTeamID = `1 extension(s)
--- com.apple.system_extension.endpoint_security
enabled	active	teamID	bundleID (version)	name	[state]
*	*		ai.canyonroad.agentsh.SysExt (1.0/14)	ai.canyonroad.agentsh.SysExt	[activated enabled]
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
		{"prefix sibling must not match", sysextListPrefixSibling, false, ""},
		{"sibling row before real row", sysextListSiblingBeforeReal, true, "WCKWMMKJ35"},
		{"blank team ID column yields empty not asterisk", sysextListBlankTeamID, true, ""},
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
		{"exit code 0 suppressed", "system/x = {\n\tstate = not running\n\tlast exit code = 0\n}\n", "not running", ""},
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
