//go:build darwin

package darwin

import (
	"errors"
	"strings"
	"testing"
)

// state = running while launchd still remembers an older crash (runs > 1).
const launchdRunningAfterCrash = `system/WCKWMMKJ35.ai.canyonroad.agentsh.SysExt = {
	active count = 1
	state = running

	pid = 4242
	runs = 199773
	last exit code = 1
}
`

func TestNewSysExtManager(t *testing.T) {
	m := NewSysExtManager()
	if m == nil {
		t.Fatal("NewSysExtManager() returned nil")
	}
	if m.bundleID != "ai.canyonroad.agentsh.SysExt" {
		t.Errorf("bundleID = %q, want %q", m.bundleID, "ai.canyonroad.agentsh.SysExt")
	}
}

func TestSysExtManager_Status(t *testing.T) {
	m := NewSysExtManager()

	status, err := m.Status()
	if err != nil {
		t.Fatalf("Status() error = %v, want nil (errors go in status.Error)", err)
	}
	if status == nil {
		t.Fatal("Status() returned nil status")
	}
	if status.BundleID != "ai.canyonroad.agentsh.SysExt" {
		t.Errorf("BundleID = %q, want %q", status.BundleID, "ai.canyonroad.agentsh.SysExt")
	}
}

func TestSysExtManager_Status_NeverReturnsError(t *testing.T) {
	// The Status method should never return an error - errors go in status.Error field
	m := &SysExtManager{
		bundlePath: "",
		bundleID:   "ai.canyonroad.agentsh.SysExt",
	}

	status, err := m.Status()
	if err != nil {
		t.Fatalf("Status() returned error %v, want nil (errors should be in status.Error)", err)
	}
	if status.Error == "" {
		t.Error("Expected status.Error to contain error message for missing bundle")
	}
}

func TestSysExtManager_Install_NoBundleError(t *testing.T) {
	m := &SysExtManager{
		bundlePath: "",
		bundleID:   "ai.canyonroad.agentsh.SysExt",
	}

	err := m.Install()
	if err == nil {
		t.Fatal("Install() should error when bundle not found")
	}
}

func TestSysExtManager_Uninstall_NotImplemented(t *testing.T) {
	m := NewSysExtManager()

	err := m.Uninstall()
	if err == nil {
		t.Fatal("Uninstall() should return error (not implemented)")
	}
}

func TestFindAppBundle_FromWithinBundle(t *testing.T) {
	tests := []struct {
		name     string
		execPath string
		want     string
	}{
		{
			name:     "from within app bundle Contents/MacOS",
			execPath: "/Applications/AgentSH.app/Contents/MacOS/agentsh",
			want:     "/Applications/AgentSH.app",
		},
		{
			name:     "from within app bundle nested",
			execPath: "/some/path/AgentSH.app/Contents/Resources/bin/tool",
			want:     "/some/path/AgentSH.app",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := findAppBundle(tt.execPath)
			if got != tt.want {
				t.Errorf("findAppBundle(%q) = %q, want %q", tt.execPath, got, tt.want)
			}
		})
	}
}

func TestSysExtStatus_JSONTags(t *testing.T) {
	// Verify that SysExtStatus has the expected structure
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

	// Just verify the struct can be created with all fields
	if !status.Installed {
		t.Error("Installed should be true")
	}
	if !status.Running {
		t.Error("Running should be true")
	}
	if status.Version != "1.0.0" {
		t.Error("Version mismatch")
	}
	if status.BundleID != "ai.canyonroad.agentsh.SysExt" {
		t.Error("BundleID mismatch")
	}
	if status.ExtensionID != "ext-123" {
		t.Error("ExtensionID mismatch")
	}
	if status.State != "running" {
		t.Error("State mismatch")
	}
}

func TestSysExtManager_Status_LivenessMapping(t *testing.T) {
	restore := runLivenessCommand
	defer func() { runLivenessCommand = restore }()

	m := &SysExtManager{bundlePath: "/tmp", bundleID: "ai.canyonroad.agentsh.SysExt"}

	tests := []struct {
		name            string
		sysextFails     bool   // true: systemextensionsctl itself fails, so Activated stays false
		launchctlOut    string // "" injects a launchctl command failure (ignored when sysextFails)
		wantInstalled   bool
		wantRunning     bool
		wantErrSub      string // "" means Error must be empty
		wantLastExit    string
		wantState       string
		wantProbeFailed bool
	}{
		{"activated but spawn scheduled", false, launchdSpawnScheduled, true, false, "activated but not running", "exit code 1", "spawn scheduled", false},
		{"activated and running suppresses historical exit", false, launchdRunningAfterCrash, true, true, "", "", "running", false},
		{"launchctl failure -> probe failed", false, "", true, false, "could not be verified", "", "", true},
		// Activated=false here (systemextensionsctl itself failed), so this is
		// the only row where the `liveness.ProbeFailed ||` half of the Error
		// condition is load-bearing: the `Activated && !Running` half alone
		// would not fire since Activated is false.
		{"systemextensionsctl failure -> not activated, probe failed, error still populated", true, "", false, false, "could not be verified", "", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runLivenessCommand = func(name string, args ...string) (string, error) {
				if name == "systemextensionsctl" {
					if tt.sysextFails {
						return "", errors.New("no such process")
					}
					return sysextListBoth, nil
				}
				if tt.launchctlOut == "" {
					return "", errors.New("Could not find service")
				}
				return tt.launchctlOut, nil
			}
			status, err := m.Status()
			if err != nil {
				t.Fatalf("Status() error = %v", err)
			}
			if status.Installed != tt.wantInstalled {
				t.Errorf("Installed = %v, want %v", status.Installed, tt.wantInstalled)
			}
			if status.Running != tt.wantRunning {
				t.Errorf("Running = %v, want %v", status.Running, tt.wantRunning)
			}
			if tt.wantErrSub == "" && status.Error != "" {
				t.Errorf("Error = %q, want empty", status.Error)
			}
			if tt.wantErrSub != "" && !strings.Contains(status.Error, tt.wantErrSub) {
				t.Errorf("Error = %q, want substring %q", status.Error, tt.wantErrSub)
			}
			if status.LastExit != tt.wantLastExit {
				t.Errorf("LastExit = %q, want %q (historical exits are suppressed while running)", status.LastExit, tt.wantLastExit)
			}
			if status.State != tt.wantState {
				t.Errorf("State = %q, want %q", status.State, tt.wantState)
			}
			if status.ProbeFailed != tt.wantProbeFailed {
				t.Errorf("ProbeFailed = %v, want %v", status.ProbeFailed, tt.wantProbeFailed)
			}
		})
	}
}
