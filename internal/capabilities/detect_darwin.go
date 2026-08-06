//go:build darwin

package capabilities

import (
	"os/exec"

	"github.com/agentsh/agentsh/internal/platform/darwin"
)

func buildDarwinDomains(caps map[string]any, esfDetail string) []ProtectionDomain {
	esf, _ := caps["esf"].(bool)
	networkExt, _ := caps["network_extension"].(bool)
	hasMacwrap := checkMacwrap()

	macwrapDetail := "not found"
	if hasMacwrap {
		macwrapDetail = "dynamic seatbelt"
	}

	return []ProtectionDomain{
		{
			Name: "File Protection", Weight: WeightFileProtection,
			Backends: []DetectedBackend{
				{Name: "esf", Available: esf, Detail: esfDetail, Description: "Endpoint Security Framework", CheckMethod: "sysext"},
			},
		},
		{
			Name: "Command Control", Weight: WeightCommandControl,
			Backends: []DetectedBackend{
				{Name: "esf", Available: esf, Detail: esfDetail, Description: "process execution control", CheckMethod: "sysext"},
				{Name: "dynamic-seatbelt", Available: hasMacwrap, Detail: macwrapDetail, Description: "policy-driven exec restriction", CheckMethod: "binary"},
				{Name: "sandbox-exec", Available: true, Detail: "", Description: "macOS sandbox", CheckMethod: "builtin"},
			},
			Active: func() string {
				if esf {
					return "esf"
				}
				if hasMacwrap {
					return "dynamic-seatbelt"
				}
				return "sandbox-exec"
			}(),
		},
		{
			Name: "Network", Weight: WeightNetwork,
			Backends: []DetectedBackend{
				{Name: "network-extension", Available: networkExt, Detail: "", Description: "network filtering", CheckMethod: "entitlement"},
			},
		},
		{
			Name: "Resource Limits", Weight: WeightResourceLimits,
			Backends: []DetectedBackend{
				{Name: "launchd-limits", Available: true, Detail: "", Description: "launchd resource limits", CheckMethod: "builtin"},
			},
			Active: "launchd-limits",
		},
		{
			Name: "Isolation", Weight: WeightIsolation,
			Backends: []DetectedBackend{
				{Name: "dynamic-seatbelt", Available: hasMacwrap, Detail: macwrapDetail, Description: "deny-default sandbox", CheckMethod: "binary"},
				{Name: "sandbox-exec", Available: true, Detail: "", Description: "process isolation", CheckMethod: "builtin"},
			},
			Active: func() string {
				if hasMacwrap {
					return "dynamic-seatbelt"
				}
				return "sandbox-exec"
			}(),
		},
	}
}

// darwinCaps maps sysext liveness onto the capability map. Kept pure so the
// esf/esf_activated mapping — the exact regression #441 fixed — is testable
// without subprocesses.
func darwinCaps(l darwin.SysExtLiveness) map[string]any {
	return map[string]any{
		"sandbox_exec":      true,
		"esf":               l.Running,
		"esf_activated":     l.Activated,
		"esf_probe_failed":  l.ProbeFailed,
		"network_extension": checkNetworkExtension(),
		"lima_available":    checkLima(),
	}
}

// Detect runs platform-specific detection and returns unified result.
func Detect() (*DetectResult, error) {
	liveness := darwin.CheckSysExtLiveness()
	caps := darwinCaps(liveness)

	mode, _ := selectDarwinMode(caps)
	domains := buildDarwinDomains(caps, liveness.Detail)
	score := ComputeScore(domains)

	var available, unavailable []string
	for _, d := range domains {
		for _, b := range d.Backends {
			if b.Available {
				available = append(available, b.Name)
			} else {
				unavailable = append(unavailable, b.Name)
			}
		}
	}

	tips := GenerateTipsFromDomains(domains)

	return &DetectResult{
		Platform:        "darwin",
		SecurityMode:    mode,
		ProtectionScore: score,
		Domains:         domains,
		Capabilities:    caps,
		Summary:         DetectSummary{Available: available, Unavailable: unavailable},
		Tips:            tips,
	}, nil
}

func checkNetworkExtension() bool {
	// Network Extension is available if app is properly entitled
	// For CLI detection, assume false
	return false
}

func checkLima() bool {
	// Check if limactl is available
	_, err := exec.LookPath("limactl")
	return err == nil
}

func checkMacwrap() bool {
	_, err := exec.LookPath("agentsh-macwrap")
	return err == nil
}

func selectDarwinMode(caps map[string]any) (string, int) {
	if esf, _ := caps["esf"].(bool); esf {
		return "esf", 90
	}
	if lima, _ := caps["lima_available"].(bool); lima {
		return "lima", 85
	}
	hasMacwrap := checkMacwrap()
	if hasMacwrap {
		return "dynamic-seatbelt", 65
	}
	return "sandbox-exec", 60
}
