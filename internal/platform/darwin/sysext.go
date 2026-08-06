//go:build darwin

// Package darwin provides the macOS platform implementation for agentsh.
package darwin

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// SysExtStatus represents the state of the System Extension.
type SysExtStatus struct {
	Installed   bool   `json:"installed"`              // true once systemextensionsctl reports the extension activated; does not imply Running
	Running     bool   `json:"running"`                // true only on positive proof the launchd service state is "running"
	ProbeFailed bool   `json:"probe_failed,omitempty"` // a liveness probe command failed or its output was unparseable
	State       string `json:"state,omitempty"`        // raw launchd state ("running", "spawn scheduled", ...); "" if unknown
	LastExit    string `json:"last_exit,omitempty"`    // last recorded launchd exit; suppressed while Running is true
	Version     string `json:"version,omitempty"`
	BundleID    string `json:"bundle_id,omitempty"`
	ExtensionID string `json:"extension_id,omitempty"`
	Error       string `json:"error,omitempty"`
}

// SysExtManager manages the agentsh System Extension lifecycle.
type SysExtManager struct {
	bundlePath string
	bundleID   string
}

// NewSysExtManager creates a new System Extension manager.
func NewSysExtManager() *SysExtManager {
	// Find the app bundle - either we're running from it or it's adjacent
	execPath, _ := os.Executable()
	bundlePath := findAppBundle(execPath)

	return &SysExtManager{
		bundlePath: bundlePath,
		bundleID:   sysExtBundleID,
	}
}

// findAppBundle locates the AgentSH.app bundle.
func findAppBundle(execPath string) string {
	// If running from within .app bundle
	if idx := strings.Index(execPath, ".app/"); idx >= 0 {
		return execPath[:idx+4]
	}

	// Check common locations
	candidates := []string{
		"/Applications/AgentSH.app",
		filepath.Join(filepath.Dir(execPath), "AgentSH.app"),
		filepath.Join(filepath.Dir(execPath), "..", "AgentSH.app"),
	}

	for _, c := range candidates {
		if info, err := os.Stat(c); err == nil && info.IsDir() {
			return c
		}
	}

	return ""
}

// Status returns the current System Extension status.
// This method never returns an error - any errors are reported via status.Error.
//
// The m.bundlePath == "" early return below is a bundle-presence
// precondition, not a liveness statement: an extension orphaned by app
// deletion persists in /Library/SystemExtensions and would still report
// Installed=false here because the .app bundle is gone. Surfaces that need
// the ground truth regardless of the app bundle call CheckSysExtLiveness
// directly and do not pass through this gate.
//
// Installed reflects activation (systemextensionsctl), not readiness: an
// extension still awaiting user approval in System Settings reports
// Installed=false. LastExit is suppressed while Running is true, since a
// historical exit on an otherwise healthy running service is misleading.
func (m *SysExtManager) Status() (*SysExtStatus, error) {
	status := &SysExtStatus{
		BundleID: m.bundleID,
	}

	if m.bundlePath == "" {
		status.Error = "AgentSH.app bundle not found"
		return status, nil
	}

	liveness := CheckSysExtLiveness()
	status.Installed = liveness.Activated
	status.Running = liveness.Running
	status.ProbeFailed = liveness.ProbeFailed
	status.State = liveness.State
	if !liveness.Running {
		// A historical exit on a healthy running service is misleading in
		// status output; surface it only when the process is not up.
		status.LastExit = liveness.LastExit
	}
	if liveness.ProbeFailed || (liveness.Activated && !liveness.Running) {
		status.Error = liveness.Detail
	}

	return status, nil
}

// Install requests installation of the System Extension.
func (m *SysExtManager) Install() error {
	if m.bundlePath == "" {
		return fmt.Errorf("AgentSH.app bundle not found; install it first")
	}

	extPath := filepath.Join(m.bundlePath, "Contents", "Library", "SystemExtensions",
		m.bundleID+".systemextension")

	if _, err := os.Stat(extPath); err != nil {
		return fmt.Errorf("System Extension not found at %s", extPath)
	}

	return fmt.Errorf("not implemented: use Activate() instead")
}

// Activate submits an activation request for the system extension via
// OSSystemExtensionManager. Requires CGO and the system-extension.install
// entitlement on the calling binary.
func (m *SysExtManager) Activate() (ActivateResult, error) {
	if m.bundlePath == "" {
		return ActivateFailed, fmt.Errorf("AgentSH.app bundle not found; install it first")
	}
	return activateExtension()
}

// Uninstall removes the System Extension.
func (m *SysExtManager) Uninstall() error {
	return fmt.Errorf("not implemented: requires Swift integration")
}
