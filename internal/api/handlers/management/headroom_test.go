package management

import (
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
