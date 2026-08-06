//go:build darwin

package capabilities

import (
	"strings"
	"testing"

	darwin "github.com/agentsh/agentsh/internal/platform/darwin"
)

func TestSelectDarwinMode(t *testing.T) {
	hasMacwrap := checkMacwrap()

	tests := []struct {
		name         string
		caps         map[string]any
		wantMode     string
		wantScore    int
		needsMacwrap bool
	}{
		{"esf wins", map[string]any{"esf": true, "lima_available": true}, "esf", 90, false},
		{"lima second", map[string]any{"esf": false, "lima_available": true}, "lima", 85, false},
		{"dynamic seatbelt", map[string]any{"esf": false, "lima_available": false}, "dynamic-seatbelt", 65, true},
		{"sandbox-exec fallback", map[string]any{"esf": false, "lima_available": false}, "sandbox-exec", 60, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.needsMacwrap && !hasMacwrap {
				t.Skip("agentsh-macwrap not in PATH")
			}
			if !tt.needsMacwrap && hasMacwrap {
				if tt.wantMode == "sandbox-exec" {
					t.Skip("macwrap is in PATH, this tests the no-macwrap path")
				}
			}
			mode, score := selectDarwinMode(tt.caps)
			if mode != tt.wantMode {
				t.Errorf("selectDarwinMode() mode = %q, want %q", mode, tt.wantMode)
			}
			if score != tt.wantScore {
				t.Errorf("selectDarwinMode() score = %d, want %d", score, tt.wantScore)
			}
		})
	}
}

func TestDetect_Darwin(t *testing.T) {
	result, err := Detect()
	if err != nil {
		t.Fatalf("Detect() error: %v", err)
	}

	if result.Platform != "darwin" {
		t.Errorf("Platform = %q, want darwin", result.Platform)
	}

	// Should have macOS-specific capability keys
	expectedKeys := []string{"sandbox_exec", "esf", "esf_activated", "esf_probe_failed", "network_extension"}
	for _, key := range expectedKeys {
		if _, exists := result.Capabilities[key]; !exists {
			t.Errorf("Capabilities missing key %q", key)
		}
	}

	// sandbox_exec should always be true (built into macOS)
	if se, ok := result.Capabilities["sandbox_exec"].(bool); !ok || !se {
		t.Error("sandbox_exec should be true")
	}
}

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

func TestDarwinCaps_LivenessMapping(t *testing.T) {
	tests := []struct {
		name     string
		liveness darwin.SysExtLiveness
		wantESF  bool
		wantAct  bool
		wantPF   bool
	}{
		{"activated but not running is the #441 case", darwin.SysExtLiveness{Activated: true, Running: false}, false, true, false},
		{"running implies esf", darwin.SysExtLiveness{Activated: true, Running: true}, true, true, false},
		{"not activated", darwin.SysExtLiveness{}, false, false, false},
		{"probe failure surfaces", darwin.SysExtLiveness{Activated: true, ProbeFailed: true}, false, true, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			caps := darwinCaps(tt.liveness)
			if got := caps["esf"].(bool); got != tt.wantESF {
				t.Errorf("esf = %v, want %v", got, tt.wantESF)
			}
			if got := caps["esf_activated"].(bool); got != tt.wantAct {
				t.Errorf("esf_activated = %v, want %v", got, tt.wantAct)
			}
			if got := caps["esf_probe_failed"].(bool); got != tt.wantPF {
				t.Errorf("esf_probe_failed = %v, want %v", got, tt.wantPF)
			}
		})
	}
}
