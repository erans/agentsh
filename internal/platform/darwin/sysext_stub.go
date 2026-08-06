//go:build !darwin

// Package darwin provides stubs for non-macOS platforms.
package darwin

import "fmt"

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
type SysExtManager struct{}

// NewSysExtManager creates a new System Extension manager.
func NewSysExtManager() *SysExtManager {
	return &SysExtManager{}
}

// Status returns the current System Extension status.
func (m *SysExtManager) Status() (*SysExtStatus, error) {
	return &SysExtStatus{Error: "System Extensions are only available on macOS"}, nil
}

// ActivateResult represents the outcome of a system extension activation request.
type ActivateResult int

const (
	ActivateOK            ActivateResult = 0
	ActivateNeedsApproval ActivateResult = 1
	ActivateFailed        ActivateResult = -1
)

// Install requests installation of the System Extension.
func (m *SysExtManager) Install() error {
	return fmt.Errorf("System Extensions are only available on macOS")
}

// Activate submits an activation request for the system extension.
func (m *SysExtManager) Activate() (ActivateResult, error) {
	return ActivateFailed, fmt.Errorf("System Extensions are only available on macOS")
}

// Uninstall removes the System Extension.
func (m *SysExtManager) Uninstall() error {
	return fmt.Errorf("System Extensions are only available on macOS")
}
