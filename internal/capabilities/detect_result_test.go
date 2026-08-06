//go:build linux || darwin || windows

package capabilities

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestDetectResult_JSON(t *testing.T) {
	result := &DetectResult{
		Platform:        "linux",
		SecurityMode:    "landlock-only",
		ProtectionScore: 80,
		Capabilities: map[string]any{
			"landlock":     true,
			"landlock_abi": 4,
		},
		Summary: DetectSummary{
			Available:   []string{"landlock"},
			Unavailable: []string{"seccomp"},
		},
		Tips: []Tip{
			{
				Feature: "seccomp",
				Status:  "unavailable",
				Impact:  "Syscall filtering disabled",
				Action:  "Run on host",
			},
		},
	}

	json, err := result.JSON()
	if err != nil {
		t.Fatalf("JSON() error: %v", err)
	}
	if len(json) == 0 {
		t.Error("JSON() returned empty")
	}
}

func TestDetectResult_YAML(t *testing.T) {
	result := &DetectResult{
		Platform:        "linux",
		SecurityMode:    "minimal",
		ProtectionScore: 50,
		Capabilities:    map[string]any{},
		Summary:         DetectSummary{},
		Tips:            []Tip{},
	}

	yaml, err := result.YAML()
	if err != nil {
		t.Fatalf("YAML() error: %v", err)
	}
	if len(yaml) == 0 {
		t.Error("YAML() returned empty")
	}
}

func TestDetectResult_Table(t *testing.T) {
	result := &DetectResult{
		Platform:        "linux",
		SecurityMode:    "landlock-only",
		ProtectionScore: 80,
		Capabilities: map[string]any{
			"seccomp":          false,
			"landlock":         true,
			"landlock_abi":     4,
			"landlock_network": true,
			"fuse":             false,
		},
		Summary: DetectSummary{
			Available:   []string{"landlock", "landlock_network"},
			Unavailable: []string{"seccomp", "fuse"},
		},
		Tips: []Tip{
			{
				Feature: "fuse",
				Status:  "unavailable",
				Impact:  "Fine-grained filesystem control disabled",
				Action:  "Install FUSE3: pacman -S fuse3",
			},
		},
	}

	table := result.Table()
	if len(table) == 0 {
		t.Error("Table() returned empty")
	}
	// Check key elements are present
	if !strings.Contains(table, "linux") {
		t.Error("Table() missing platform")
	}
	if !strings.Contains(table, "landlock") {
		t.Error("Table() missing landlock")
	}
	if !strings.Contains(table, "fuse") {
		t.Error("Table() missing tip about fuse")
	}
}

func TestComputeScore_AllAvailable(t *testing.T) {
	domains := []ProtectionDomain{
		{Name: "File Protection", Weight: 25, Backends: []DetectedBackend{{Available: true}}},
		{Name: "Command Control", Weight: 25, Backends: []DetectedBackend{{Available: true}}},
		{Name: "Network", Weight: 20, Backends: []DetectedBackend{{Available: true}}},
		{Name: "Resource Limits", Weight: 15, Backends: []DetectedBackend{{Available: true}}},
		{Name: "Isolation", Weight: 15, Backends: []DetectedBackend{{Available: true}}},
	}
	assert.Equal(t, 100, ComputeScore(domains))
}

func TestComputeScore_NoneAvailable(t *testing.T) {
	domains := []ProtectionDomain{
		{Name: "File Protection", Weight: 25, Backends: []DetectedBackend{{Available: false}}},
		{Name: "Command Control", Weight: 25, Backends: []DetectedBackend{{Available: false}}},
		{Name: "Network", Weight: 20, Backends: []DetectedBackend{{Available: false}}},
		{Name: "Resource Limits", Weight: 15, Backends: []DetectedBackend{{Available: false}}},
		{Name: "Isolation", Weight: 15, Backends: []DetectedBackend{{Available: false}}},
	}
	assert.Equal(t, 0, ComputeScore(domains))
}

func TestComputeScore_Partial(t *testing.T) {
	tests := []struct {
		name     string
		file     bool
		cmd      bool
		net      bool
		res      bool
		iso      bool
		expected int
	}{
		{"File+Command only", true, true, false, false, false, 50},
		{"Network+Resource+Isolation", false, false, true, true, true, 50},
		{"All except Network", true, true, false, true, true, 80},
		{"Only Isolation", false, false, false, false, true, 15},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			domains := []ProtectionDomain{
				{Weight: 25, Backends: []DetectedBackend{{Available: tt.file}}},
				{Weight: 25, Backends: []DetectedBackend{{Available: tt.cmd}}},
				{Weight: 20, Backends: []DetectedBackend{{Available: tt.net}}},
				{Weight: 15, Backends: []DetectedBackend{{Available: tt.res}}},
				{Weight: 15, Backends: []DetectedBackend{{Available: tt.iso}}},
			}
			assert.Equal(t, tt.expected, ComputeScore(domains))
		})
	}
}

func TestComputeScore_SingleBackendInMultiBackendDomain(t *testing.T) {
	// One backend available in a multi-backend domain should still score full weight
	domains := []ProtectionDomain{
		{Weight: 25, Backends: []DetectedBackend{
			{Name: "fuse", Available: false},
			{Name: "landlock", Available: true},
			{Name: "seccomp-notify", Available: false},
		}},
	}
	assert.Equal(t, 25, ComputeScore(domains))
}

