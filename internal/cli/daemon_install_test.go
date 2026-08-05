package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// setupFakeTools installs fake launchctl/systemctl executables on PATH that
// append each invocation to a calls file, and redirects HOME to a temp dir so
// service files land in the test sandbox. The body script runs after the call
// is recorded and controls the fake's behavior; an empty body means exit 0.
func setupFakeTools(t *testing.T, launchctlBody, systemctlBody string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake-tool tests require /bin/sh")
	}
	binDir := t.TempDir()
	callsFile := filepath.Join(t.TempDir(), "calls")
	t.Setenv("AGENTSH_TEST_CALLS", callsFile)
	writeTool := func(name, body string) {
		script := "#!/bin/sh\n" +
			"echo \"$(basename \"$0\") $@\" >> \"$AGENTSH_TEST_CALLS\"\n" +
			body + "\nexit 0\n"
		if err := os.WriteFile(filepath.Join(binDir, name), []byte(script), 0o755); err != nil {
			t.Fatalf("write fake %s: %v", name, err)
		}
	}
	writeTool("launchctl", launchctlBody)
	writeTool("systemctl", systemctlBody)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("HOME", t.TempDir())
	return callsFile
}

// recordedCalls returns the fake-tool invocations, one per line, or nil if no
// tool was ever called.
func recordedCalls(t *testing.T, callsFile string) []string {
	t.Helper()
	data, err := os.ReadFile(callsFile)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("read calls file: %v", err)
	}
	return strings.Split(strings.TrimSpace(string(data)), "\n")
}

func newTestCmd() (*cobra.Command, *bytes.Buffer) {
	cmd := &cobra.Command{}
	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	return cmd, buf
}

func assertCalls(t *testing.T, got, want []string) {
	t.Helper()
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("recorded calls:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestInstallLaunchd_UnloadsBeforeLoad(t *testing.T) {
	callsFile := setupFakeTools(t, "", "")
	cmd, buf := newTestCmd()

	if err := installLaunchdService(cmd, false); err != nil {
		t.Fatalf("install failed: %v", err)
	}

	plistPath := getLaunchdPlistPath()
	assertCalls(t, recordedCalls(t, callsFile), []string{
		"launchctl unload " + plistPath,
		"launchctl load " + plistPath,
	})
	if _, err := os.Stat(plistPath); err != nil {
		t.Errorf("plist not written inside sandbox HOME: %v", err)
	}
	if !strings.Contains(buf.String(), "Service loaded and started") {
		t.Errorf("missing success message, got: %s", buf.String())
	}
}

func TestInstallLaunchd_ForceReplacesLoadedDefinition(t *testing.T) {
	callsFile := setupFakeTools(t, "", "")
	plistPath := getLaunchdPlistPath()
	if err := os.MkdirAll(filepath.Dir(plistPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(plistPath, []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd, _ := newTestCmd()
	if err := installLaunchdService(cmd, true); err != nil {
		t.Fatalf("install --force failed: %v", err)
	}

	assertCalls(t, recordedCalls(t, callsFile), []string{
		"launchctl unload " + plistPath,
		"launchctl load " + plistPath,
	})
	content, err := os.ReadFile(plistPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), "ai.canyonroad.agentsh.daemon") {
		t.Errorf("plist not rewritten, still: %s", content)
	}
}

func TestInstallLaunchd_ExistingPlistWithoutForce(t *testing.T) {
	callsFile := setupFakeTools(t, "", "")
	plistPath := getLaunchdPlistPath()
	if err := os.MkdirAll(filepath.Dir(plistPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(plistPath, []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd, buf := newTestCmd()
	if err := installLaunchdService(cmd, false); err != nil {
		t.Fatalf("install failed: %v", err)
	}

	if calls := recordedCalls(t, callsFile); calls != nil {
		t.Errorf("expected no launchctl calls without --force, got %v", calls)
	}
	if !strings.Contains(buf.String(), "Use --force to overwrite") {
		t.Errorf("missing --force hint, got: %s", buf.String())
	}
}

func TestInstallLaunchd_LoadFailureIsError(t *testing.T) {
	setupFakeTools(t, `[ "$1" = "load" ] && exit 1`, "")
	cmd, _ := newTestCmd()

	err := installLaunchdService(cmd, false)
	if err == nil {
		t.Fatal("expected error when launchctl load fails")
	}
	if !strings.Contains(err.Error(), getLaunchdPlistPath()) {
		t.Errorf("error should mention plist path: %v", err)
	}
}
