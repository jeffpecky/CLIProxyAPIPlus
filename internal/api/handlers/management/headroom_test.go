package management

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestHeadroomStartupTimeoutAllowsSlowWindowsColdStart(t *testing.T) {
	if headroomStartupTimeout != 30*time.Second {
		t.Fatalf("startup timeout = %v, want 30s", headroomStartupTimeout)
	}
}

func TestWaitForHeadroomHealthyAllowsTwelveSecondColdStart(t *testing.T) {
	current := time.Unix(0, 0)
	healthy := func(string) bool { return current.Sub(time.Unix(0, 0)) >= 12*time.Second }
	now := func() time.Time { return current }
	sleep := func(duration time.Duration) { current = current.Add(duration) }

	if !waitForHeadroomHealthyWithClock("http://127.0.0.1:8787", 30*time.Second, 200*time.Millisecond, healthy, now, sleep) {
		t.Fatal("expected Headroom becoming healthy after 12s to succeed")
	}
}

func TestClassifyHeadroomStartReusesHealthyUnmanagedProcess(t *testing.T) {
	result := classifyHeadroomStart(true, 4321, false)
	if result.kind != headroomStartReuse || result.managed {
		t.Fatalf("result = %+v, want unmanaged reuse", result)
	}
}

func TestClassifyHeadroomStartRejectsOccupiedUnhealthyPort(t *testing.T) {
	result := classifyHeadroomStart(false, 4321, false)
	if result.kind != headroomStartConflict || result.ownerPID != 4321 {
		t.Fatalf("result = %+v, want conflict owned by PID 4321", result)
	}
}

func TestClassifyHeadroomStartAllowsFreePort(t *testing.T) {
	result := classifyHeadroomStart(false, 0, false)
	if result.kind != headroomStartSpawn {
		t.Fatalf("result = %+v, want spawn", result)
	}
}

func TestStopHeadroomPIDUsesProcessTreeCleanup(t *testing.T) {
	original := stopProcessTree
	t.Cleanup(func() { stopProcessTree = original })
	stoppedPID := 0
	stopProcessTree = func(pid int) error { stoppedPID = pid; return nil }

	if err := stopProcessTree(1924); err != nil {
		t.Fatal(err)
	}

	if stoppedPID != 1924 {
		t.Fatalf("stopped PID = %d, want 1924", stoppedPID)
	}
}

func TestHeadroomStartupFailureIncludesBoundedSanitizedDiagnostics(t *testing.T) {
	originalPath := headroomLogPath
	headroomLogPath = filepath.Join(t.TempDir(), "proxy.log")
	t.Cleanup(func() { headroomLogPath = originalPath })

	lines := make([]string, 0, headroomLogTailLines+1)
	lines = append(lines, "discard me")
	for i := 0; i < headroomLogTailLines; i++ {
		lines = append(lines, "startup line")
	}
	lines[len(lines)-1] = "target=https://user:secret@example.test/path?token=secret API_KEY=secret"
	if err := os.WriteFile(headroomLogPath, []byte(strings.Join(lines, "\n")), 0o600); err != nil {
		t.Fatal(err)
	}

	payload := headroomStartupFailure("readiness_timeout", "Headroom did not become healthy within 30s.", nil)
	if payload["code"] != "HEADROOM_STARTUP_FAILED" || payload["stage"] != "readiness_timeout" {
		t.Fatalf("payload = %#v", payload)
	}
	logTail, _ := payload["log_tail"].(string)
	if strings.Contains(logTail, "discard me") || strings.Contains(logTail, "secret") {
		t.Fatalf("log_tail was not bounded and sanitized: %q", logTail)
	}
	if !strings.Contains(logTail, "https://example.test/path") || !strings.Contains(logTail, "API_KEY=[REDACTED]") {
		t.Fatalf("log_tail missing sanitized diagnostics: %q", logTail)
	}
}

func TestHeadroomStartupFailureIncludesExitCode(t *testing.T) {
	originalPath := headroomLogPath
	headroomLogPath = filepath.Join(t.TempDir(), "missing.log")
	t.Cleanup(func() { headroomLogPath = originalPath })

	exitCode := 2
	payload := headroomStartupFailure("process_exited", "Headroom proxy exited during startup.", &exitCode)
	if payload["exit_code"] != 2 {
		t.Fatalf("exit_code = %v, want 2", payload["exit_code"])
	}
	if payload["hint"] == "" {
		t.Fatal("expected remediation hint")
	}
}
