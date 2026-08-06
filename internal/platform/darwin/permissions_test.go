//go:build darwin

package darwin

import (
	"strings"
	"testing"
)

func TestPermissionTier_String(t *testing.T) {
	tests := []struct {
		tier PermissionTier
		want string
	}{
		{TierEnterprise, "enterprise"},
		{TierStandard, "standard"},
		{TierMinimal, "minimal"},
		{PermissionTier(99), "unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			if got := tt.tier.String(); got != tt.want {
				t.Errorf("String() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestPermissionTier_SecurityScore(t *testing.T) {
	tests := []struct {
		tier PermissionTier
		want int
	}{
		{TierEnterprise, 95},
		{TierStandard, 50},
		{TierMinimal, 10},
		{PermissionTier(99), 0},
	}

	for _, tt := range tests {
		t.Run(tt.tier.String(), func(t *testing.T) {
			if got := tt.tier.SecurityScore(); got != tt.want {
				t.Errorf("SecurityScore() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestPermissions_computeTier(t *testing.T) {
	tests := []struct {
		name string
		perm Permissions
		want PermissionTier
	}{
		{
			name: "enterprise with system extension",
			perm: Permissions{
				HasSystemExtension: true,
			},
			want: TierEnterprise,
		},
		{
			name: "standard with root and pf",
			perm: Permissions{
				HasRootAccess: true,
				CanUsePF:      true,
			},
			want: TierStandard,
		},
		{
			name: "minimal with nothing",
			perm: Permissions{},
			want: TierMinimal,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.perm.computeTier()
			if tt.perm.Tier != tt.want {
				t.Errorf("computeTier() = %v, want %v", tt.perm.Tier, tt.want)
			}
		})
	}
}

func TestPermissions_AvailableFeatures(t *testing.T) {
	tests := []struct {
		tier         PermissionTier
		wantContains []string
	}{
		{
			tier:         TierEnterprise,
			wantContains: []string{"ESF", "NE", "tls_inspection"},
		},
		{
			tier:         TierStandard,
			wantContains: []string{"FSEvents", "pf"},
		},
		{
			tier:         TierMinimal,
			wantContains: []string{"command_logging"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.tier.String(), func(t *testing.T) {
			p := &Permissions{Tier: tt.tier}
			features := p.AvailableFeatures()
			featureStr := strings.Join(features, " ")
			for _, want := range tt.wantContains {
				if !strings.Contains(featureStr, want) {
					t.Errorf("AvailableFeatures() missing %q, got %v", want, features)
				}
			}
		})
	}
}

func TestPermissions_DisabledFeatures(t *testing.T) {
	tests := []struct {
		tier      PermissionTier
		wantEmpty bool
	}{
		{TierEnterprise, true},
		{TierStandard, false},
		{TierMinimal, false},
	}

	for _, tt := range tests {
		t.Run(tt.tier.String(), func(t *testing.T) {
			p := &Permissions{Tier: tt.tier}
			features := p.DisabledFeatures()
			if tt.wantEmpty && len(features) != 0 {
				t.Errorf("DisabledFeatures() = %v, want empty", features)
			}
			if !tt.wantEmpty && len(features) == 0 {
				t.Error("DisabledFeatures() is empty, want non-empty")
			}
		})
	}
}

func TestPermissions_computeMissingPermissions(t *testing.T) {
	p := &Permissions{
		HasSystemExtension: false,
		HasRootAccess:      false,
		HasFullDiskAccess:  false,
	}
	p.computeMissingPermissions()

	if len(p.MissingPermissions) == 0 {
		t.Error("computeMissingPermissions() returned empty list")
	}

	names := make(map[string]bool)
	for _, mp := range p.MissingPermissions {
		names[mp.Name] = true
	}

	expected := []string{"System Extension", "Root Access", "Full Disk Access"}
	for _, name := range expected {
		if !names[name] {
			t.Errorf("Missing expected permission: %s", name)
		}
	}

	// Verify system extension tip mentions the app bundle
	for _, mp := range p.MissingPermissions {
		if mp.Name == "System Extension" {
			if !strings.Contains(mp.HowToEnable, "app bundle") {
				t.Errorf("System Extension HowToEnable should mention app bundle, got: %s", mp.HowToEnable)
			}
		}
	}
}

func TestPermissions_LogStatus(t *testing.T) {
	p := &Permissions{
		HasSystemExtension: false,
		HasRootAccess:      true,
		CanUsePF:           true,
		HasFSEvents:        true,
		Tier:               TierStandard,
	}
	p.computeMissingPermissions()

	status := p.LogStatus()

	// Check for key sections
	if !strings.Contains(status, "macOS Permission Status") {
		t.Error("LogStatus() missing header")
	}
	if !strings.Contains(status, "Operating Tier") {
		t.Error("LogStatus() missing tier info")
	}
	if !strings.Contains(status, "Feature Availability") {
		t.Error("LogStatus() missing feature availability")
	}
	if !strings.Contains(status, "System Extension") {
		t.Error("LogStatus() missing System Extension section")
	}
}

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

func TestDetectPermissions_SysExtLivenessMapping(t *testing.T) {
	restore := runLivenessCommand
	defer func() { runLivenessCommand = restore }()

	cases := []struct {
		name          string
		launchctlOut  string
		wantHasSysExt bool
	}{
		{"activated but spawn scheduled must not count", launchdSpawnScheduled, false},
		{"activated and running counts", launchdRunning, true},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			runLivenessCommand = func(name string, args ...string) (string, error) {
				if name == "systemextensionsctl" {
					return sysextListBoth, nil
				}
				return tt.launchctlOut, nil
			}
			p := DetectPermissions()
			if p.HasSystemExtension != tt.wantHasSysExt {
				t.Errorf("HasSystemExtension = %v, want %v (the #441 mapping)", p.HasSystemExtension, tt.wantHasSysExt)
			}
			if !p.SysExtActivated {
				t.Error("SysExtActivated = false, want true")
			}
		})
	}
}

func TestComputeMissingPermissions_ProbeFailedBranch(t *testing.T) {
	p := &Permissions{
		SysExtProbeFailed: true,
		SysExtDetail:      "activated but liveness could not be verified (no state in launchctl output)",
	}
	p.computeMissingPermissions()
	mp := findMissing(t, p, "System Extension")
	if !strings.Contains(mp.Impact, "could not be verified") {
		t.Errorf("Impact = %q, want unverifiable wording, not a not-running claim", mp.Impact)
	}
	if !strings.Contains(mp.HowToEnable, p.SysExtDetail) {
		t.Errorf("HowToEnable = %q, want embedded detail", mp.HowToEnable)
	}
	if strings.Contains(mp.HowToEnable, "Install the agentsh macOS app bundle") {
		t.Errorf("HowToEnable = %q, must not show install guidance on probe failure", mp.HowToEnable)
	}
}
