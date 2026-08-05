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