func TestComputeScore_SetsScoreOnDomains(t *testing.T) {
	domains := []ProtectionDomain{
		{Weight: 25, Backends: []DetectedBackend{{Available: true}}},
		{Weight: 20, Backends: []DetectedBackend{{Available: false}}},
	}
	ComputeScore(domains)
	assert.Equal(t, 25, domains[0].Score)
	assert.Equal(t, 0, domains[1].Score)
}

func TestTableFormat_Grouped(t *testing.T) {
	result := &DetectResult{
		Platform:        "linux",
		SecurityMode:    "full",
		ProtectionScore: 85,
		Domains: []ProtectionDomain{
			{Name: "File Protection", Weight: 25, Score: 25, Backends: []DetectedBackend{
				{Name: "fuse", Available: true, Detail: "fusermount3", Description: "file interception"},
				{Name: "landlock", Available: false, Detail: "not available", Description: "kernel path restrictions"},
			}, Active: "fuse"},
			{Name: "Network", Weight: 20, Score: 0, Backends: []DetectedBackend{
				{Name: "ebpf", Available: false, Detail: "EPERM", Description: "network monitoring"},
			}},
		},
		Capabilities: map[string]any{"fuse": true},
	}
	table := result.Table()
	assert.Contains(t, table, "FILE PROTECTION")
	assert.Contains(t, table, "25/25")
	assert.Contains(t, table, "fuse")
	assert.Contains(t, table, "✓")
	assert.Contains(t, table, "fusermount3")
	assert.Contains(t, table, "active backend:")
	assert.Contains(t, table, "NETWORK")
	assert.Contains(t, table, "0/20")
	assert.Contains(t, table, "EPERM")
	assert.Contains(t, table, "85/100")
}

func TestTableFormat_FallbackFlat(t *testing.T) {
	// No domains → falls back to flat capabilities
	result := &DetectResult{
		Platform:        "darwin",
		SecurityMode:    "sandbox-exec",
		ProtectionScore: 60,
		Capabilities:    map[string]any{"sandbox_exec": true, "fuse_t": false},
	}
	table := result.Table()
	assert.Contains(t, table, "CAPABILITIES")
	assert.Contains(t, table, "sandbox_exec")
}

func TestTable_LongDetailContinuationLine(t *testing.T) {
	const longDetail = "activated but not running (state: spawn scheduled, last exit: exit code 1)"
	result := &DetectResult{
		Platform:        "darwin",
		SecurityMode:    "esf",
		ProtectionScore: 70,
		Domains: []ProtectionDomain{
			{Name: "Command Control", Weight: 25, Score: 25, Backends: []DetectedBackend{
				{Name: "esf-exec", Available: true, Detail: "ok", Description: "exec auth"},
				{Name: "esf-liveness", Available: false, Detail: longDetail, Description: "liveness monitor"},
			}, Active: "esf-exec"},
		},
		Capabilities: map[string]any{"esf": true},
	}

	table := result.Table()
	lines := strings.Split(table, "\n")

	// The long detail must appear on a line that does NOT also contain the
	// Description, i.e. it's rendered on its own continuation line.
	var longDetailLine string
	for _, line := range lines {
		if strings.Contains(line, longDetail) {
			longDetailLine = line
			break
		}
	}
	if longDetailLine == "" {
		t.Fatalf("Table() output does not contain long detail %q:\n%s", longDetail, table)
	}
	if strings.Contains(longDetailLine, "liveness monitor") {
		t.Errorf("long detail line should not also contain Description, got: %q", longDetailLine)
	}

	// The backend row for the long-detail backend must still contain its
	// Name, status marker, and Description (just not the detail).
	var backendRowLine string
	for _, line := range lines {
		if strings.Contains(line, "esf-liveness") {
			backendRowLine = line
			break
		}
	}
	if backendRowLine == "" {
		t.Fatalf("Table() output does not contain backend row for esf-liveness:\n%s", table)
	}
	if !strings.Contains(backendRowLine, "-") {
		t.Errorf("backend row should contain unavailable status marker, got: %q", backendRowLine)
	}
	if !strings.Contains(backendRowLine, "liveness monitor") {
		t.Errorf("backend row should contain Description, got: %q", backendRowLine)
	}

	// The short-detail backend renders inline on one line containing name,
	// detail, and description.
	var shortDetailLine string
	for _, line := range lines {
		if strings.Contains(line, "esf-exec") {
			shortDetailLine = line
			break
		}
	}
	if shortDetailLine == "" {
		t.Fatalf("Table() output does not contain backend row for esf-exec:\n%s", table)
	}
	if !strings.Contains(shortDetailLine, "ok") || !strings.Contains(shortDetailLine, "exec auth") {
		t.Errorf("short-detail backend row should contain name, detail, and description inline, got: %q", shortDetailLine)
	}
}

func TestDetectResult_JSONContainsDomains(t *testing.T) {
	result := &DetectResult{
		Platform:        "linux",
		SecurityMode:    "full",
		ProtectionScore: 100,
		Domains: []ProtectionDomain{
			{Name: "File Protection", Weight: 25, Score: 25, Backends: []DetectedBackend{
				{Name: "fuse", Available: true, Detail: "fusermount3"},
			}},
		},
		Capabilities: map[string]any{"fuse": true},
	}
	data, err := result.JSON()
	assert.NoError(t, err)
	var parsed map[string]any
	assert.NoError(t, json.Unmarshal(data, &parsed))
	assert.Contains(t, parsed, "domains")
	assert.Contains(t, parsed, "capabilities")
}
